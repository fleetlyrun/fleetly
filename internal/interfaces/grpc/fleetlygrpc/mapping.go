package fleetlygrpc

import (
	"errors"
	"net"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/appvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	configrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/config"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/revision"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/route"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
)

// mapStateError 把 repo/engine 哨兵与 capability 面（指标查询拒绝等）错误
// 映射为 apperr 信封（API 层唯一出口）。已是 *apperr.Error 的错误原样穿透
// （handler 在事务 fn 内组合的应用错误不二次包裹——二次包裹会把 E_CONFLICT
// 之类吞成 E_INTERNAL）。
func mapStateError(err error, what string) error {
	var qe *capability.MetricsQueryError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrNotFound):
		return apperr.New("E_NOT_FOUND", "%s not found", what).WithCause(err)
	case errors.Is(err, state.ErrAlreadyExists):
		// 唯一约束命中（批 0 复核回归：与 CAS/FK 冲突重新分立）——gRPC
		// AlreadyExists，REST 409；重名重试语义不静默退化成 400。
		return apperr.New("E_ALREADY_EXISTS", "%s already exists", what).WithCause(err)
	case errors.Is(err, state.ErrConflict):
		// 中性冲突文案（Q-12）：ErrConflict 承载 CAS 前置不符与 FK
		// RESTRICT（唯一约束命中已分流 E_ALREADY_EXISTS）——文案只说
		// "被并发改动或仍被引用"，具体原因留在 cause 链。
		return apperr.New("E_CONFLICT",
			"conflict: %s was changed concurrently or is still referenced; refresh and retry", what).WithCause(err)
	case errors.Is(err, engine.ErrQueueFull):
		return apperr.New("E_QUEUE_FULL", "deployment queue is full for this app").WithCause(err)
	case errors.Is(err, engine.ErrNotCancellable):
		return apperr.New("E_NOT_CANCELLABLE", "deployment already finished").WithCause(err)
	case errors.Is(err, engine.ErrTaskTerminal):
		// Task 域哨兵的防御 fallback（ADR-0068 拓扑注记）：Task 哨兵的唯一
		// owner 是 mapTaskVerbError（生命周期动词路径）；本函数的现役
		// "task" 域调用点全为 state 读（Get/List），这三分支不可达——
		// 新增 Task 哨兵不得落此，落 mapTaskVerbError。文案自持不委托
		// （委托回 mapTaskVerbError 会与其尾部 mapStateError 回退成环）。
		return apperr.New("E_CONFLICT", "task already reached a terminal state; create a new task instead").WithCause(err)
	case errors.Is(err, engine.ErrTaskNotRenewable):
		return apperr.New("E_INVALID_ARGUMENT", "one-shot tasks carry no owner lease; renew targets resident tasks").WithCause(err)
	case errors.Is(err, engine.ErrNotResident):
		return apperr.New("E_INVALID_ARGUMENT", "this verb targets resident tasks only").WithCause(err)
	case errors.Is(err, engine.ErrCrossProjectRefNotApproved):
		// 受理预检的 strict 拒绝（ADR-0013 附录 A.3）——可编程分支而非
		// E_INTERNAL（staging 真机实证哨兵漏映射，2026-10-02）。
		return apperr.New("E_INVALID_ARGUMENT", "%s (declare and approve the network peer, or remove the reference)", err.Error()).WithCause(err)
	case errors.Is(err, engine.ErrFirstBootNetworkUnknown):
		// firstBootJobs 裸网名受理预检（B12 P3-5）——同上可编程分支。
		return apperr.New("E_INVALID_ARGUMENT", "%s", err.Error()).WithCause(err)
	case errors.As(err, &qe):
		// 指标存储上游 4xx（capability.MetricsQueryError，F-B1）：查询
		// 语法/语义错属调用方错——透传上游原文落 E_INVALID_ARGUMENT，
		// 不吞成 E_INTERNAL 500（staging 实证：缺 group_left 的除法查询
		// 曾整链 500 且 cause 不落盘，误导排查方向）。
		return apperr.New("E_INVALID_ARGUMENT", "metrics query: %s", qe.Message).WithCause(err)
	default:
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return ae
		}
		return apperr.New("E_INTERNAL", "internal error: %s", err.Error()).WithCause(err)
	}
}

// mapValidationError 把 spec 校验错误映射为字段级 InvalidArgument。
func mapValidationError(err error) error {
	var verr *spec.ValidationError
	if errors.As(err, &verr) {
		return apperr.New("E_INVALID_ARGUMENT", "%s: %s", verr.Field, verr.Reason).WithCause(err)
	}
	return apperr.New("E_INVALID_ARGUMENT", "%s", err.Error()).WithCause(err)
}

// ---- 行 ↔ proto 映射（时间戳与状态原样：存储即契约） ----

func projectMsg(p *project.Project) *structurev1.Project {
	return &structurev1.Project{Id: p.ID, Seq: p.Seq, Name: p.Name, TeamId: p.TeamID, CreatedAt: p.CreatedAt, DeletedAt: p.DeletedAt}
}

func appMsg(a *app.App) *structurev1.App {
	return &structurev1.App{Id: a.ID, Seq: a.Seq, ProjectId: a.ProjectID, Name: a.Name, CreatedAt: a.CreatedAt}
}

func secretMsg(s secret.Secret) *structurev1.Secret {
	return &structurev1.Secret{Id: s.ID, ProjectId: s.ProjectID, Name: s.Name, Fingerprint: s.Fingerprint, UpdatedAt: s.UpdatedAt}
}

func configMsg(c configrepo.Config, withContent bool) *structurev1.Config {
	out := &structurev1.Config{Id: c.ID, ProjectId: c.ProjectID, Name: c.Name, Version: c.Version, CreatedAt: c.CreatedAt}
	if withContent {
		out.Content = string(c.Content)
	}
	return out
}

// sharedVariableMsg 值明文回显（非敏感契约，ADR-0043 决策 1——指纹形态
// 是 Secrets 的面，共享变量是 Configs 桶）。
func sharedVariableMsg(v sharedvariable.SharedVariable) *structurev1.SharedVariable {
	return &structurev1.SharedVariable{Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, Value: v.Value, UpdatedAt: v.UpdatedAt}
}

func appVariableMsg(v appvariable.AppVariable) *structurev1.AppVariable {
	return &structurev1.AppVariable{Id: v.ID, AppId: v.AppID, Name: v.Name, Value: v.Value, UpdatedAt: v.UpdatedAt}
}

func volumeMsg(v volume.Volume) *structurev1.Volume {
	return &structurev1.Volume{Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, PinnedNodeId: v.PinnedNodeID, CreatedAt: v.CreatedAt}
}

func networkMsg(n networkrepo.Network) *structurev1.Network {
	return &structurev1.Network{Id: n.ID, ProjectId: n.ProjectID, Name: n.Name, EgressNone: n.EgressNone, CreatedAt: n.CreatedAt}
}

func routeMsg(r route.Route) *proxyv1.Route {
	return &proxyv1.Route{
		Id: r.ID, Seq: r.Seq, ProjectId: r.ProjectID, Host: r.Host, Path: r.Path,
		AppId: r.AppID, Process: r.Process, Port: r.Port,
		Protocol: string(r.Protocol), TlsMode: r.TLSMode, CreatedAt: r.CreatedAt,
	}
}

func deploymentMsg(d deployment.Deployment) *deliveryv1.Deployment {
	return &deliveryv1.Deployment{
		Id: d.ID, Seq: d.Seq, AppId: d.AppID, FromRevision: d.FromRevision, ToRevision: d.ToRevision,
		State: string(d.State), Generation: d.Generation, IdempotencyKey: d.IdempotencyKey,
		CommitSha: d.CommitSHA, SupersededBy: d.SupersededBy, Error: d.Error,
		ObserveDeadline: d.ObserveDeadline, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		FinishedAt: d.FinishedAt, FirstBootTaskId: engine.FirstBootTaskID(&d),
		FromGeneration: d.FromGeneration,
	}
}

// revisionMsg 是 Revision 行投影（F3.1：process_strategies 从冻结 spec 派生
// ——行本就存 AppSpec protojson 规范序列化，解析零额外存储面；解析失败
// 静默退化为零值摘要（不可变行落库前已过 ValidateApp，坏行不该出现，
// 读面不因摘要缺位而整页 500）。
func revisionMsg(rev revision.Revision) *deliveryv1.Revision {
	msg := &deliveryv1.Revision{
		Id: rev.ID, AppId: rev.AppID, Seq: rev.Seq, Digest: rev.Digest, CreatedAt: rev.CreatedAt,
		SourceText: rev.SourceText,
	}
	var frozen specv1.AppSpec
	if err := protojson.Unmarshal(rev.Spec, &frozen); err == nil {
		for i := range frozen.Processes {
			p := frozen.Processes[i]
			msg.ProcessStrategies = append(msg.ProcessStrategies, &deliveryv1.ProcessStrategy{
				Process: p.GetName(), Strategy: p.GetStrategy(),
			})
		}
		msg.SourceKind = sourceKindOf(frozen.GetSource())
	}
	return msg
}

// sourceKindOf 从冻结体 source oneof 派生材料来源分类（ADR-0071 决策 6：
// 逐 Revision provenance，零迁移——列表/详情同一投影）。
func sourceKindOf(src *specv1.Source) deliveryv1.SourceKind {
	switch src.GetKind().(type) {
	case *specv1.Source_Git:
		return deliveryv1.SourceKind_SOURCE_KIND_GIT
	case *specv1.Source_Image:
		return deliveryv1.SourceKind_SOURCE_KIND_IMAGE
	case *specv1.Source_Upload:
		return deliveryv1.SourceKind_SOURCE_KIND_UPLOAD
	}
	return deliveryv1.SourceKind_SOURCE_KIND_UNSPECIFIED
}

// workloadMsg 是 Workload 视图的单载体投影（ADR-0071 投影层）：
// capability.Workload 用户面字段镜像——Generation（渲染非下发）、
// Materials（凭证永不进视图）与受管域专用声明不映射。
func workloadMsg(w capability.Workload) *deliveryv1.Workload {
	msg := &deliveryv1.Workload{
		Id: w.ID, Process: w.Process, Image: w.Image, Command: w.Command,
		Env: w.Env, Replicas: w.Replicas,
		Networks: w.Networks, EgressNetworks: w.EgressNetworks,
		Addressing: addressNames(w.Addressing),
		Restart:    string(w.Restart),
		StopGrace:  durationMsg(w.StopGrace),
	}
	for _, p := range w.Ports {
		msg.Ports = append(msg.Ports, &specv1.PortSpec{Port: p.Port, Protocol: protocolEnum(p.Protocol)})
	}
	if w.Healthcheck != nil {
		msg.Healthcheck = &deliveryv1.WorkloadHealthcheck{
			HttpPath: w.Healthcheck.HTTPPath, HttpPort: w.Healthcheck.HTTPPort,
			TcpPort: w.Healthcheck.TCPPort, Exec: w.Healthcheck.Exec,
			Interval:    durationMsg(w.Healthcheck.Interval),
			Timeout:     durationMsg(w.Healthcheck.Timeout),
			StartPeriod: durationMsg(w.Healthcheck.StartPeriod),
			Retries:     w.Healthcheck.Retries,
		}
	}
	if w.Resources != nil {
		msg.Resources = &specv1.ResourcesSpec{CpuMillis: w.Resources.CPUMillis, MemoryMb: w.Resources.MemoryMB}
	}
	msg.Placement = &specv1.PlacementSpec{NodeIds: w.Placement.NodeIDs}
	for _, v := range w.Volumes {
		msg.Volumes = append(msg.Volumes, &specv1.VolumeAttachment{
			VolumeId: v.VolumeID, Target: v.Target, ReadOnly: v.ReadOnly,
		})
	}
	return msg
}

// workloadObservationMsg 是在服观测投影（ADR-0071 观测层）。
func workloadObservationMsg(o capability.WorkloadObservation) *runtimev1.WorkloadObservation {
	return &runtimev1.WorkloadObservation{
		WorkloadId: o.WorkloadID, Generation: uint64(o.Generation),
		Image: o.Image, Command: o.Command, Replicas: o.Replicas,
		State: string(o.State), RolloutStalled: o.RolloutStalled, RolloutDetail: o.RolloutDetail,
	}
}

// protocolEnum 把 capability 协议串映射回 spec.v1 枚举（白名单外归
// UNSPECIFIED——未知协议只可能来自未来扩展，视图面零值诚实）。
func protocolEnum(p capability.Protocol) specv1.Protocol {
	switch p {
	case capability.ProtocolHTTP:
		return specv1.Protocol_PROTOCOL_HTTP
	case capability.ProtocolH2C:
		return specv1.Protocol_PROTOCOL_H2C
	case capability.ProtocolTCP:
		return specv1.Protocol_PROTOCOL_TCP
	}
	return specv1.Protocol_PROTOCOL_UNSPECIFIED
}

func addressNames(addrs []capability.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Name)
	}
	return out
}

// durationMsg 是 time.Duration → proto Duration 投影（零值 = nil——
// protojson 省略，视图面"缺省 = Provider 缺省"语义由零值承载）。
func durationMsg(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

// admissionMsg 是 Submit 判定附注的响应投影（P10；nil 安全——engine 旧
// 路径缺附注时投影零值面由调用方豁免）。
func admissionMsg(a *engine.Admission) *deliveryv1.Admission {
	if a == nil {
		return nil
	}
	return &deliveryv1.Admission{
		Outcome: string(a.Outcome),
		//nolint:gosec // 位次域 = 活跃部署数 + 1（排队容量上限内，远小于 int32）
		Position: int32(a.Position), ExistingDeployment: a.Existing,
	}
}

func buildMsg(b build.Build) *deliveryv1.Build {
	return &deliveryv1.Build{
		Id: b.ID, Seq: b.Seq, AppId: b.AppID, RevisionId: b.RevisionID, State: string(b.State),
		Digest: b.Digest, Error: b.Error, CreatedAt: b.CreatedAt, FinishedAt: b.FinishedAt,
	}
}

func nodeMsg(n node.Node) *runtimev1.Node {
	return &runtimev1.Node{
		PlatformId: n.PlatformID, Seq: n.Seq, CarrierId: n.CarrierID, Hostname: n.Hostname,
		Role: n.Role, Available: n.Available, FirstSeenAt: n.FirstSeenAt, LastSeenAt: n.LastSeenAt,
	}
}

// marshalSpec 是 AppSpec 的规范序列化（protojson；Revision 冻结体形态）。
// UseProtoNames 与 CLI --json / REST gateway 同策略（snake_case）——冻结体
// 经 DiffRevisions 的路径输出直接用户可见，三面不得割裂（N0 修复批 A4）。
// 注意：本函数决定 Revision digest 的内容寻址；改序列化形态 = 全部历史
// digest 失配（同内容会冻结新行而非复用）——只在可承受一次性数据迁移的
// 批次变更（N0 staging 数据一次性重生）。
func marshalSpec(m proto.Message) ([]byte, error) {
	return protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(m)
}

// validName 是有名实体 name 的受理校验（ADR-0070 决策 4）：dns 标签形态
// 是可读 id base 与派生名（namespace/DNS/载体名）安全的前提。
func validName(field, name string) error {
	if !entityid.ValidName(name) {
		return apperr.New("E_INVALID_ARGUMENT",
			"%s: must be a dns label (lowercase letters, digits, hyphens; start and end alphanumeric, e.g. \"web\" or \"nightly-backup\")", field)
	}
	return nil
}

// rejectPrivateHost 拒绝指向环回/私网/链路本地/未指定段的 URL host
// （P0-7.3，validateAPIBase 与 validateGitRepo 的单源 SSRF 防线）：两者都
// 以控制面身份向 URL host 发起出站请求——写权者可配 https://10.x 形态
// 让控制面探测内网且上游 message 原样回显。只判字面 IP 与 localhost 名
// （校验位不做 DNS 解析——解析即 TOCTOU，且受理面无解析依赖）；DNS 名
// 形态的内网指向不在此列。GHES 部署在内网是真实场景：显式豁免配置项
// 留待真需求，落地时必须同时作用于本 helper 的两处调用（单源不豁开）。
func rejectPrivateHost(field, host string) error {
	h := strings.ToLower(host)
	if h == "" {
		return apperr.New("E_INVALID_ARGUMENT", "%s: URL host must not be empty", field)
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return apperr.New("E_INVALID_ARGUMENT",
			"%s: must not point at localhost (got %q)", field, host)
	}
	ip := net.ParseIP(h)
	if ip == nil {
		// 纯数字/点字面（"2130706433"、"0177.0.0.1"）不是合法 IP 词法但
		// libc 解析器可能按十进制/八进制 IPv4 拨号——字面数字面一律拒。
		if !strings.ContainsAny(h, ":") && strings.Trim(h, "0123456789.") == "" {
			return apperr.New("E_INVALID_ARGUMENT",
				"%s: numeric host forms are not accepted (got %q); use a DNS name or a dotted IPv4 literal", field, host)
		}
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return apperr.New("E_INVALID_ARGUMENT",
			"%s: must not point at loopback, private, link-local or unspecified addresses (got %q)", field, host)
	}
	return nil
}
