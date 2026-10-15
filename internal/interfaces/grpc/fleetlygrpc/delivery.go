package fleetlygrpc

// Delivery 上下文服务实现（Deployments/Revisions/Builds）。Deploy 是一站式
// 入口：两源归一化 → 校验 → Revision 冻结（内容寻址复用）→ admission。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/composeintake"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
)

type DeploymentsService struct {
	deliveryv1.UnimplementedDeploymentsServiceServer
	s *Services
}

// Deploy 归一化四源（image 直投 / compose 受理语言 / 上传产物 / 裸
// AppSpec spec_file）并受理部署（合并/冻结/admission 全在
// engine.SubmitSpec 单咽喉——P3-7 下沉批；hook 闸门留 api）。
func (svc *DeploymentsService) Deploy(ctx context.Context, req *deliveryv1.DeployRequest) (*deliveryv1.DeployResponse, error) {
	// app_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致；四源互斥/
	// probe/port/protocol/builder 族归 P4-2，仍住手写。
	// 行级授权（ADR-0035）：App 归属 Team 比对（载行供归一化复用）。
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}

	// spec_file 形态（第四源，F3.5）先解析：Source.upload 引用要在受理
	// 前置走与 upload_id 形态同一条归属/blob 执法（intake 形态不放松
	// project 级材料纪律），解析产物直通归一化（单次解析）。
	var specFile *specv1.AppSpec
	if body := req.GetSpecFile(); body != "" {
		s, err := spec.ParseSpecFile([]byte(body), appRow.ID, appRow.ProjectID)
		if err != nil {
			return nil, mapValidationError(err)
		}
		specFile = s
	}

	// 上传产物形态的受理前置：行存在、归属同 Project、blob 在盘（跨项目
	// 引用拒绝——Upload 是 project 级材料，ADR-0019 附录 A.1；spec_file
	// 形态的 Source.upload 同一条执法）。
	var uploadRow *sourceupload.Upload
	uploadID := req.GetUploadId()
	if uploadID == "" && specFile != nil {
		uploadID = specFile.GetSource().GetUpload().GetId()
	}
	if uploadID != "" {
		row, err := svc.s.Uploads.Get(ctx, svc.s.DB.Runner(), uploadID)
		if err != nil {
			return nil, mapStateError(err, "upload")
		}
		if row.ProjectID != appRow.ProjectID {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"upload %s belongs to project %s, not project %s; upload the source again within the app's project",
				uploadID, row.ProjectID, appRow.ProjectID)
		}
		if !svc.s.UploadStore.BlobExists(row.Digest) {
			return nil, apperr.New("E_UPLOAD_UNAVAILABLE",
				"the uploaded source %s no longer has its blob on disk (digest %s)", uploadID, row.Digest).
				WithSuggestion("The upload was likely affected by a retention sweep or data-root migration; upload the source again.")
		}
		uploadRow = row
	}

	// 变量环境装载失败上抛（P0-7.1）：不吞错——mapStateError 兜 E_INTERNAL。
	varLookup, err := deployVarLookup(ctx, svc.s, appRow)
	if err != nil {
		return nil, mapStateError(err, "variables")
	}
	appSpec, err := normalizeDeploySource(req, appRow, uploadRow, specFile, varLookup)
	if err != nil {
		return nil, err
	}

	// Spec 所有权分裂（ADR-0072）：配置了 git hook 的 App 材料归 git——
	// 材料源（image/compose/upload）硬闸门（override_hook 一次性全量
	// 接管）；spec_file 是运行时 overlay（材料半边取冻结侧，提交件
	// source 被忽略——防旧 ref 构建旧 commit）。此处只判形状：闸门留在
	// api，合并执行随冻结/admission 下沉 engine.SubmitSpec（P3-7）。
	hookOwned := false
	merge := engine.MergeNone
	if hookRow, herr := svc.s.Hooks.Get(ctx, svc.s.DB.Runner(), appRow.ID); herr == nil {
		hookOwned = true
		if !req.GetOverrideHook() && req.GetSpecFile() == "" {
			return nil, apperr.New("E_APP_HOOK_OWNED",
				"app %s is deployed from a git hook (repo %s); material sources are hook-owned — runtime changes go through spec_file, env through app variables, and material through the repo",
				appRow.ID, hookRow.Repo)
		}
		if req.GetSpecFile() != "" && !req.GetOverrideHook() {
			// 冻结材料 + 提交件运行时（提交件 source 忽略）——执行在 engine。
			merge = engine.MergeOverlay
		}
	} else if !errors.Is(herr, state.ErrNotFound) {
		return nil, mapStateError(herr, "hook")
	}

	// 受理单咽喉（P3-7）：合并（方向参数化）→ 三级变量合成 → Revision
	// 冻结（内容寻址复用）→ admission 全在 engine.SubmitSpec；无冻结
	// Revision（hook 配置后从未部署）= 首部署原样受理的语义在 engine 同形。
	// rev 响应面不回显（与现行一致——deployment.to_revision 已承载锚定）。
	_, d, adm, err := svc.s.Engine.SubmitSpec(ctx, engine.SubmitSpecRequest{
		AppID: appRow.ID, Spec: appSpec, SourceText: req.GetComposeYaml(),
		HookOwned: hookOwned, Merge: merge,
		Submit: &engine.SubmitOptions{
			IdempotencyKey: req.GetIdempotencyKey(), CommitSHA: req.GetCommitSha(),
			Supersede: req.GetSupersede(),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	return &deliveryv1.DeployResponse{Deployment: deploymentMsg(*d), Admission: admissionMsg(adm)}, nil
}

// deployVarLookup 合成 compose 官方插值的平台变量环境：Project 层
// SharedVariable 为底、App 层 AppVariable 顶（App 胜出——与受理咽喉
// engine.SubmitSpec 的三级合成同向；compose 形态的 env 值在受理期消解
// 冻结，ADR-0074）。环境一次性物化（两次列表查询），闭包查表。装载失败
// 上抛（P0-7.1）：DB 瞬断不得静默吞成空环境——那会把内部故障伪装成
// "compose 变量不可消解"的 E_INVALID_ARGUMENT（fail-closed 消解律的用户面
// 失真）；与 engine 侧合成的 ListActive 同纪律，经 mapStateError 落
// E_INTERNAL。
func deployVarLookup(ctx context.Context, s *Services, appRow *app.App) (composeintake.VariableLookup, error) {
	env := map[string]string{}
	vars, err := s.SharedVariables.ListActive(ctx, s.DB.Runner(), appRow.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, v := range vars {
		env[v.Name] = v.Value
	}
	appVars, err := s.AppVariables.ListActive(ctx, s.DB.Runner(), appRow.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range appVars {
		env[v.Name] = v.Value
	}
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}, nil
}

// normalizeDeploySource 归一化四源：image 直投 / compose 受理语言 / 上传
// 产物 / 裸 AppSpec（spec_file，F3.5）。直投与上传形态的探针声明
// （http_probe/tcp_probe，B2）互斥；compose 形态的探针经 healthcheck 扩展键
// （直投/上传旗标与 compose 互斥）。specFile 是 spec_file 形态在 Deploy
// 受理前置已解析的产物（Source.upload 归属执法需先行），此处直通。
func normalizeDeploySource(req *deliveryv1.DeployRequest, appRow *app.App, uploadRow *sourceupload.Upload, specFile *specv1.AppSpec, varLookup composeintake.VariableLookup) (*specv1.AppSpec, error) {
	sources := 0
	for _, v := range []string{req.GetImage(), req.GetComposeYaml(), req.GetUploadId(), req.GetSpecFile()} {
		if v != "" {
			sources++
		}
	}
	if sources > 1 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "image, compose_yaml, upload_id and spec_file are mutually exclusive; exactly one source form per deploy")
	}
	// builder 面是上传形态专属（ADR-0032）：image 直投无构建、compose 受控
	// 子集无构建源，携带即拒。
	if req.GetUploadId() == "" && (req.GetBuilder() != "" || req.GetRailpackVersion() != "" || req.GetOutputDir() != "") {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"builder, railpack_version and output_dir are for upload deploys only; image and compose deploys do not carry a builder")
	}
	// env 是 image/upload 形态的 App 级变量直传（ADR-0043 决策 2）：compose
	// 自带 environment 声明面，携带即拒（http_probe 旗标同款先例）。
	if req.GetComposeYaml() != "" && len(req.GetEnv()) > 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"env is for image or upload deploys; compose declares variables via each service's environment")
	}
	// spec_file 自带全部声明面：单进程形态旗标（进程名/探针/端口/变量）
	// 与裸 spec 互斥——文件是期望状态唯一来源（builder 族已由上面的
	// upload 专属执法盖住）。
	if req.GetSpecFile() != "" &&
		(req.GetProcessName() != "" || req.GetHttpProbe() != "" || req.GetTcpProbe() != 0 ||
			req.GetPort() != 0 || req.GetProtocol() != "" || len(req.GetEnv()) > 0) {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"process_name, probe, port, protocol and env flags are for image or upload deploys; spec_file carries the full process declaration")
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
	// 端口声明（F3.5）：直投与上传形态的 Route-facing 面。compose 自带
	// ports 声明（服务键），携带即拒（http_probe/env 旗标同款先例）。
	ports, err := deployRequestPorts(req)
	if err != nil {
		return nil, err
	}
	switch {
	case req.GetImage() != "":
		s, err := spec.ImageDeploy(appRow.ID, appRow.ProjectID, req.GetImage(), req.GetProcessName(), probe, req.GetEnv(), ports)
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	case req.GetComposeYaml() != "":
		if req.GetHttpProbe() != "" || req.GetTcpProbe() != 0 {
			return nil, apperr.New("E_INVALID_ARGUMENT", "http_probe/tcp_probe are for image or upload deploys; compose declares probes via healthcheck.x-fleetly-http-path/x-fleetly-tcp-port")
		}
		// compose 受理语言 = compose-go 官方解析 + fleetly 方言执法
		// （ADR-0074）；官方插值环境 = 平台变量（Shared ∪ AppVariable，
		// App 胜出——与受理咽喉的三级合成同向，未消解即拒）。
		s, err := composeintake.FromYAML([]byte(req.GetComposeYaml()), appRow.ID, appRow.ProjectID, varLookup)
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
			OutputDir: req.GetOutputDir(), Probe: probe, Env: req.GetEnv(),
			Ports: ports,
		})
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	case req.GetSpecFile() != "":
		// 受理前置已解析并校验（AppRef 权威覆写 + ValidateApp 叶子执法
		// + Source.upload 归属/blob 检查在 Deploy）；此处直通——归一化
		// 语义 = "文件即期望状态"。
		return specFile, nil
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "one of image, compose_yaml, upload_id or spec_file is required")
	}
}

// builderOrDefault 是错误文本用的 builder 缺省名（空 = dockerfile 轨）。
func builderOrDefault(b string) string {
	if b == "" {
		return spec.BuilderDockerfile
	}
	return b
}

// deployRequestPorts 解析 DeployRequest 的端口声明（F3.5）：直投与上传
// 形态专属（compose 自带服务键 ports，携带即拒——归一化层引用隔离在此
// 单点收口）；protocol 缺省 http（Route 同词汇）；端口域与协议枚举的
// 叶子级复核由 ValidateProcess 承载（归一化产物必经）。
func deployRequestPorts(req *deliveryv1.DeployRequest) ([]*specv1.PortSpec, error) {
	if req.GetComposeYaml() != "" && (req.GetPort() != 0 || req.GetProtocol() != "") {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"port and protocol are for image or upload deploys; compose declares ports per service (\"8080\" or \"8080/h2c\")")
	}
	if req.GetPort() == 0 {
		if req.GetProtocol() != "" {
			return nil, apperr.New("E_INVALID_ARGUMENT", "protocol is only meaningful together with a declared port")
		}
		return nil, nil
	}
	if req.GetPort() < 1 || req.GetPort() > 65535 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "port: out of range (1-65535)")
	}
	var proto specv1.Protocol
	switch p := req.GetProtocol(); p {
	case "":
		proto = specv1.Protocol_PROTOCOL_HTTP
	case "http":
		proto = specv1.Protocol_PROTOCOL_HTTP
	case "h2c":
		proto = specv1.Protocol_PROTOCOL_H2C
	case "tcp":
		proto = specv1.Protocol_PROTOCOL_TCP
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "protocol %q must be http, h2c or tcp", p)
	}
	return []*specv1.PortSpec{{Port: req.GetPort(), Protocol: proto}}, nil
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
		req.GetAppId(), req.GetAfterDeploymentSeq(), listLimit(req.GetLimit()))
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

// RestartProcess 强制重排单个进程的载体（ADR-0069）：授权面与 Rollback
// 同款（app 域写面）；engine 哨兵 map 为精确错误码（仿 systemgrpc 组件
// 重启面——未部署与未知进程同归 E_NOT_FOUND，运行时缺子面是平台形态问题
// 归 E_INTERNAL）。
func (svc *DeploymentsService) RestartProcess(ctx context.Context, req *deliveryv1.RestartProcessRequest) (*deliveryv1.RestartProcessResponse, error) {
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	if err := svc.s.Engine.RestartProcess(ctx, req.GetAppId(), req.GetProcessName()); err != nil {
		switch {
		case errors.Is(err, engine.ErrProcessUnknown):
			return nil, apperr.New("E_NOT_FOUND",
				"process %q not found in app %s (unknown name or never deployed)",
				req.GetProcessName(), req.GetAppId()).WithCause(err)
		case errors.Is(err, engine.ErrRuntimeNoRestart):
			return nil, apperr.New("E_INTERNAL", "the runtime provider does not implement carrier restart").WithCause(err)
		}
		return nil, mapStateError(err, "app")
	}
	return &deliveryv1.RestartProcessResponse{}, nil
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
		out.Revisions = append(out.Revisions, revisionMsg(rev))
	}
	return out, nil
}

// GetRevision 回读单条 Revision 的冻结 AppSpec 全文（ADR-0071 冻结层）：
// 行级授权与 DiffRevisions 同款（冻结体含 env/secret 引用名——跨租户读面）。
func (svc *RevisionsService) GetRevision(ctx context.Context, req *deliveryv1.GetRevisionRequest) (*deliveryv1.GetRevisionResponse, error) {
	// app_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	row, err := svc.s.Revisions.GetBySeq(ctx, svc.s.DB.Runner(), req.GetAppId(), req.GetSeq())
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	spec := &specv1.AppSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(row.Spec, spec); err != nil {
		return nil, mapStateError(err, "revision spec")
	}
	return &deliveryv1.GetRevisionResponse{Revision: revisionMsg(*row), Spec: spec}, nil
}

// GetAppWorkloads 按 Revision 只读重放 Workload 投影（ADR-0071 投影层）。
// seq 缺省 = 当前生效（engine 侧 LatestSucceeded 解析）；渲染错误经
// mapStateError（ErrNotFound → E_NOT_FOUND，其余侧表解析失败如实上抛）。
func (svc *RevisionsService) GetAppWorkloads(ctx context.Context, req *deliveryv1.GetAppWorkloadsRequest) (*deliveryv1.GetAppWorkloadsResponse, error) {
	// app_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	row, ws, err := svc.s.Engine.RenderWorkloads(ctx, req.GetAppId(), req.GetSeq())
	if err != nil {
		return nil, mapStateError(err, "workloads")
	}
	out := &deliveryv1.GetAppWorkloadsResponse{Revision: revisionMsg(*row)}
	for _, w := range ws {
		out.Workloads = append(out.Workloads, workloadMsg(w))
	}
	return out, nil
}

func (svc *RevisionsService) DiffRevisions(ctx context.Context, req *deliveryv1.DiffRevisionsRequest) (*deliveryv1.DiffRevisionsResponse, error) {
	// app_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
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
		req.GetAppId(), req.GetAfterBuildSeq(), listLimit(req.GetLimit()))
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
