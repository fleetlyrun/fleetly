package fleetlygrpc

import (
	"errors"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// mapStateError 把 repo/engine 哨兵映射为 apperr 信封（API 层唯一出口）。
func mapStateError(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrNotFound):
		return apperr.New("E_NOT_FOUND", "%s not found", what).WithCause(err)
	case errors.Is(err, state.ErrConflict):
		return apperr.New("E_ALREADY_EXISTS", "%s already exists", what).WithCause(err)
	case errors.Is(err, engine.ErrQueueFull):
		return apperr.New("E_QUEUE_FULL", "deployment queue is full for this app").WithCause(err)
	case errors.Is(err, engine.ErrNotCancellable):
		return apperr.New("E_NOT_CANCELLABLE", "deployment already finished").WithCause(err)
	default:
		return apperr.New("E_INTERNAL", "internal error").WithCause(err)
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
	return &structurev1.Project{Id: p.ID, Name: p.Name, TeamId: p.TeamID, CreatedAt: p.CreatedAt, DeletedAt: p.DeletedAt}
}

func appMsg(a *app.App) *structurev1.App {
	return &structurev1.App{Id: a.ID, ProjectId: a.ProjectID, Name: a.Name, CreatedAt: a.CreatedAt}
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

func volumeMsg(v volume.Volume) *structurev1.Volume {
	return &structurev1.Volume{Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, PinnedNodeId: v.PinnedNodeID, CreatedAt: v.CreatedAt}
}

func networkMsg(n networkrepo.Network) *structurev1.Network {
	return &structurev1.Network{Id: n.ID, ProjectId: n.ProjectID, Name: n.Name, EgressNone: n.EgressNone, CreatedAt: n.CreatedAt}
}

func routeMsg(r route.Route) *edgev1.Route {
	return &edgev1.Route{
		Id: r.ID, ProjectId: r.ProjectID, Host: r.Host, Path: r.Path,
		AppId: r.AppID, Process: r.Process, Port: r.Port,
		Protocol: string(r.Protocol), TlsMode: r.TLSMode, CreatedAt: r.CreatedAt,
	}
}

func deploymentMsg(d deployment.Deployment) *deliveryv1.Deployment {
	return &deliveryv1.Deployment{
		Id: d.ID, AppId: d.AppID, FromRevision: d.FromRevision, ToRevision: d.ToRevision,
		State: string(d.State), Generation: d.Generation, IdempotencyKey: d.IdempotencyKey,
		CommitSha: d.CommitSHA, SupersededBy: d.SupersededBy, Error: d.Error,
		ObserveDeadline: d.ObserveDeadline, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		FinishedAt: d.FinishedAt,
	}
}

func buildMsg(b build.Build) *deliveryv1.Build {
	return &deliveryv1.Build{
		Id: b.ID, AppId: b.AppID, RevisionId: b.RevisionID, State: string(b.State),
		Digest: b.Digest, Error: b.Error, CreatedAt: b.CreatedAt, FinishedAt: b.FinishedAt,
	}
}

func nodeMsg(n node.Node) *runtimev1.Node {
	return &runtimev1.Node{
		PlatformId: n.PlatformID, CarrierId: n.CarrierID, Hostname: n.Hostname,
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

// newID 生成 ULID。
func newID() string { return ulid.Make().String() }
