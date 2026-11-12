package fleetlygrpc

import (
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	configrepo "github.com/fleetlyrun/fleetly/internal/domain/structure/config"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// mapStateError 是 grpcapi.MapStateError 的既名委托（共享 transport kernel）。
func mapStateError(err error, what string) error { return grpcapi.MapStateError(err, what) }

// mapValidationError 是 grpcapi.MapValidationError 的既名委托。
func mapValidationError(err error) error { return grpcapi.MapValidationError(err) }

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

func networkMsg(n network.Network) *structurev1.Network {
	return &structurev1.Network{Id: n.ID, ProjectId: n.ProjectID, Name: n.Name, EgressNone: n.EgressNone, CreatedAt: n.CreatedAt}
}

func deploymentMsg(d deployment.Deployment) *deliveryv1.Deployment {
	return &deliveryv1.Deployment{
		Id: d.ID, Seq: d.Seq, AppId: d.AppID, FromRevision: d.FromRevision, ToRevision: d.ToRevision,
		State: string(d.State), Generation: d.Generation, IdempotencyKey: d.IdempotencyKey,
		CommitSha: d.CommitSHA, SupersededBy: d.SupersededBy, Error: d.Error,
		ObserveDeadline: d.ObserveDeadline, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		FinishedAt: d.FinishedAt, PreDeployTaskId: api.PreDeployTaskID(&d),
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
		RestartSeq: w.RestartSeq,
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

// admissionMsg 是 grpcapi.AdmissionMsg 的既名委托。
func admissionMsg(a *api.Admission) *deliveryv1.Admission { return grpcapi.AdmissionMsg(a) }

func buildMsg(b build.Build) *deliveryv1.Build {
	return &deliveryv1.Build{
		Id: b.ID, Seq: b.Seq, AppId: b.AppID, RevisionId: b.RevisionID, State: string(b.State),
		Digest: b.Digest, Error: b.Error, CreatedAt: b.CreatedAt, FinishedAt: b.FinishedAt,
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

// validName 已删除（P4-1 批 1 Run B2）：最后三个调用点
//（CreateNotificationChannel/CreateTask/CreateSchedule 的 name）退役后
// 零调用——dns 标签形状执法单源住 descriptor 注解，字节一致由 entityid
// 包的 descriptor 对齐测试执法。
