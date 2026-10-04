package fleetlygrpc

// Delivery 上下文服务实现（Deployments/Revisions/Builds）。Deploy 是一站式
// 入口：两源归一化 → 校验 → Revision 冻结（内容寻址复用）→ admission。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/sourceupload"
)

type DeploymentsService struct {
	deliveryv1.UnimplementedDeploymentsServiceServer
	s *Services
}

// Deploy 归一化三源（image 直投 / Compose 受控子集 / 上传产物）并受理部署
// （admission 判定全在 engine.Submit）。
func (svc *DeploymentsService) Deploy(ctx context.Context, req *deliveryv1.DeployRequest) (*deliveryv1.DeployResponse, error) {
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	// 行级授权（ADR-0035）：App 归属 Team 比对（载行供归一化复用）。
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}

	// 上传产物形态的受理前置：行存在、归属同 Project、blob 在盘（跨项目
	// 引用拒绝——Upload 是 project 级材料，ADR-0019 附录 A.1）。
	var uploadRow *sourceupload.Upload
	if uid := req.GetUploadId(); uid != "" {
		row, err := svc.s.Uploads.Get(ctx, svc.s.DB.Runner(), uid)
		if err != nil {
			return nil, mapStateError(err, "upload")
		}
		if row.ProjectID != appRow.ProjectID {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"upload %s belongs to project %s, not project %s; upload the source again within the app's project",
				uid, row.ProjectID, appRow.ProjectID)
		}
		if !svc.s.UploadStore.BlobExists(row.Digest) {
			return nil, apperr.New("E_UPLOAD_UNAVAILABLE",
				"the uploaded source %s no longer has its blob on disk (digest %s)", uid, row.Digest).
				WithSuggestion("The upload was likely affected by a retention sweep or data-root migration; upload the source again.")
		}
		uploadRow = row
	}

	appSpec, err := normalizeDeploySource(req, appRow, uploadRow)
	if err != nil {
		return nil, err
	}

	// Revision 冻结（内容寻址复用：同内容只冻结一份；R1..Rn 序号 App 内单调）。
	rev, err := freezeRevision(ctx, svc.s, appRow, appSpec)
	if err != nil {
		return nil, mapStateError(err, "revision")
	}

	d, adm, err := svc.s.Engine.Submit(ctx, engine.SubmitRequest{
		AppID: appRow.ID, RevisionID: rev.ID,
		IdempotencyKey: req.GetIdempotencyKey(), CommitSHA: req.GetCommitSha(),
		Supersede: req.GetSupersede(),
	})
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	return &deliveryv1.DeployResponse{Deployment: deploymentMsg(*d), Admission: admissionMsg(adm)}, nil
}

// freezeRevision 冻结 Revision（内容寻址复用：同内容只冻结一份；R1..Rn
// 序号 App 内单调）。Deploy 与 webhook 触发共用——审计 actor/source 取
// ctx（webhook 路径经 WithAuditOverride 标注）。
func freezeRevision(ctx context.Context, s *Services, appRow *app.App, appSpec *specv1.AppSpec) (*revision.Revision, error) {
	body, err := marshalSpec(appSpec)
	if err != nil {
		return nil, err
	}
	var rev *revision.Revision
	var created *audit.Entry
	err = s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			if existing, ferr := s.Revisions.FindByDigest(ctx, tx, appRow.ID, revision.Digest(body)); ferr == nil {
				rev = existing
				return nil // 内容寻址复用：不冻结新行、不落新审计
			}
			seq, serr := s.Revisions.NextSeq(ctx, tx, appRow.ID)
			if serr != nil {
				return serr
			}
			rev = &revision.Revision{ID: newID(), AppID: appRow.ID, Seq: seq, Spec: body}
			if cerr := s.Revisions.Create(ctx, tx, rev); cerr != nil {
				return cerr
			}
			created = &audit.Entry{
				ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "revision.create",
				Resource: "revision/" + rev.ID, AfterFP: rev.Digest,
			}
			return nil
		},
		auditsFrom: func() []*audit.Entry {
			if created == nil {
				return nil
			}
			return []*audit.Entry{created}
		},
	})
	if err != nil {
		return nil, err
	}
	return rev, nil
}

// normalizeDeploySource 归一化三源：image 直投 / Compose 受控子集 / 上传
// 产物。直投与上传形态的探针声明（http_probe/tcp_probe，B2）互斥；compose
// 形态的探针经 healthcheck 扩展键（直投/上传旗标与 compose 互斥）。
func normalizeDeploySource(req *deliveryv1.DeployRequest, appRow *app.App, uploadRow *sourceupload.Upload) (*specv1.AppSpec, error) {
	sources := 0
	for _, v := range []string{req.GetImage(), req.GetComposeYaml(), req.GetUploadId()} {
		if v != "" {
			sources++
		}
	}
	if sources > 1 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "image, compose_yaml and upload_id are mutually exclusive; exactly one source form per deploy")
	}
	// builder 面是上传形态专属（ADR-0032）：image 直投无构建、compose 受控
	// 子集无构建源，携带即拒。
	if req.GetUploadId() == "" && (req.GetBuilder() != "" || req.GetRailpackVersion() != "" || req.GetOutputDir() != "") {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"builder, railpack_version and output_dir are for upload deploys only; image and compose deploys do not carry a builder")
	}
	var probe *specv1.HealthcheckSpec
	switch {
	case req.GetHttpProbe() != "" && req.GetTcpProbe() != 0:
		return nil, apperr.New("E_INVALID_ARGUMENT", "http_probe and tcp_probe are mutually exclusive")
	case req.GetHttpProbe() != "":
		if !strings.HasPrefix(req.GetHttpProbe(), "/") {
			return nil, apperr.New("E_INVALID_ARGUMENT", "http_probe: must be an absolute path starting with '/'")
		}
		probe = &specv1.HealthcheckSpec{Probe: &specv1.HealthcheckSpec_HttpPath{HttpPath: req.GetHttpProbe()}, Retries: 3}
	case req.GetTcpProbe() != 0:
		if req.GetTcpProbe() < 1 || req.GetTcpProbe() > 65535 {
			return nil, apperr.New("E_INVALID_ARGUMENT", "tcp_probe: port out of range (1-65535)")
		}
		probe = &specv1.HealthcheckSpec{Probe: &specv1.HealthcheckSpec_TcpPort{TcpPort: req.GetTcpProbe()}, Retries: 3}
	}
	switch {
	case req.GetImage() != "":
		s, err := spec.ImageDeploy(appRow.ID, appRow.ProjectID, req.GetImage(), req.GetProcessName(), probe)
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	case req.GetComposeYaml() != "":
		if req.GetHttpProbe() != "" || req.GetTcpProbe() != 0 {
			return nil, apperr.New("E_INVALID_ARGUMENT", "http_probe/tcp_probe are for image or upload deploys; compose declares probes via healthcheck.http_path/tcp_port")
		}
		var doc spec.ComposeDoc
		if err := yaml.Unmarshal([]byte(req.GetComposeYaml()), &doc); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "compose_yaml: invalid YAML: %s", err.Error()).WithCause(err)
		}
		s, err := spec.NormalizeCompose(doc, appRow.ID, appRow.ProjectID)
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	case req.GetUploadId() != "":
		// uploadRow 已在 Deploy 受理前置解析（行存在 + 归属 + blob 在盘）。
		if req.GetBuilder() != "" && req.GetBuilder() != spec.BuilderDockerfile && req.GetDockerfile() != "" {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"dockerfile is only set with the dockerfile builder (got builder %q); railpack builds detect the source, static serves an artifact directory", req.GetBuilder())
		}
		if req.GetRailpackVersion() != "" && req.GetBuilder() != spec.BuilderRailpack {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"railpack_version is only set with the railpack builder (got %q)", builderOrDefault(req.GetBuilder()))
		}
		if req.GetOutputDir() != "" && req.GetBuilder() != spec.BuilderStatic {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"output_dir is only set with the static builder (got %q)", builderOrDefault(req.GetBuilder()))
		}
		if req.GetBuilder() == spec.BuilderRailpack && req.GetRailpackVersion() == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"railpack_version is required for railpack builds (a bare semver like \"0.39.0\"; the platform's version appears in the build error if they mismatch)")
		}
		s, err := spec.UploadDeploy(spec.UploadDeployInput{
			AppID: appRow.ID, Project: appRow.ProjectID, UploadID: uploadRow.ID,
			ProcessName: req.GetProcessName(), Builder: req.GetBuilder(),
			Dockerfile: req.GetDockerfile(), RailpackVersion: req.GetRailpackVersion(),
			OutputDir: req.GetOutputDir(), Probe: probe,
		})
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "one of image, compose_yaml or upload_id is required")
	}
}

// builderOrDefault 是错误文本用的 builder 缺省名（空 = dockerfile 轨）。
func builderOrDefault(b string) string {
	if b == "" {
		return spec.BuilderDockerfile
	}
	return b
}

func (svc *DeploymentsService) GetDeployment(ctx context.Context, req *deliveryv1.GetDeploymentRequest) (*deliveryv1.GetDeploymentResponse, error) {
	if err := svc.s.authorizeDeploymentID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	d, err := svc.s.Deployments.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	return &deliveryv1.GetDeploymentResponse{Deployment: deploymentMsg(*d)}, nil
}

func (svc *DeploymentsService) ListDeployments(ctx context.Context, req *deliveryv1.ListDeploymentsRequest) (*deliveryv1.ListDeploymentsResponse, error) {
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Deployments.ListByApp(ctx, svc.s.DB.Runner(),
		req.GetAppId(), req.GetAfterDeploymentId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	out := &deliveryv1.ListDeploymentsResponse{}
	for _, d := range list {
		out.Deployments = append(out.Deployments, deploymentMsg(d))
	}
	return out, nil
}

func (svc *DeploymentsService) CancelDeployment(ctx context.Context, req *deliveryv1.CancelDeploymentRequest) (*deliveryv1.CancelDeploymentResponse, error) {
	if err := svc.s.authorizeDeploymentID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	d, err := svc.s.Engine.Cancel(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	return &deliveryv1.CancelDeploymentResponse{Deployment: deploymentMsg(*d)}, nil
}

func (svc *DeploymentsService) Rollback(ctx context.Context, req *deliveryv1.RollbackRequest) (*deliveryv1.RollbackResponse, error) {
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	d, adm, err := svc.s.Engine.Rollback(ctx, req.GetAppId(), req.GetToRevision())
	if err != nil {
		// 哨兵判定（Q-13）：engine 哨兵 → E_NO_BASELINE（映射目标不变，
		// 只把文案 Contains 换成 errors.Is——文案再改不破坏错误契约）。
		if errors.Is(err, engine.ErrNoSuccessfulBaseline) {
			return nil, apperr.New("E_NO_BASELINE", "app %s has no successful deployment to roll back to", req.GetAppId()).WithCause(err)
		}
		return nil, mapStateError(err, "rollback")
	}
	return &deliveryv1.RollbackResponse{Deployment: deploymentMsg(*d), Admission: admissionMsg(adm)}, nil
}

// ---- Revisions ----

type RevisionsService struct {
	deliveryv1.UnimplementedRevisionsServiceServer
	s *Services
}

func (svc *RevisionsService) ListRevisions(ctx context.Context, req *deliveryv1.ListRevisionsRequest) (*deliveryv1.ListRevisionsResponse, error) {
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Revisions.ListByApp(ctx, svc.s.DB.Runner(),
		req.GetAppId(), req.GetAfterSeq(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	out := &deliveryv1.ListRevisionsResponse{}
	for _, rev := range list {
		out.Revisions = append(out.Revisions, &deliveryv1.Revision{
			Id: rev.ID, AppId: rev.AppID, Seq: rev.Seq, Digest: rev.Digest, CreatedAt: rev.CreatedAt,
		})
	}
	return out, nil
}

func (svc *RevisionsService) DiffRevisions(ctx context.Context, req *deliveryv1.DiffRevisionsRequest) (*deliveryv1.DiffRevisionsResponse, error) {
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	// 行级授权（ADR-0035）：diff 回读两侧冻结 spec 全文（含 env/secret 引用
	// 名）——跨租户读面。
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	from, err := svc.s.Revisions.GetBySeq(ctx, svc.s.DB.Runner(), req.GetAppId(), req.GetFromSeq())
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	to, err := svc.s.Revisions.GetBySeq(ctx, svc.s.DB.Runner(), req.GetAppId(), req.GetToSeq())
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	entries, err := spec.Diff(from.Spec, to.Spec)
	if err != nil {
		return nil, mapStateError(err, "revision diff")
	}
	out := &deliveryv1.DiffRevisionsResponse{}
	for _, e := range entries {
		out.Entries = append(out.Entries, &deliveryv1.DiffEntry{
			Path: e.Path, OldValue: scalarJSON(e.Old), NewValue: scalarJSON(e.New),
		})
	}
	return out, nil
}

// ---- Builds ----

type BuildsService struct {
	deliveryv1.UnimplementedBuildsServiceServer
	s *Services
}

func (svc *BuildsService) ListBuilds(ctx context.Context, req *deliveryv1.ListBuildsRequest) (*deliveryv1.ListBuildsResponse, error) {
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Builds.ListByApp(ctx, svc.s.DB.Runner(),
		req.GetAppId(), req.GetAfterBuildId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "build")
	}
	out := &deliveryv1.ListBuildsResponse{}
	for _, b := range list {
		out.Builds = append(out.Builds, buildMsg(b))
	}
	return out, nil
}

// StreamBuildLogs 读构建日志（B4 + ADR-0040 决策 3）：环形缓冲（live +
// 末 8 个 build）优先；缓冲不在场（已淘汰/重启丢失）且 Logging 面在册 →
// VL 保留窗回读（kind=build 全量帧）。follow 时轮询增量续流至终态。
// 未知 build id 是精确拒绝——不静默空流。游标用帧序列号（N0.1 P2-1）：
// 环形缓冲回绕丢帧后按 seq 续流，不受 len 位置假象影响。
func (svc *BuildsService) StreamBuildLogs(req *deliveryv1.StreamBuildLogsRequest, stream deliveryv1.BuildsService_StreamBuildLogsServer) error {
	if req.GetBuildId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "build_id: must not be empty")
	}
	ctx := stream.Context()
	// 行级授权（ADR-0035）：载 Build 行比对归属（未知 id 由此给出 404）。
	if err := svc.s.authorizeBuildID(ctx, req.GetBuildId()); err != nil {
		return err
	}
	var lastSeq int64 = -1
	newFrames := 0
	flush := func() error {
		frames, last := svc.s.Engine.RecentBuildLogsAfter(req.GetBuildId(), lastSeq)
		for _, f := range frames {
			if err := stream.Send(&deliveryv1.StreamBuildLogsResponse{
				BuildId: req.GetBuildId(),
				Time:    f.Time.UTC().Format(timeFormatRFC3339),
				Line:    f.Line,
			}); err != nil {
				return err
			}
		}
		lastSeq, newFrames = last, len(frames)
		return nil
	}
	if err := flush(); err != nil {
		return err
	}
	// 缓冲不在场的终态 build：VL 保留窗回读（历史全量；ring 只保末 8 个，
	// 重启即失——持久化面正是为此）。行级隔离 = 查询构造只携带本 build
	// 的域锚（fleetly_build=<id> + 归属域字段）。
	if newFrames == 0 && svc.s.Logging != nil {
		b, err := svc.s.Builds.Get(ctx, svc.s.DB.Runner(), req.GetBuildId())
		if err == nil && b.State.Terminal() {
			if backfillErr := svc.backfillBuildLogs(ctx, b, stream); backfillErr != nil {
				return backfillErr
			}
		}
	}
	if !req.GetFollow() {
		return nil
	}
	// follow：至终态（终态后缓冲不再增长，最后一拍无新帧即收口）。
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if err := flush(); err != nil {
			return err
		}
		if b, err := svc.s.Builds.Get(ctx, svc.s.DB.Runner(), req.GetBuildId()); err == nil && b.State.Terminal() && newFrames == 0 {
			return nil
		}
	}
}

// backfillBuildLogs 从 VL 回读一个终态 build 的日志（域锚 = Build 行的
// 归属 App/Project + Team 实取）。
func (svc *BuildsService) backfillBuildLogs(ctx context.Context, b *build.Build, stream deliveryv1.BuildsService_StreamBuildLogsServer) error {
	appRow, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), b.AppID)
	if err != nil {
		return mapStateError(err, "app")
	}
	proj, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), appRow.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	w := buildLogBackfillWriter{stream: stream, buildID: b.ID}
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: proj.TeamID, Project: appRow.ProjectID, App: appRow.ID},
		Source:    b.ID,
	}
	if err := svc.s.Logging.Query(ctx, q, &w); err != nil {
		return mapStateError(err, "logs")
	}
	return nil
}

// buildLogBackfillWriter 把 VL 帧转发为 RPC 流帧。
type buildLogBackfillWriter struct {
	stream  deliveryv1.BuildsService_StreamBuildLogsServer
	buildID string
}

func (w *buildLogBackfillWriter) WriteLog(_ context.Context, f capability.LogFrame) error {
	return w.stream.Send(&deliveryv1.StreamBuildLogsResponse{
		BuildId: w.buildID,
		Time:    f.Time.UTC().Format(timeFormatRFC3339),
		Line:    f.Line,
	})
}

// scalarJSON 渲染 diff 标量（nil → 空串 = 缺失侧）。
func scalarJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
