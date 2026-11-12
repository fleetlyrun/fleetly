// Package deliverygrpc 实现 fleetly.delivery.v1 的 Deployments/Revisions/
// Builds 服务与 fleetly.runtime.v1.Workloads 观测面：部署受理、预览、
// 等待、上传产物与构建日志。
package deliverygrpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/composeintake"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Deploy 是部署收敛面的窄消费接口（engine 门面实现）。
type Deploy interface {
	SubmitSpec(ctx context.Context, req api.SubmitSpecRequest) (*revision.Revision, *deployment.Deployment, *api.Admission, error)
	Cancel(ctx context.Context, id string) (*deployment.Deployment, error)
	Rollback(ctx context.Context, appID, toRevisionID string, confirm *api.ConfirmInput) (*deployment.Deployment, *api.Admission, error)
	RestartProcess(ctx context.Context, appID, process string) error
	ScaleProcess(ctx context.Context, appID, process string, replicas int64) (*deployment.Deployment, *api.Admission, error)
	RenderWorkloads(ctx context.Context, appID string, seq int64) (*revision.Revision, []capability.Workload, error)
	InspectAppWorkloads(ctx context.Context, appID string) ([]capability.WorkloadObservation, error)
	PreviewSpec(ctx context.Context, req api.SubmitSpecRequest) (*api.PreviewSpecResult, error)
	ApplyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) error
	ConfirmBaseSnapshot(ctx context.Context, appID string) (governance.ConfirmBase, error)
	TeardownAnchors(ctx context.Context, appID string) (governance.ConfirmBase, error)
	TeardownCascade(ctx context.Context, appID string) ([]api.TeardownCascadeEntry, error)
	ObservationSnapshot(appID string) []capability.WorkloadEvent
}

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeAppID(ctx context.Context, appID string) (*app.App, error)
	AuthorizeAppIDOnly(ctx context.Context, appID string) error
	AuthorizeDeploymentID(ctx context.Context, deploymentID string) error
	AuthorizeBuildID(ctx context.Context, buildID string) error
	AuthorizeProjectID(ctx context.Context, projectID string) error
}

// Registry 是构建日志环形缓冲的窄消费接口。
type Registry interface {
	RecentBuildLogsAfter(buildID string, after int64) ([]capability.LogFrame, int64)
}

// Deps 是 delivery 服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Deploy             Deploy
	Deployments        deployment.Repo
	Revisions          revision.Repo
	Builds             build.Repo
	Uploads            sourceupload.Repo
	UploadStore        func() sourceupload.Store
	Logging            func() capability.Logging
	Apps               app.Repo
	Projects           project.Repo
	Volumes            volume.Repo
	SharedVariables    sharedvariable.Repo
	AppVariables       appvariable.Repo
	Hooks              hook.Repo
	Registry           Registry
	Auth               Authorizer
	Commit             func(ctx context.Context, fact grpcapi.WriteFact) error
	MintConfirm        func(payload governance.ConfirmTicketPayload) (string, time.Duration, error)
	ParentProjectAlive func(projectID string) grpcapi.Check
	Wait               grpcapi.WaitSource
}

func (d Deps) uploadStore() sourceupload.Store {
	if d.UploadStore == nil {
		return nil
	}
	return d.UploadStore()
}

func (d Deps) logging() capability.Logging {
	if d.Logging == nil {
		return nil
	}
	return d.Logging()
}

// DeploymentsService 满足 deliveryv1.DeploymentsServiceServer。
type DeploymentsService struct {
	deliveryv1.UnimplementedDeploymentsServiceServer
	deps Deps
}

// RevisionsService 满足 deliveryv1.RevisionsServiceServer。
type RevisionsService struct {
	deliveryv1.UnimplementedRevisionsServiceServer
	deps Deps
}

// BuildsService 满足 deliveryv1.BuildsServiceServer。
type BuildsService struct {
	deliveryv1.UnimplementedBuildsServiceServer
	deps Deps
}

// WorkloadsService 满足 runtimev1.WorkloadsServiceServer。
type WorkloadsService struct {
	runtimev1.UnimplementedWorkloadsServiceServer
	deps Deps
}

// NewDeployments 构造部署服务（模板复合编排内层直调用）。
func NewDeployments(deps Deps) *DeploymentsService { return &DeploymentsService{deps: deps} }

// Register 注册 delivery 面四个服务。
func Register(srv *grpc.Server, deps Deps) {
	deliveryv1.RegisterDeploymentsServiceServer(srv, &DeploymentsService{deps: deps})
	deliveryv1.RegisterRevisionsServiceServer(srv, &RevisionsService{deps: deps})
	deliveryv1.RegisterBuildsServiceServer(srv, &BuildsService{deps: deps})
	runtimev1.RegisterWorkloadsServiceServer(srv, &WorkloadsService{deps: deps})
}

var (
	_ deliveryv1.DeploymentsServiceServer = (*DeploymentsService)(nil)
	_ deliveryv1.RevisionsServiceServer   = (*RevisionsService)(nil)
	_ deliveryv1.BuildsServiceServer      = (*BuildsService)(nil)
	_ runtimev1.WorkloadsServiceServer    = (*WorkloadsService)(nil)
)

// Deploy 归一化四源（image 直投 / compose 受理语言 / 上传产物 / 裸
// AppSpec spec_file）并受理部署（合并/冻结/admission 全在
// engine.SubmitSpec 单咽喉——P3-7 下沉批；hook 闸门留 api）。
func (svc *DeploymentsService) Deploy(ctx context.Context, req *deliveryv1.DeployRequest) (*deliveryv1.DeployResponse, error) {
	// 受理前半（段一）与 PreviewDeploy 共享单源（B1 预览管线，设计稿
	// §1.3——漏段一则预览不复执法，违反「确认的正是展示的」）。
	intake, err := svc.deployIntake(ctx, req)
	if err != nil {
		return nil, err
	}

	// 受理单咽喉（P3-7）：合并（方向参数化）→ 三级变量合成 → Revision
	// 冻结（内容寻址复用）→ admission 全在 engine.SubmitSpec；无冻结
	// Revision（hook 配置后从未部署）= 首部署原样受理的语义在 engine 同形。
	// rev 响应面不回显（与现行一致——deployment.to_revision 已承载锚定）。
	// B1 确认票透传（设计稿 §1.4 甲'）：通用验票已过 confirm 拦截器，
	// ④digest/⑤base 内容绑定与烧票在 engine 的 appMu 临界区内闭合；
	// 提交语义位（supersede/override_hook）随入参进④指纹。
	_, d, adm, err := svc.deps.Deploy.SubmitSpec(ctx, api.SubmitSpecRequest{
		AppID: intake.appRow.ID, Spec: intake.spec, SourceText: req.GetComposeYaml(),
		HookOwned: intake.hookOwned, Merge: intake.merge,
		Submit: &api.SubmitOptions{
			IdempotencyKey: req.GetIdempotencyKey(), CommitSHA: req.GetCommitSha(),
			Supersede: req.GetSupersede(),
		},
		Confirmation: deployConfirmInput(req),
	})
	if err != nil {
		if cerr := mapConfirmError(err); cerr != nil {
			return nil, cerr
		}
		return nil, grpcapi.MapStateError(err, "revision")
	}
	return &deliveryv1.DeployResponse{Deployment: deploymentMsg(*d), Admission: grpcapi.AdmissionMsg(adm)}, nil
}

// deployIntakeResult 是受理前半（段一）的产物。
type deployIntakeResult struct {
	appRow    *app.App
	spec      *specv1.AppSpec
	merge     api.MergeMode
	hookOwned bool
}

// deployIntake 是 Deploy/PreviewDeploy 共享的受理前半（B1 段一抽取，设计稿
// §1.3）：authorize → spec_file 解析 → upload 归属/blob 在盘 → 变量
// lookup → normalizeDeploySource → hook 闸门（E_APP_HOOK_OWNED）与 merge
// 裁决。Preview 与 Deploy 同一入口执法——授权/四源互斥（descriptor CEL
// 经 PreviewDeployRequest 内嵌 DeployRequest 单源继承）/材料归属/hook
// 所有权在预览侧全量复执法。
func (svc *DeploymentsService) deployIntake(ctx context.Context, req *deliveryv1.DeployRequest) (*deployIntakeResult, error) {
	// 受理面形状执法（app_id 必填 + 四源互斥/至少一源/spec_file×单进程
	// 旗标/probe 组/env×compose/port×compose/protocol/builder 族）已全量
	// 退役 DeployRequest descriptor 注解（P4-1 批 1 + P4-2 批 2）：由
	// protovalidate 拦截器在受理面前拦截，文案与退役前手写逐字节（含
	// %q 回显的静态化，契约变化在 P4-2 批 2 报告列明）。此处只留业务检查
	// （upload 归属/blob 在盘）、spec 组装。
	// 行级授权（ADR-0035）：App 归属 Team 比对（载行供归一化复用）。
	appRow, err := svc.deps.Auth.AuthorizeAppID(ctx, req.GetAppId())
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
			return nil, grpcapi.MapValidationError(err)
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
		row, err := svc.deps.Uploads.Get(ctx, uploadID)
		if err != nil {
			return nil, grpcapi.MapStateError(err, "upload")
		}
		if row.ProjectID != appRow.ProjectID {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"upload %s belongs to project %s, not project %s; upload the source again within the app's project",
				uploadID, row.ProjectID, appRow.ProjectID)
		}
		if !svc.deps.uploadStore().BlobExists(row.Digest) {
			return nil, apperr.New("E_UPLOAD_UNAVAILABLE",
				"the uploaded source %s no longer has its blob on disk (digest %s)", uploadID, row.Digest).
				WithSuggestion("The upload was likely affected by a retention sweep or data-root migration; upload the source again.")
		}
		uploadRow = row
	}

	// 变量环境装载失败上抛（P0-7.1）：不吞错——mapStateError 兜 E_INTERNAL。
	varLookup, err := DeployVarLookup(ctx, svc.deps.SharedVariables, svc.deps.AppVariables, appRow)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "variables")
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
	merge := api.MergeNone
	if hookRow, herr := svc.deps.Hooks.Get(ctx, appRow.ID); herr == nil {
		hookOwned = true
		if !req.GetOverrideHook() && req.GetSpecFile() == "" {
			return nil, apperr.New("E_APP_HOOK_OWNED",
				"app %s is deployed from a git hook (repo %s); material sources are hook-owned — runtime changes go through spec_file, env through app variables, and material through the repo",
				appRow.ID, hookRow.Repo)
		}
		if req.GetSpecFile() != "" && !req.GetOverrideHook() {
			// 冻结材料 + 提交件运行时（提交件 source 忽略）——执行在 engine。
			merge = api.MergeOverlay
		}
	} else if !errors.Is(herr, state.ErrNotFound) {
		return nil, grpcapi.MapStateError(herr, "hook")
	}
	return &deployIntakeResult{appRow: appRow, spec: appSpec, merge: merge, hookOwned: hookOwned}, nil
}

// deployConfirmInput 把请求的 confirmation 铸为 engine 入参（nil = 未携带
// ——拦截器已拦 REQUIRED，此处是透传面；语义位取服务端观察的本请求事实）。
func deployConfirmInput(req *deliveryv1.DeployRequest) *api.ConfirmInput {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	return &api.ConfirmInput{
		Ticket: conf.GetTicket(), Skip: conf.GetSkip(),
		Semantics: governance.ConfirmSemantics{
			Supersede:    req.GetSupersede(),
			OverrideHook: req.GetOverrideHook(),
		},
	}
}

// mapConfirmError 把 engine 确认哨兵映射为用户面 errcode（设计稿 §1.2 三
// 码；文案不内插票串/digest）。AdmissionError 包装经 Unwrap 穿透哨兵。
func mapConfirmError(err error) error {
	switch {
	case errors.Is(err, api.ErrConfirmStale):
		return apperr.New("E_CONFIRMATION_STALE",
			"the confirmation for this deploy no longer matches the current state").
			WithCause(err).
			WithSuggestion("The baseline moved (a newer revision, deployment, variable environment or hook row) or the ticket expired. Run the preview again and confirm the fresh ticket.")
	case errors.Is(err, api.ErrConfirmMismatch):
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the submitted request").
			WithCause(err).
			WithSuggestion("The ticket binds the previewed payload and its operator. Resend the exact previewed request, or mint a fresh preview ticket.")
	}
	return nil
}

// deployVarLookup 合成 compose 官方插值的平台变量环境：Project 层
// SharedVariable 为底、App 层 AppVariable 顶（App 胜出——与受理咽喉
// engine.SubmitSpec 的三级合成同向；compose 形态的 env 值在受理期消解
// 冻结，ADR-0074）。环境一次性物化（两次列表查询），闭包查表。装载失败
// 上抛（P0-7.1）：DB 瞬断不得静默吞成空环境——那会把内部故障伪装成
// "compose 变量不可消解"的 E_INVALID_ARGUMENT（fail-closed 消解律的用户面
// 失真）；与 engine 侧合成的 ListActive 同纪律，经 mapStateError 落
// E_INTERNAL。
func DeployVarLookup(ctx context.Context, shared sharedvariable.Repo, appVarsRepo appvariable.Repo, appRow *app.App) (composeintake.VariableLookup, error) {
	env := map[string]string{}
	vars, err := shared.ListActive(ctx, appRow.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, v := range vars {
		env[v.Name] = v.Value
	}
	appVars, err := appVarsRepo.ListActive(ctx, appRow.ID)
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
// （http_probe/tcp_probe，B2）组装为 HealthcheckSpec。形状与互斥执法
// （四源互斥/至少一源/builder 族/env×compose/spec_file×单进程旗标/
// probe 组/port×compose）已退役 DeployRequest descriptor 注解（P4-2 批 2，
// 拦截器受理前拦截）；此处只做归一化组装。specFile 是 spec_file 形态在
// Deploy 受理前置已解析的产物（Source.upload 归属执法需先行），此处直通。
func normalizeDeploySource(req *deliveryv1.DeployRequest, appRow *app.App, uploadRow *sourceupload.Upload, specFile *specv1.AppSpec, varLookup composeintake.VariableLookup) (*specv1.AppSpec, error) {
	var probe *specv1.HealthcheckSpec
	switch {
	case req.GetHttpProbe() != "":
		probe = &specv1.HealthcheckSpec{Probe: &specv1.HealthcheckSpec_HttpPath{HttpPath: req.GetHttpProbe()}, Retries: 3}
	case req.GetTcpProbe() != 0:
		probe = &specv1.HealthcheckSpec{Probe: &specv1.HealthcheckSpec_TcpPort{TcpPort: req.GetTcpProbe()}, Retries: 3}
	}
	// 端口声明（F3.5）：直投与上传形态的 Route-facing 面（形状执法已上移
	// descriptor，此处纯组装）。
	ports := deployRequestPorts(req)
	switch {
	case req.GetImage() != "":
		s, err := spec.ImageDeploy(appRow.ID, appRow.ProjectID, req.GetImage(), req.GetProcessName(), probe, req.GetEnv(), ports)
		if err != nil {
			return nil, grpcapi.MapValidationError(err)
		}
		return s, nil
	case req.GetComposeYaml() != "":
		// compose 受理语言 = compose-go 官方解析 + fleetly 方言执法
		// （ADR-0074）；官方插值环境 = 平台变量（Shared ∪ AppVariable，
		// App 胜出——与受理咽喉的三级合成同向，未消解即拒）。
		s, err := composeintake.FromYAML([]byte(req.GetComposeYaml()), appRow.ID, appRow.ProjectID, varLookup)
		if err != nil {
			return nil, grpcapi.MapValidationError(err)
		}
		return s, nil
	case req.GetUploadId() != "":
		// uploadRow 已在 Deploy 受理前置解析（行存在 + 归属 + blob 在盘）。
		// builder 旗标配对执法（dockerfile×builder/railpack_version×builder/
		// output_dir×builder/railpack 必版本/值域）已退役 descriptor（P4-2
		// 批 2）；semver 形态执法仍在归一化叶子 ValidateApp（railpack 钉版
		// 须等于平台钉版的比对在 build 侧，不在受理面）。
		s, err := spec.UploadDeploy(spec.UploadDeployInput{
			AppID: appRow.ID, Project: appRow.ProjectID, UploadID: uploadRow.ID,
			ProcessName: req.GetProcessName(), Builder: req.GetBuilder(),
			Dockerfile: req.GetDockerfile(), RailpackVersion: req.GetRailpackVersion(),
			OutputDir: req.GetOutputDir(), Probe: probe, Env: req.GetEnv(),
			Ports: ports,
		})
		if err != nil {
			return nil, grpcapi.MapValidationError(err)
		}
		return s, nil
	case req.GetSpecFile() != "":
		// 受理前置已解析并校验（AppRef 权威覆写 + ValidateApp 叶子执法
		// + Source.upload 归属/blob 检查在 Deploy）；此处直通——归一化
		// 语义 = "文件即期望状态"。
		return specFile, nil
	default:
		// descriptor at-least-one 规则拦截后的兜底（常态不可达——拦截器
		// 缺位时的纵深，文案与注解逐字节一致）。
		return nil, apperr.New("E_INVALID_ARGUMENT", "one of image, compose_yaml, upload_id or spec_file is required")
	}
}

// deployRequestPorts 组装 DeployRequest 的端口声明（F3.5）：直投与上传
// 形态的 Route-facing 面。形状执法（compose 互斥/protocol×port/端口域/
// 协议枚举）已退役 DeployRequest descriptor 注解（P4-2 批 2，拦截器受理
// 前拦截）；此处纯组装：protocol 缺省 http（Route 同词汇；descriptor
// 保证值域内，default 分支兜底 http），端口域与协议枚举的叶子级复核由
// ValidateProcess 承载（归一化产物必经）。
func deployRequestPorts(req *deliveryv1.DeployRequest) []*specv1.PortSpec {
	if req.GetPort() == 0 {
		return nil
	}
	var proto specv1.Protocol
	switch req.GetProtocol() {
	case "h2c":
		proto = specv1.Protocol_PROTOCOL_H2C
	case "tcp":
		proto = specv1.Protocol_PROTOCOL_TCP
	default:
		proto = specv1.Protocol_PROTOCOL_HTTP
	}
	return []*specv1.PortSpec{{Port: req.GetPort(), Protocol: proto}}
}

func (svc *DeploymentsService) GetDeployment(ctx context.Context, req *deliveryv1.GetDeploymentRequest) (*deliveryv1.GetDeploymentResponse, error) {
	if err := svc.deps.Auth.AuthorizeDeploymentID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	d, err := svc.deps.Deployments.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "deployment")
	}
	return &deliveryv1.GetDeploymentResponse{Deployment: deploymentMsg(*d)}, nil
}

func (svc *DeploymentsService) ListDeployments(ctx context.Context, req *deliveryv1.ListDeploymentsRequest) (*deliveryv1.ListDeploymentsResponse, error) {
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.deps.Deployments.ListByApp(ctx,
		req.GetAppId(), req.GetAfterDeploymentSeq(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "deployment")
	}
	out := &deliveryv1.ListDeploymentsResponse{}
	for _, d := range list {
		out.Deployments = append(out.Deployments, deploymentMsg(d))
	}
	return out, nil
}

func (svc *DeploymentsService) CancelDeployment(ctx context.Context, req *deliveryv1.CancelDeploymentRequest) (*deliveryv1.CancelDeploymentResponse, error) {
	if err := svc.deps.Auth.AuthorizeDeploymentID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	d, err := svc.deps.Deploy.Cancel(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "deployment")
	}
	return &deliveryv1.CancelDeploymentResponse{Deployment: deploymentMsg(*d)}, nil
}

func (svc *DeploymentsService) Rollback(ctx context.Context, req *deliveryv1.RollbackRequest) (*deliveryv1.RollbackResponse, error) {
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	// B1 单目标验票（设计稿 §1.2 重裁）：confirmation 透传 engine——
	// Rollback 走 Submit 路径，④digest/⑤base 与烧票复用 Deploy 的临界区
	// 闭合（④锚 = 目标 Revision 冻结体；语义位 supersede 由 engine 单源
	// 归一）。
	d, adm, err := svc.deps.Deploy.Rollback(ctx, req.GetAppId(), req.GetToRevision(), rollbackConfirmInput(req))
	if err != nil {
		// 哨兵判定（Q-13）：engine 哨兵 → E_NO_BASELINE（映射目标不变，
		// 只把文案 Contains 换成 errors.Is——文案再改不破坏错误契约）。
		// 文案覆盖缺省目标解析的两类诚实拒绝：无任何成功基线，或唯一
		// 成功基线即当前生效修订（缺省回滚跳过当前修订后无可回滚对象）。
		if errors.Is(err, api.ErrNoSuccessfulBaseline) {
			return nil, apperr.New("E_NO_BASELINE",
				"app %s has no successful baseline to roll back to (none exists, or the only one is the current revision)",
				req.GetAppId()).WithCause(err)
		}
		if cerr := mapConfirmError(err); cerr != nil {
			return nil, cerr
		}
		return nil, grpcapi.MapStateError(err, "rollback")
	}
	return &deliveryv1.RollbackResponse{Deployment: deploymentMsg(*d), Admission: grpcapi.AdmissionMsg(adm)}, nil
}

// rollbackConfirmInput 把 RollbackRequest 的 confirmation 铸为 engine 入参
// （nil = 未携带——拦截器已拦 REQUIRED）。
func rollbackConfirmInput(req *deliveryv1.RollbackRequest) *api.ConfirmInput {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	return &api.ConfirmInput{Ticket: conf.GetTicket(), Skip: conf.GetSkip()}
}

// PreviewDeploy / PreviewTeardown / ScaleProcess 的实装在 preview.go
// （B1 W2a 预览管线；PreviewDeploy = 段一 deployIntake + engine 段二/段三
// 只读孪生 + 铸票，设计稿 §1.3/§1.4）。

// RestartProcess 强制重排单个进程的载体（ADR-0069）：授权面与 Rollback
// 同款（app 域写面）；engine 哨兵 map 为精确错误码（仿 systemgrpc 组件
// 重启面——未部署与未知进程同归 E_NOT_FOUND，运行时缺子面是平台形态问题
// 归 E_INTERNAL）。
func (svc *DeploymentsService) RestartProcess(ctx context.Context, req *deliveryv1.RestartProcessRequest) (*deliveryv1.RestartProcessResponse, error) {
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	if err := svc.deps.Deploy.RestartProcess(ctx, req.GetAppId(), req.GetProcessName()); err != nil {
		switch {
		case errors.Is(err, api.ErrProcessUnknown):
			return nil, apperr.New("E_NOT_FOUND",
				"process %q not found in app %s (unknown name or never deployed)",
				req.GetProcessName(), req.GetAppId()).WithCause(err)
		case errors.Is(err, api.ErrRuntimeNoRestart):
			return nil, apperr.New("E_INTERNAL", "the runtime provider does not implement carrier restart").WithCause(err)
		}
		return nil, grpcapi.MapStateError(err, "app")
	}
	return &deliveryv1.RestartProcessResponse{}, nil
}

// ---- Revisions ----

func (svc *RevisionsService) ListRevisions(ctx context.Context, req *deliveryv1.ListRevisionsRequest) (*deliveryv1.ListRevisionsResponse, error) {
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.deps.Revisions.ListByApp(ctx,
		req.GetAppId(), req.GetAfterSeq(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "revision")
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
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	row, err := svc.deps.Revisions.GetBySeq(ctx, req.GetAppId(), req.GetSeq())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "revision")
	}
	spec := &specv1.AppSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(row.Spec, spec); err != nil {
		return nil, grpcapi.MapStateError(err, "revision spec")
	}
	return &deliveryv1.GetRevisionResponse{Revision: revisionMsg(*row), Spec: spec}, nil
}

// GetAppWorkloads 按 Revision 只读重放 Workload 投影（ADR-0071 投影层）。
// seq 缺省 = 当前生效（engine 侧 LatestSucceeded 解析）；渲染错误经
// mapStateError（ErrNotFound → E_NOT_FOUND，其余侧表解析失败如实上抛）。
func (svc *RevisionsService) GetAppWorkloads(ctx context.Context, req *deliveryv1.GetAppWorkloadsRequest) (*deliveryv1.GetAppWorkloadsResponse, error) {
	// app_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	row, ws, err := svc.deps.Deploy.RenderWorkloads(ctx, req.GetAppId(), req.GetSeq())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "workloads")
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
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	from, err := svc.deps.Revisions.GetBySeq(ctx, req.GetAppId(), req.GetFromSeq())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "revision")
	}
	to, err := svc.deps.Revisions.GetBySeq(ctx, req.GetAppId(), req.GetToSeq())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "revision")
	}
	entries, err := spec.Diff(from.Spec, to.Spec)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "revision diff")
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

func (svc *BuildsService) ListBuilds(ctx context.Context, req *deliveryv1.ListBuildsRequest) (*deliveryv1.ListBuildsResponse, error) {
	if err := svc.deps.Auth.AuthorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.deps.Builds.ListByApp(ctx,
		req.GetAppId(), req.GetAfterBuildSeq(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "build")
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
	if err := svc.deps.Auth.AuthorizeBuildID(ctx, req.GetBuildId()); err != nil {
		return err
	}
	var lastSeq int64 = -1
	newFrames := 0
	flush := func() error {
		frames, last := svc.deps.Registry.RecentBuildLogsAfter(req.GetBuildId(), lastSeq)
		for _, f := range frames {
			if err := stream.Send(&deliveryv1.StreamBuildLogsResponse{
				BuildId: req.GetBuildId(),
				Time:    f.Time.UTC().Format(grpcapi.TimeFormatRFC3339),
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
	if newFrames == 0 && svc.deps.logging() != nil {
		b, err := svc.deps.Builds.Get(ctx, req.GetBuildId())
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
		if b, err := svc.deps.Builds.Get(ctx, req.GetBuildId()); err == nil && b.State.Terminal() && newFrames == 0 {
			return nil
		}
	}
}

// backfillBuildLogs 从 VL 回读一个终态 build 的日志（域锚 = Build 行的
// 归属 App/Project + Team 实取）。
func (svc *BuildsService) backfillBuildLogs(ctx context.Context, b *build.Build, stream deliveryv1.BuildsService_StreamBuildLogsServer) error {
	appRow, err := svc.deps.Apps.Get(ctx, b.AppID)
	if err != nil {
		return grpcapi.MapStateError(err, "app")
	}
	proj, err := svc.deps.Projects.Get(ctx, appRow.ProjectID)
	if err != nil {
		return grpcapi.MapStateError(err, "project")
	}
	w := buildLogBackfillWriter{stream: stream, buildID: b.ID}
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: proj.TeamID, Project: appRow.ProjectID, App: appRow.ID, AppName: appRow.Name},
		Source:    b.ID,
	}
	if err := svc.deps.logging().Query(ctx, q, &w); err != nil {
		return grpcapi.MapStateError(err, "logs")
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
		Time:    f.Time.UTC().Format(grpcapi.TimeFormatRFC3339),
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
