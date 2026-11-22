// functions.go 是 FunctionsService 的 gRPC 受理面（ADR-0102 任务④；设计
// v3 §5/§10）：受理位（授权/配额/名字/活跃网络在场 fail-closed/叶子校验）
// → Commit 三段落账（行 + 冻结体 + 保留窗裁剪 + 事件/审计同生共死）→
// Kick 收敛环。幂等面（Create/Deploy）由 idem 拦截器按 EnforcedMethods
// 扣——handler 零配合；冻结体 protojson 与 App 侧同参（内容寻址前提）。
package deliverygrpc

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// 事件五枚（eventcode 只增——注册表真源 internal/domain/shared/model/
// eventcode/events.go；零状态类事件：status 是观测缓存非状态机，设计 §10）。
const (
	eventFunctionCreated        = "function.created"
	eventFunctionRevisionChange = "function.revision_changed"
	eventFunctionScaled         = "function.scaled"
	eventFunctionExposureChange = "function.exposure_changed"
	eventFunctionDeleted        = "function.deleted"
	eventFunctionBuildQueued    = "function.build_queued"
)

// 事件名导出面（schemareg 注册与跨包消费，datagrpc 同款）。
const (
	EventFunctionCreated        = eventFunctionCreated
	EventFunctionRevisionChange = eventFunctionRevisionChange
	EventFunctionScaled         = eventFunctionScaled
	EventFunctionExposureChange = eventFunctionExposureChange
	EventFunctionDeleted        = eventFunctionDeleted
	EventFunctionBuildQueued    = eventFunctionBuildQueued
)

// Events 是函数事件名的导出清单（消费方事件路由/文档引用单源）。
var Events = []string{
	EventFunctionCreated, EventFunctionRevisionChange, EventFunctionScaled,
	EventFunctionExposureChange, EventFunctionDeleted,
}

// FunctionsEngine 是 engine 面的函数窄接口（组合根注入 *engine.Engine：
// 门面委托在核，ADR-0088 消费面纪律）。
type FunctionsEngine interface {
	TeardownFunction(ctx context.Context, id string) error
	KickFunctions()
	// WakeFunction 唤醒窄面（P2-1a，设计 v2 §1.2）：条件 CAS + Kick。
	WakeFunction(ctx context.Context, id string) (bool, error)
	// ResolveFunctionBackendTarget 后端解析面（P2-1a §1.6）：函数载体
	// 端点（id 寻址——接口零 domain 类型面）。
	ResolveFunctionBackendTarget(ctx context.Context, functionID string) (string, bool, error)
}

// SystemFace 是 Proxy 发布即时触发面（engine.PublishRoutesNow 的窄投影）。
type SystemFace interface {
	PublishRoutesNow()
}

// FunctionsService 是 Functions 产品线的受理服务（创建即首部署：无空行）。
type FunctionsService struct {
	deliveryv1.UnimplementedFunctionsServiceServer
	deps Deps
}

// ---- 冻结体构建与归一 ----

// marshalFunctionSpec 是冻结体规范序列化单源（与 App 侧 deployspec 同参：
// EmitUnpopulated:false + UseProtoNames:true——参数漂移 = 内容寻址失配）。
func marshalFunctionSpec(s *specv1.FunctionSpec) ([]byte, error) {
	return protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(s)
}

// functionSpecFromRequest 把受理字段归一为 FunctionSpec（源两态：image 直投
// 主路径；upload 源走构建就绪门——P2-3，设计 v2 §3。upload 形态 = railpack
// 构建钉版必填——RailpackBuilder 必须钉版立法，bare semver 受理位执法；
// handler 进程 name 归一 "handler"、端口恰一、协议 UNSPECIFIED→http）。
func functionSpecFromRequest(fnID, projectID, image, uploadID, railpackVersion string, command []string, env map[string]string, secretRefs []string, port int32, proto specv1.Protocol, hc *specv1.HealthcheckSpec, res *specv1.ResourcesSpec) (*specv1.FunctionSpec, error) {
	if proto == specv1.Protocol_PROTOCOL_TCP {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"port_protocol: must be http or h2c (tcp is not a function trigger protocol)")
	}
	h := &specv1.ProcessSpec{
		Name: "handler", Command: command, Env: env, SecretRefs: secretRefs,
		Ports:       []*specv1.PortSpec{{Port: port, Protocol: proto}},
		Healthcheck: hc, Resources: res,
	}
	s := &specv1.FunctionSpec{
		SchemaVersion: 1,
		Function:      &specv1.FunctionRef{Id: fnID, Project: projectID},
		Handler:       h,
	}
	switch {
	case uploadID != "":
		if railpackVersion == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"railpack_version: must not be empty for upload builds (the railpack builder requires a pinned version to prevent plan drift)").
				WithSuggestion("Pass --railpack-version (e.g. 0.39.0).")
		}
		s.Source = &specv1.Source{Kind: &specv1.Source_Upload{Upload: &specv1.UploadSource{Id: uploadID}}}
		s.Build = &specv1.BuildSpec{
			Builder:  "railpack",
			Strategy: &specv1.BuildSpec_Railpack{Railpack: &specv1.RailpackBuilder{PinnedVersion: railpackVersion}},
		}
		h.ImageOrigin = &specv1.ProcessSpec_FromBuild{FromBuild: "handler"}
	default:
		s.Source = &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: image}}}
		h.ImageOrigin = &specv1.ProcessSpec_Image{Image: image}
	}
	return s, nil
}

// handlerPort 从冻结体读 handler 声明口（响应 URL 面；冻结体经受理归一，
// 恰一口是结构性不变量——防御缺省 0）。
func handlerPort(body []byte) int32 {
	s := &specv1.FunctionSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, s); err != nil {
		return 0
	}
	ports := s.GetHandler().GetPorts()
	if len(ports) == 0 {
		return 0
	}
	return ports[0].GetPort()
}

// functionMsg 是行 → 响应 message 的单源（URL 面：private 恒 fn-<id>:<port>；
// public 加 https://<host>——三律①：fn 名与 exposure 无关）。
func functionMsg(row function.Function, rev *function.Revision) *deliveryv1.Function {
	dns := "fn-" + lower(row.ID)
	port := int32(0)
	var digest string
	var seq int64
	if rev != nil {
		port = handlerPort(rev.Spec)
		digest = rev.Digest
		if len(digest) > 12 {
			digest = digest[:12]
		}
		seq = rev.Seq
	}
	// parked 显示映射（P2-1a，设计 v2 §1.7 裁决——主线/独立 B 分歧从主线）：
	// 缓存层维持四值法不动；响应层以行事实优先——replicas=0 渲染 parked
	// （缩零时残留的 running 观测被映射遮蔽；唤醒后恢复观测真值）。映射
	// 住本单源，CLI/Console 免改。
	status := row.Status
	if row.Replicas == 0 {
		status = "parked"
	}
	msg := &deliveryv1.Function{
		Id: row.ID, ProjectId: row.ProjectID, Name: row.Name,
		State: "active", Status: status,
		DnsName: dns, PrivateUrl: fmt.Sprintf("http://%s:%d", dns, port),
		Exposure: row.Exposure, Replicas: row.Replicas,
		MinReplicas: row.MinReplicas, MaxReplicas: row.MaxReplicas,
		IdleTimeoutSecs:    row.IdleTimeoutSecs,
		CurrentRevisionSeq: seq, CurrentRevisionDigest: digest,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.Exposure == "public" && row.PublicHost != "" {
		msg.PublicHost = row.PublicHost
		msg.PublicUrl = "https://" + row.PublicHost
	}
	return msg
}

func lower(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

// ---- 受理 checks（事务内；fail-closed） ----

// functionQuotaCheck 是函数配额（CheckFunctionQuota 单源；delta 是本次
// 副本增量——Create 携初值、Scale 携差值）。
func functionQuotaCheck(projectID string, delta int64, create bool) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		active, sum, err := st.Functions.StatsByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if err := api.CheckFunctionQuota(projectID, active, sum, delta, create); err != nil {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s reached its function quota (limit %d functions / %d replicas); delete functions or scale down first",
				projectID, api.MaxFunctionsPerProject, api.MaxFunctionReplicasPerProject).WithCause(err)
		}
		return nil
	}
}

// functionProjectHasNetwork 是活跃网络在场检查（零网项目的 private 函数
// 不可达——fail-closed 拒绝优于静默孤岛，ADR-0029 同款）。
func functionProjectHasNetwork(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		networks, err := st.Networks.ListByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if len(networks) == 0 {
			return apperr.New("E_CONFLICT",
				"project %s has no active network; a function is unreachable without one — create a network first", projectID)
		}
		return nil
	}
}

// functionActive 是行活跃复查（事务内时点一致性：受理段读到的行到落账时
// 仍须活跃）。
func functionActive(id string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		if _, err := st.Functions.Get(ctx, id); err != nil {
			return err
		}
		return nil
	}
}

// secretRefsExist 是 secret_refs 存在性检查（fail-closed 受理——缺失精确
// 拒，不做部署期迟到失败）。
func secretRefsExist(projectID string, refs []string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		for _, name := range refs {
			if _, err := st.Secrets.GetByName(ctx, projectID, name); err != nil {
				if errors.Is(err, state.ErrNotFound) {
					return apperr.New("E_NOT_FOUND", "secret %q not found in project %s", name, projectID)
				}
				return err
			}
		}
		return nil
	}
}

// freezeRevision 是冻结半边（内容寻址复用优先；新行落账同事务保留窗裁剪
// ——ADR-0101：Function 锚集 = current 指针行，protect 传入新锚）。
func freezeRevision(ctx context.Context, st transact.Stores, fnID string, spec *specv1.FunctionSpec) (*function.Revision, error) {
	body, err := marshalFunctionSpec(spec)
	if err != nil {
		return nil, err
	}
	digest := function.Digest(body)
	if existing, err := st.FunctionRevisions.FindByDigest(ctx, fnID, digest); err == nil {
		return existing, nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	seq, err := st.FunctionRevisions.NextSeq(ctx, fnID)
	if err != nil {
		return nil, err
	}
	rev := &function.Revision{FunctionID: fnID, Seq: seq, Spec: body}
	if err := st.FunctionRevisions.Create(ctx, rev); err != nil {
		return nil, err
	}
	if _, err := st.FunctionRevisions.Prune(ctx, fnID, []string{rev.ID}, api.RevisionRetentionPerApp); err != nil {
		return nil, err
	}
	return rev, nil
}

// authorizeFunction 是函数行级授权（行 → Project 轴，ADR-0035 沿 project 链）。
func (svc *FunctionsService) authorizeFunction(ctx context.Context, id string) (*function.Function, error) {
	row, err := svc.deps.Functions.Get(ctx, id)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return row, nil
}

// currentRevision 读在服冻结体（悬空 = 终局窗竞态，按 404 诚实）。
func (svc *FunctionsService) currentRevision(ctx context.Context, row *function.Function) (*function.Revision, error) {
	if row.CurrentRevisionID == "" {
		return nil, nil
	}
	return svc.deps.FunctionRevisions.Get(ctx, row.CurrentRevisionID)
}

// ---- 动词 ----

// CreateFunction 创建即首部署（幂等面拦截器扣；创建事务 = 行 + 首冻结体
// + 换轨 + 保留窗 + 事件/审计）。
func (svc *FunctionsService) CreateFunction(ctx context.Context, req *deliveryv1.CreateFunctionRequest) (*deliveryv1.CreateFunctionResponse, error) {
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	exposure := req.GetExposure()
	if exposure == "" {
		exposure = "private"
	}
	host := req.GetHost()
	if exposure == "public" && host == "" {
		// 服务端缺省层（P2-4）：base_domain 在场即拼装（同 SetExposure）。
		var derr error
		host, derr = svc.defaultPublicHost(ctx, req.GetName(), req.GetProjectId())
		if derr != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"host: must not be empty when exposure is public (a public function needs a host)").WithSuggestion("Pass --host (or configure functions.base_domain for managed-domain defaults).")
		}
	}
	// min 归一：缺省(0)→1；显式 0 = scale-to-zero 使能（P2-1a，设计 v2
	// §1.3——缺省与显式零同域但语义分立，受理位单点归一）。max 同。
	minR, maxR := req.GetMinReplicas(), req.GetMaxReplicas()
	if minR < 0 {
		minR = 1
	}
	if maxR <= 0 {
		maxR = 1
	}
	if minR > maxR {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"min_replicas %d must not exceed max_replicas %d", minR, maxR)
	}
	// 首副本 = max(1, min) 钳 max（min=0 的函数出生即常驻 1——缩零由决策
	// 环 idle 判定驱动，不在创建面）。
	firstReplicas := minR
	if firstReplicas < 1 {
		firstReplicas = 1
	}
	if firstReplicas > maxR {
		firstReplicas = maxR
	}

	// 行铸造（id/seq 由 repo 铸）；spec 冻结体在事务内以行 id 构建。
	row := &function.Function{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
		Replicas: firstReplicas, MinReplicas: minR, MaxReplicas: maxR,
		IdleTimeoutSecs: req.GetIdleTimeoutSeconds(),
		Exposure:        exposure, PublicHost: hostIfPublic(exposure, host),
		Status: "pending",
	}
	spec, serr := functionSpecFromRequest("", req.GetProjectId(), req.GetImage(), "", "",
		req.GetCommand(), req.GetEnv(), req.GetSecretRefs(), req.GetPort(), req.GetPortProtocol(), req.GetHealthcheck(), req.GetResources())
	if serr != nil {
		return nil, serr
	}

	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, req.GetProjectId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionCreated, Aggregate: "function", ID: row.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "function.create", Resource: "function/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}

	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{
			svc.deps.ParentProjectAlive(req.GetProjectId()),
			functionQuotaCheck(req.GetProjectId(), minR, true),
			functionProjectHasNetwork(req.GetProjectId()),
			secretRefsExist(req.GetProjectId(), req.GetSecretRefs()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Functions.Create(ctx, row); err != nil {
				return err
			}
			spec.Function = &specv1.FunctionRef{Id: row.ID, Project: row.ProjectID}
			rev, err := freezeRevision(ctx, st, row.ID, spec)
			if err != nil {
				return err
			}
			return st.Functions.SwapCurrent(ctx, row.ID, rev.ID, rev.Digest)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	svc.deps.FunctionsEngine.KickFunctions()

	// 行重读：Write 段的换轨在库上，受理局部变量不持结果（诚实响应面）。
	fresh, err := svc.deps.Functions.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	rev, rerr := svc.currentRevision(ctx, fresh)
	if rerr != nil {
		return nil, grpcapi.MapStateError(rerr, "function")
	}
	return &deliveryv1.CreateFunctionResponse{
		Function:  functionMsg(*fresh, rev),
		Admission: &deliveryv1.Admission{Outcome: "queued", Position: 1},
	}, nil
}

func revSeqOf(rev *function.Revision) int64 {
	if rev == nil {
		return 0
	}
	return rev.Seq
}

// defaultPublicHost 是 public host 的服务端缺省层（P2-4，设计 v2 §4）：
// `<name>-<project>.<base_domain>`——config functions.base_domain 在场才有
// 缺省层；未配置返回 E_INVALID_ARGUMENT（host 必填现状，建议文案指向
// --host 或 base_domain 配置）。
func (svc *FunctionsService) defaultPublicHost(ctx context.Context, name, projectID string) (string, error) {
	if svc.deps.FunctionsBaseDomain == "" {
		return "", apperr.New("E_INVALID_ARGUMENT",
			"host: must not be empty when exposure is public (no functions.base_domain is configured)").
			WithSuggestion("Pass --host explicitly, or configure functions.base_domain for managed-domain defaults.")
	}
	host := fmt.Sprintf("%s-%s.%s", lower(name), lower(projectID), svc.deps.FunctionsBaseDomain)
	return host, nil
}

func hostIfPublic(exposure, host string) string {
	if exposure == "public" {
		return host
	}
	return ""
}

// GetFunction 读单条（行级授权沿 project 链）。
func (svc *FunctionsService) GetFunction(ctx context.Context, req *deliveryv1.GetFunctionRequest) (*deliveryv1.GetFunctionResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	rev, rerr := svc.currentRevision(ctx, row)
	if rerr != nil {
		return nil, grpcapi.MapStateError(rerr, "function")
	}
	return &deliveryv1.GetFunctionResponse{Function: functionMsg(*row, rev)}, nil
}

// ListFunctions 按项目列活跃行（after_* + limit 游标）。
func (svc *FunctionsService) ListFunctions(ctx context.Context, req *deliveryv1.ListFunctionsRequest) (*deliveryv1.ListFunctionsResponse, error) {
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	rows, err := svc.deps.Functions.ListByProject(ctx, req.GetProjectId(),
		req.GetAfterFunctionSeq(), int(req.GetLimit()), req.GetExposure(), req.GetState())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	out := &deliveryv1.ListFunctionsResponse{}
	for i := range rows {
		rev, rerr := svc.currentRevision(ctx, &rows[i])
		if rerr != nil {
			return nil, grpcapi.MapStateError(rerr, "function")
		}
		out.Functions = append(out.Functions, functionMsg(rows[i], rev))
	}
	return out, nil
}

// DeployFunction 全量替换（源 + 形状；容量/接入不动——三平面分立）：
// 冻结新 Revision（内容寻址复用）+ 原子换轨 + revision_changed(cause=deploy)。
func (svc *FunctionsService) DeployFunction(ctx context.Context, req *deliveryv1.DeployFunctionRequest) (*deliveryv1.DeployFunctionResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, err
	}
	spec, serr := functionSpecFromRequest(row.ID, row.ProjectID, req.GetImage(), req.GetUploadId(), req.GetRailpackVersion(),
		req.GetCommand(), req.GetEnv(), req.GetSecretRefs(), req.GetPort(), req.GetPortProtocol(), req.GetHealthcheck(), req.GetResources())
	if serr != nil {
		return nil, serr
	}
	if req.GetUploadId() != "" {
		// upload 源走构建就绪门（P2-3，设计 v2 §3 机制三件①）：冻结
		// revision + 铸 queued Build + 落 pending 意图锚——不换轨；
		// 幂等重放（同 digest 已 pending）返回同一 pending 态。换轨由
		// function 环推进步承载（Build 终态 succeeded → 一事务
		// SwapCurrent + 清位 + revision_changed(cause=deploy)）。
		resp, derr := svc.deployPending(ctx, row, spec, req.GetUploadId())
		if derr != nil {
			return nil, derr
		}
		return resp, nil
	}
	wf, werr := svc.switchWriteFact(ctx, row, spec, req.GetSecretRefs(), "deploy")
	if werr != nil {
		return nil, werr
	}
	if err := svc.deps.Commit(ctx, wf); err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	svc.deps.FunctionsEngine.KickFunctions()
	fresh, err := svc.deps.Functions.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	rev, rerr := svc.currentRevision(ctx, fresh)
	if rerr != nil {
		return nil, grpcapi.MapStateError(rerr, "function")
	}
	return &deliveryv1.DeployFunctionResponse{
		Function:  functionMsg(*fresh, rev),
		Admission: &deliveryv1.Admission{Outcome: "queued", Position: 1},
	}, nil
}

// deployPending 是 upload 源的就绪门受理事务（P2-3）：冻结 revision +
// 铸 queued Build（owner=function）+ 落 pending 意图锚，一事务原子；
// E_FUNCTION_NOT_READY 呈现面（design v2 §3 预留码在此兑现——pending
// 期间调用面 421 语义由错误码信封承载）。
func (svc *FunctionsService) deployPending(ctx context.Context, row *function.Function, spec *specv1.FunctionSpec, uploadID string) (*deliveryv1.DeployFunctionResponse, error) {
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionBuildQueued, Aggregate: "function", ID: row.ID,
			Payload: grpcapi.MustJSON(map[string]string{"upload_id": uploadID}),
			TeamID:  team,
			Action:  "function.build_queued", Resource: "function/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{
			functionActive(row.ID),
			secretRefsExist(row.ProjectID, spec.GetHandler().GetSecretRefs()),
			uploadExists(row.ProjectID, uploadID),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			rev, err := freezeRevisionPending(ctx, st, row, spec)
			if err != nil {
				return err
			}
			// 幂等重放：同 revision 已 pending → 原样返回同一 pending 态
			//（Build 铸造留给环推进步的 D-4 重拾——不在此争并发）。
			if row.PendingRevisionID == rev.ID {
				return nil
			}
			if err := st.Functions.SetPending(ctx, row.ID, rev.ID); err != nil {
				return err
			}
			bseq, err := st.NextSeq(ctx, "builds")
			if err != nil {
				return err
			}
			queued := &build.Build{
				ID: entityid.Derive(row.ID, "fb", bseq), Seq: bseq,
				FunctionID: row.ID, OwnerKind: build.OwnerFunction,
				RevisionID: rev.ID, State: build.StateQueued,
				Repo: contract.BuildRepo(row.ProjectID, row.ID),
			}
			return st.Builds.Create(ctx, queued)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	svc.deps.FunctionsEngine.KickFunctions()
	fresh, err := svc.deps.Functions.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	pendingRev, err := svc.deps.FunctionRevisions.Get(ctx, fresh.PendingRevisionID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	return &deliveryv1.DeployFunctionResponse{
		Function:  functionMsg(*fresh, svc.currentRevisionOrPending(ctx, fresh, pendingRev)),
		Admission: &deliveryv1.Admission{Outcome: "queued", Position: 1},
	}, nil
}

// currentRevisionOrPending 是 pending 窗的响应 revision 投影（pending 行
// 在服 revision 未换轨——响应锚 pending 体让调用方见构建目标形状）。
func (svc *FunctionsService) currentRevisionOrPending(ctx context.Context, row *function.Function, pending *function.Revision) *function.Revision {
	if row.PendingRevisionID == "" || pending == nil {
		rev, err := svc.currentRevision(ctx, row)
		if err != nil {
			return nil
		}
		return rev
	}
	return pending
}

// uploadExists 是上传产物行存在性检查（fail-closed——不存在构建必悬挂）。
func uploadExists(projectID, uploadID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		if _, err := st.SourceUploads.Get(ctx, uploadID); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return apperr.New("E_INVALID_ARGUMENT",
					"upload_id %s does not exist in project %s; upload the source archive first", uploadID, projectID).
					WithSuggestion("Run `fleetly uploads create` (or the console upload flow) first.")
			}
			return err
		}
		return nil
	}
}

// freezeRevisionPending 是就绪门的冻结半边（freezeRevision 同构）：
// protect 集 = current 指针行 + pending 锚行（ADR-0101 保留窗——漏并 =
// pending 指 GC 悬空）。
func freezeRevisionPending(ctx context.Context, st transact.Stores, row *function.Function, spec *specv1.FunctionSpec) (*function.Revision, error) {
	body, err := marshalFunctionSpec(spec)
	if err != nil {
		return nil, err
	}
	digest := function.Digest(body)
	if existing, err := st.FunctionRevisions.FindByDigest(ctx, row.ID, digest); err == nil {
		return existing, nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	seq, err := st.FunctionRevisions.NextSeq(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	rev := &function.Revision{FunctionID: row.ID, Seq: seq, Spec: body}
	if err := st.FunctionRevisions.Create(ctx, rev); err != nil {
		return nil, err
	}
	protect := []string{rev.ID}
	if row.CurrentRevisionID != "" {
		protect = append(protect, row.CurrentRevisionID)
	}
	if _, err := st.FunctionRevisions.Prune(ctx, row.ID, protect, api.RevisionRetentionPerApp); err != nil {
		return nil, err
	}
	return rev, nil
}

// switchWriteFact 是换轨事务的声明段（Deploy 与 Rollback 同轨；cause 进
// 事件 payload 与审计 detail）——Commit 由调用方直发（acceptanceguard
// 按动词体见 Commit 原语）。
func (svc *FunctionsService) switchWriteFact(ctx context.Context, row *function.Function, spec *specv1.FunctionSpec, secretRefs []string, cause string) (grpcapi.WriteFact, error) {
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionRevisionChange, Aggregate: "function", ID: row.ID,
			Payload: grpcapi.MustJSON(map[string]string{"cause": cause}),
			TeamID:  team,
			Action:  "function." + cause, Resource: "function/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return grpcapi.WriteFact{}, err
	}
	return grpcapi.WriteFact{
		Checks: []grpcapi.Check{functionActive(row.ID), secretRefsExist(row.ProjectID, secretRefs)},
		Write: func(ctx context.Context, st transact.Stores) error {
			rev, err := freezeRevision(ctx, st, row.ID, spec)
			if err != nil {
				return err
			}
			return st.Functions.SwapCurrent(ctx, row.ID, rev.ID, rev.Digest)
		},
		Facts: []func() writes.FactRow{fact},
	}, nil
}

// ScaleFunction 容量平面：行 CAS，出 [min,max] 界即拒（受理位单点执法、
// 收敛环盲目，设计 §8）；配额按差值执法。
func (svc *FunctionsService) ScaleFunction(ctx context.Context, req *deliveryv1.ScaleFunctionRequest) (*deliveryv1.ScaleFunctionResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, err
	}
	// scale-to-zero 特化门先于通用窗口门（0 < min 命中通用文案会掩盖
	// 「需要 min=0 才能缩零」的行动指引）。
	if req.GetReplicas() == 0 && row.MinReplicas > 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"replicas 0 requires min_replicas 0 (scale-to-zero) for function %s; its window starts at %d",
			row.ID, row.MinReplicas).
			WithSuggestion("Recreate the function with --min-replicas 0 to enable scale-to-zero, or scale within the window.")
	}
	if req.GetReplicas() < row.MinReplicas || req.GetReplicas() > row.MaxReplicas {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"replicas %d is outside [%d, %d] for function %s",
			req.GetReplicas(), row.MinReplicas, row.MaxReplicas, row.ID).
			WithSuggestion("Adjust min_replicas/max_replicas first if you need a wider capacity window.")
	}
	delta := req.GetReplicas() - row.Replicas
	// idle_timeout 行旋钮同拍落（P2-2：ScaleFunction 是容量平面唯一写动词
	// ——replicas 与防抖窗一次行写，避免两动词两事务的中间态）；0 = 不改。
	idleTimeout := row.IdleTimeoutSecs
	if req.GetIdleTimeoutSeconds() > 0 {
		idleTimeout = req.GetIdleTimeoutSeconds()
	}
	fact, ferr := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionScaled, Aggregate: "function", ID: row.ID,
			Payload: grpcapi.MustJSON(map[string]int64{"replicas": req.GetReplicas(), "idle_timeout_secs": idleTimeout}),
			TeamID:  team,
			Action:  "function.scale", Resource: "function/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if ferr != nil {
		return nil, ferr
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{functionActive(row.ID), functionQuotaCheck(row.ProjectID, delta, false)},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Functions.SetCapacity(ctx, row.ID, req.GetReplicas()); err != nil {
				return err
			}
			if req.GetIdleTimeoutSeconds() > 0 {
				return st.Functions.SetIdleTimeout(ctx, row.ID, idleTimeout)
			}
			return nil
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	svc.deps.FunctionsEngine.KickFunctions()
	fresh, err := svc.deps.Functions.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	rev, rerr := svc.currentRevision(ctx, fresh)
	if rerr != nil {
		return nil, grpcapi.MapStateError(rerr, "function")
	}
	return &deliveryv1.ScaleFunctionResponse{Function: functionMsg(*fresh, rev)}, nil
}

// SetFunctionExposure 接入平面：行写 + 即时语义（public 需 host 且占用预检
// E_CONFLICT；转 private 清 host 位——三律②零载体动作）。
func (svc *FunctionsService) SetFunctionExposure(ctx context.Context, req *deliveryv1.SetFunctionExposureRequest) (*deliveryv1.SetFunctionExposureResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, err
	}
	exposure, host := req.GetExposure(), ""
	if exposure == "public" {
		host = req.GetHost()
		if host == "" {
			// 服务端缺省层（P2-4，设计 v2 §4）：config functions.base_domain
			// 在场时按 `<name>-<project>.<base_domain>` 拼装（占用预检同轨）；
			// 未配置 = host 必填现状（CLI sslip/连接域派生的回落语义）。
			var missing error
			host, missing = svc.defaultPublicHost(ctx, row.Name, row.ProjectID)
			if missing != nil {
				return nil, missing
			}
		}
	}
	fact, ferr := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionExposureChange, Aggregate: "function", ID: row.ID,
			Payload: grpcapi.MustJSON(map[string]string{"exposure": exposure}),
			TeamID:  team,
			Action:  "function.exposure", Resource: "function/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if ferr != nil {
		return nil, ferr
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{functionActive(row.ID), hostNotOccupied(row.ProjectID, row.ID, exposure, host)},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Functions.SetExposure(ctx, row.ID, exposure, host)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	svc.deps.FunctionsEngine.KickFunctions()
	svc.deps.System.PublishRoutesNow() // 接入平面即时重发布（§6 mandatory②）
	fresh, err := svc.deps.Functions.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	rev, rerr := svc.currentRevision(ctx, fresh)
	if rerr != nil {
		return nil, grpcapi.MapStateError(rerr, "function")
	}
	return &deliveryv1.SetFunctionExposureResponse{Function: functionMsg(*fresh, rev)}, nil
}

// hostNotOccupied 是 public host 占用预检（同项目他函数已占 = E_CONFLICT
// 快速反馈；引擎合成期 TOCTOU 窗口冲突 = 事件诚实报，设计 §5）。
func hostNotOccupied(projectID, selfID, exposure, host string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		if exposure != "public" || host == "" {
			return nil
		}
		rows, err := st.Functions.ListByProject(ctx, projectID, 0, 200, "public", "")
		if err != nil {
			return err
		}
		for i := range rows {
			if rows[i].ID != selfID && rows[i].PublicHost == host {
				return apperr.New("E_CONFLICT",
					"host %q is already served by function %s in this project", host, rows[i].ID)
			}
		}
		return nil
	}
}

// RollbackFunction 指回旧冻结 Revision（Replay——复用行不新增）：显式目标
// 须在保留窗内（窗外 NOT_FOUND + 保留策略 suggestion，ADR-0101 决策 7）；
// 缺省目标 = 在服冻结体之前的最近一枚。前置校验：目标引用的 secret_refs
// 仍存在（缺 = E_CONFLICT，CF 护栏先例，设计 v3 §5）。
func (svc *FunctionsService) RollbackFunction(ctx context.Context, req *deliveryv1.RollbackFunctionRequest) (*deliveryv1.RollbackFunctionResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, err
	}
	var target *function.Revision
	if req.GetRevisionSeq() > 0 {
		target, err = svc.deps.FunctionRevisions.GetBySeq(ctx, row.ID, req.GetRevisionSeq())
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, apperr.New("E_NOT_FOUND",
					"revision %d not found for function %s", req.GetRevisionSeq(), row.ID).WithCause(err).
					WithSuggestion("The target revision may have been removed by the retention policy (the most recent 100 revisions per function are kept). Redeploy the desired spec instead of rolling back.")
			}
			return nil, grpcapi.MapStateError(err, "function")
		}
	} else {
		current, rerr := svc.currentRevision(ctx, row)
		if rerr != nil {
			return nil, grpcapi.MapStateError(rerr, "function")
		}
		targets, lerr := svc.deps.FunctionRevisions.ListByFunction(ctx, row.ID, 0, 200)
		if lerr != nil {
			return nil, grpcapi.MapStateError(lerr, "function")
		}
		currentID := ""
		if current != nil {
			currentID = current.ID
		}
		found := false
		for i := len(targets) - 1; i >= 0; i-- {
			if targets[i].ID != currentID {
				target = &targets[i]
				found = true
				break
			}
		}
		if !found {
			return nil, apperr.New("E_NO_BASELINE",
				"function %s has no earlier revision to roll back to (the current one is the only frozen revision)", row.ID)
		}
	}
	// 回滚前置校验（设计 v3 §5）经 commitSwitch 的事务 checks 承载：
	// 目标引用的 secret_refs 仍在——缺即 E_NOT_FOUND 拒（部分回滚不做）。
	spec := &specv1.FunctionSpec{}
	if uerr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(target.Spec, spec); uerr != nil {
		return nil, grpcapi.MapStateError(uerr, "function")
	}
	wf, werr := svc.switchWriteFact(ctx, row, spec, spec.GetHandler().GetSecretRefs(), "rollback")
	if werr != nil {
		return nil, werr
	}
	if err := svc.deps.Commit(ctx, wf); err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	svc.deps.FunctionsEngine.KickFunctions()
	// Replay 语义：复用既有冻结行——commitSwitch 的 freezeRevision 按内容
	// 寻址天然命中既有行（零新增）。
	fresh, err := svc.deps.Functions.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	rev, rerr := svc.currentRevision(ctx, fresh)
	if rerr != nil {
		return nil, grpcapi.MapStateError(rerr, "function")
	}
	return &deliveryv1.RollbackFunctionResponse{Function: functionMsg(*fresh, rev)}, nil
}

// DeleteFunction 收口（幂等 tombstone）：先拆载体（engine Teardown——
// ADR-0023 序语义：先停流量面后落账），再一事务落 tombstone + function_
// revisions 终局全清（ADR-0101 决策 4）+ 事件/审计。
func (svc *FunctionsService) DeleteFunction(ctx context.Context, req *deliveryv1.DeleteFunctionRequest) (*deliveryv1.DeleteFunctionResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := svc.deps.FunctionsEngine.TeardownFunction(ctx, row.ID); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "function %s not found", req.GetId())
		}
		return nil, apperr.New("E_INTERNAL", "function teardown failed").WithCause(err)
	}
	fact, ferr := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionDeleted, Aggregate: "function", ID: row.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "function.delete", Resource: "function/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if ferr != nil {
		return nil, ferr
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{functionActive(row.ID)},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Functions.SoftDelete(ctx, row.ID); err != nil {
				return err
			}
			// 聚合终局即时全清（ADR-0101 决策 4——审计链归 deployment 无，
			// 函数域归事件/审计行）。
			return st.FunctionRevisions.DeleteAllByFunction(ctx, row.ID)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	if row.Exposure == "public" {
		svc.deps.System.PublishRoutesNow() // 撤流即时（§6 mandatory②；public 行的合成路由随删即逝）
	}
	return &deliveryv1.DeleteFunctionResponse{}, nil
}

// ListFunctionRevisions 列冻结体（保留窗内，ADR-0101）。
func (svc *FunctionsService) ListFunctionRevisions(ctx context.Context, req *deliveryv1.ListFunctionRevisionsRequest) (*deliveryv1.ListFunctionRevisionsResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, err
	}
	revs, err := svc.deps.FunctionRevisions.ListByFunction(ctx, row.ID, req.GetAfterRevisionSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	out := &deliveryv1.ListFunctionRevisionsResponse{}
	for _, rev := range revs {
		digest := rev.Digest
		if len(digest) > 12 {
			digest = digest[:12]
		}
		out.Revisions = append(out.Revisions, &deliveryv1.FunctionRevision{
			Seq: rev.Seq, Digest: digest, CreatedAt: rev.CreatedAt,
		})
	}
	return out, nil
}

// WakeFunction 是唤醒组件的内部窄动词（P2-1a，设计 v2 §1.2）：条件 CAS +
// Kick——见 engine.WakeFunction 门面。响应 woken=false = 已醒 no-op。
func (svc *FunctionsService) WakeFunction(ctx context.Context, req *deliveryv1.WakeFunctionRequest) (*deliveryv1.WakeFunctionResponse, error) {
	// 行级授权（唤醒面沿函数行 → project 链；activator 服务 Token 与
	// 用户 Token 同一授权面——窄 scope 纪律）。
	if _, err := svc.authorizeFunction(ctx, req.GetFunctionId()); err != nil {
		return nil, err
	}
	woken, err := svc.deps.FunctionsEngine.WakeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "function")
	}
	return &deliveryv1.WakeFunctionResponse{Woken: woken}, nil
}

// ResolveFunctionBackend 是唤醒组件的后端解析面（P2-1a）：ready=true =
// 函数载体端点；ready=false = parked（唤醒扣留面）。载具名经平台解析——
// 调用方永不感知 Provider 私有命名。
func (svc *FunctionsService) ResolveFunctionBackend(ctx context.Context, req *deliveryv1.ResolveFunctionBackendRequest) (*deliveryv1.ResolveFunctionBackendResponse, error) {
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return nil, err
	}
	// 就绪判定与地址解析经引擎单源（观测期望集 + Runtime.Addresses）：
	// parked（replicas=0）= 唤醒面；否则函数载体端点。唤醒组件本体投影
	// （P2-1a 第三刀）落地前，parked 分支诚实返回未就绪（唤醒面停用态）。
	if row.Replicas == 0 {
		return &deliveryv1.ResolveFunctionBackendResponse{Ready: false}, nil
	}
	addr, ready, aerr := svc.deps.FunctionsEngine.ResolveFunctionBackendTarget(ctx, row.ID)
	if aerr != nil {
		return nil, grpcapi.MapStateError(aerr, "function")
	}
	return &deliveryv1.ResolveFunctionBackendResponse{Ready: ready, Addr: addr}, nil
}

// WaitFunction 是收敛等待流：事件订阅为信号、行重读为真源（WaitOnAggregate
// 形态）；expect_revision_seq 锚定换轨目标（0 = 任意稳定态帧）。
func (svc *FunctionsService) WaitFunction(req *deliveryv1.WaitFunctionRequest, stream deliveryv1.FunctionsService_WaitFunctionServer) error {
	ctx := stream.Context()
	row, err := svc.authorizeFunction(ctx, req.GetFunctionId())
	if err != nil {
		return err
	}
	expect := req.GetExpectRevisionSeq()
	// send 是首帧/重读帧/终态判定的单源闭包（WaitOnAggregate 形态）：
	// 行重读为真源，稳定态帧（status 离开 pending 且换轨锚满足）即收流。
	lastState := ""
	send := func() error {
		row, err := svc.deps.Functions.Get(ctx, req.GetFunctionId())
		if err != nil {
			return grpcapi.MapStateError(err, "function")
		}
		rev, rerr := svc.currentRevision(ctx, row)
		if rerr != nil {
			return grpcapi.MapStateError(rerr, "function")
		}
		// 帧去重：状态键（status#revision）未变不发（ticker 唤醒是幂等
		// 信号——行重读为真源，重复帧只占流带宽）。
		state := fmt.Sprintf("%s#%d", row.Status, revSeqOf(rev))
		if state == lastState {
			return nil
		}
		lastState = state
		if err := stream.Send(&deliveryv1.WaitFunctionResponse{Function: functionMsg(*row, rev)}); err != nil {
			return err
		}
		// pending 锚呈现（P2-3 就绪门）：upload 构建未兑现时行面 status 已
		// 离开 pending（载体仍是旧 revision）——按锚非空返回 E_FUNCTION_
		// NOT_READY（421 信封语义——设计 v2 §3 预留码在此兑现；调用方轮询
		// 到锚清位即正常返回）。
		if row.PendingRevisionID != "" &&
			(expect == 0 || (rev != nil && rev.Seq == expect)) {
			return apperr.New("E_FUNCTION_NOT_READY",
				"function %s is building its pending revision; the current revision keeps serving", row.ID).
				WithSuggestion("Poll `fleetly functions get` until pending_revision_digest clears, then wait again.")
		}
		stable := row.Status != "pending" &&
			(expect == 0 || (rev != nil && rev.Seq == expect))
		if stable {
			return grpcapi.ErrWaitDone
		}
		return nil
	}
	return grpcapi.WaitOnAggregate(ctx, svc.deps.Wait, "function", row.ID, send)
}
