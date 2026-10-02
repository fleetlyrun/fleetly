package governance

// Change Freeze 执法拦截器（ADR-0017 附录 A.3，F1.9）：命中活跃冻结
// （team_id ∈ {'', 资源所属 Team}）的变更型动词拒绝 E_CHANGE_FROZEN（信封
// 带冻结 reason 与 freeze id——Agent 可读出"为何冻结"）。判定轴是资源
// 所属 Team（ADR-0028 Team 轴：调用方所属 Team 不参与——跨 Team 管理
// Token 不得借道自家 Team 绕过目标 Team 的冻结）。
//
// 拦截器位于 authn 之后、幂等执法器之前：冻结拒绝不占幂等记录。Team 解析
// 自请求字段逐级回行（project_id 直取；app/task/schedule/network/peer/
// route/hook token 回行）；行不存在等解析失败放行——受理位自会给出诚实
// 拒绝（冻结不是 404 的掩蔽面）。冻结进出与在途写的毫秒级竞态接受为良性
// （tsuru Block 同款）。冻结面反扫守卫（internal/guards）以 proto 全方法
// 分类把守：新 RPC 必须显式进冻结表或豁免表（带理由）或落读面前缀。

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/freeze"
	"github.com/fleetlyrun/fleetly/internal/state/hook"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// scopeKind 是寻址字段到所属 Team 的解析链。
type scopeKind int

const (
	scopeTeam      scopeKind = iota // 字段值即 team_id（CreateProject）
	scopeProject                    // project_id → Project 行
	scopeApp                        // app_id → App → Project
	scopeTask                       // task id → Task → Project
	scopeSchedule                   // schedule id → Schedule → Project
	scopeNetwork                    // network_id → Network → Project
	scopePeer                       // peer id → Peer → Network → Project
	scopeRoute                      // route id → Route → Project
	scopeHookToken                  // hook token 明文 → hash → Hook → App → Project
	scopeDatabase                   // database id → Database → Project（ADR-0029）
)

// freezeScope 声明一个冻结面动词的 Team 解析方式（field 是请求字段名）。
type freezeScope struct {
	field string
	kind  scopeKind
}

// FrozenVerbs 是封禁面（ADR-0017 附录 A.3：Workload 与结构变更族；豁免面
// 与读面由 freezeguard 反扫守卫对账——本表是冻结执法唯一清单，导出供
// 守卫双向对账）。
var FrozenVerbs = map[string]freezeScope{
	// structure：全部变更动词。
	"/fleetly.structure.v1.ProjectsService/CreateProject":      {field: "team_id", kind: scopeTeam},
	"/fleetly.structure.v1.ProjectsService/DeleteProject":      {field: "id", kind: scopeProject},
	"/fleetly.structure.v1.AppsService/CreateApp":              {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.AppsService/DeleteApp":              {field: "id", kind: scopeApp},
	"/fleetly.structure.v1.SecretsService/PutSecret":           {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.SecretsService/DeleteSecret":        {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.ConfigsService/PutConfig":           {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.VolumesService/CreateVolume":        {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.NetworksService/CreateNetwork":      {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.NetworksService/DeclareNetworkPeer": {field: "network_id", kind: scopeNetwork},
	"/fleetly.structure.v1.NetworksService/ApproveNetworkPeer": {field: "id", kind: scopePeer},
	"/fleetly.structure.v1.NetworksService/RevokeNetworkPeer":  {field: "id", kind: scopePeer},
	"/fleetly.structure.v1.DatabasesService/CreateDatabase":    {field: "project_id", kind: scopeProject},
	"/fleetly.structure.v1.DatabasesService/DeleteDatabase":    {field: "id", kind: scopeDatabase},
	"/fleetly.delivery.v1.DeploymentsService/Deploy":           {field: "app_id", kind: scopeApp},
	"/fleetly.delivery.v1.DeploymentsService/Rollback":         {field: "app_id", kind: scopeApp},
	"/fleetly.delivery.v1.HooksService/SetGitHook":             {field: "app_id", kind: scopeApp},
	"/fleetly.delivery.v1.HooksService/RotateHookToken":        {field: "app_id", kind: scopeApp},
	"/fleetly.delivery.v1.HooksService/ReceiveWebhook":         {field: "token", kind: scopeHookToken},
	"/fleetly.automation.v1.TasksService/CreateTask":           {field: "project_id", kind: scopeProject},
	"/fleetly.automation.v1.TasksService/ScaleTask":            {field: "id", kind: scopeTask},
	"/fleetly.automation.v1.TasksService/DeleteTask":           {field: "id", kind: scopeTask},
	"/fleetly.automation.v1.SchedulesService/CreateSchedule":   {field: "project_id", kind: scopeProject},
	"/fleetly.automation.v1.SchedulesService/DeleteSchedule":   {field: "id", kind: scopeSchedule},
	"/fleetly.automation.v1.SchedulesService/TriggerSchedule":  {field: "id", kind: scopeSchedule},
	"/fleetly.edge.v1.RoutesService/CreateRoute":               {field: "project_id", kind: scopeProject},
	"/fleetly.edge.v1.RoutesService/DeleteRoute":               {field: "id", kind: scopeRoute},
	// 上传产物接入（F1.10，ADR-0019 附录 A.2）：client-streaming 写面——
	// Team 锚在首帧 meta.project_id（流式拦截器首帧 RecvMsg 执法；点路径
	// 读嵌套字段）。
	"/fleetly.delivery.v1.BuildsService/UploadSource": {field: "meta.project_id", kind: scopeProject},
}

// FreezeGuard 是冻结执法器（拦截器一份实现覆盖全部封禁面动词）。
type FreezeGuard struct {
	db      *state.DB
	log     *slog.Logger
	freezes *freeze.Repo

	projects  *project.Repo
	apps      *app.Repo
	tasks     *task.Repo
	schedules *schedule.Repo
	networks  *networkrepo.Repo
	peers     *networkpeer.Repo
	hooks     *hook.Repo
	routes    *route.Repo
	databases *dbrepo.Repo
}

// NewFreezeGuard 构造执法器（repo 族从 DB 时钟派生）。
func NewFreezeGuard(db *state.DB, log *slog.Logger) *FreezeGuard {
	clock := db.Clock()
	return &FreezeGuard{
		db: db, log: log,
		freezes:   freeze.New(clock),
		projects:  project.New(clock),
		apps:      app.New(clock),
		tasks:     task.New(clock),
		schedules: schedule.New(clock),
		networks:  networkrepo.New(clock),
		peers:     networkpeer.New(clock),
		hooks:     hook.New(clock),
		routes:    route.New(clock),
		databases: dbrepo.New(clock),
	}
}

// Unary 返回拦截器：非封禁面原样放行。
func (g *FreezeGuard) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		scope, ok := FrozenVerbs[info.FullMethod]
		if !ok {
			return handler(ctx, req)
		}
		if err := g.enforce(ctx, req, scope); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// enforce 执行一次冻结判定（unary 请求与流式首帧共用）：寻址失败放行交由
// 受理位拒绝；命中活跃冻结 → E_CHANGE_FROZEN（信封带冻结 reason 与 id）。
func (g *FreezeGuard) enforce(ctx context.Context, req any, scope freezeScope) error {
	team, resolved, err := g.resolveTeam(ctx, req, scope)
	if err != nil {
		return err
	}
	if !resolved {
		return nil // 寻址失败放行：受理位给出诚实拒绝
	}
	rows, err := g.freezes.ActiveForTeam(ctx, g.db.Runner(), team)
	if err != nil {
		return apperr.New("E_INTERNAL", "change freeze lookup failed").WithCause(err)
	}
	if len(rows) == 0 {
		return nil
	}
	f := rows[0]
	return apperr.New("E_CHANGE_FROZEN",
		"changes are frozen for team %s: %s", team, f.Reason).
		WithContext("freeze_id", f.ID).
		WithContext("team_id", f.TeamID).
		WithSuggestion("This change freeze is lifted by an operator ('fleetly freeze lift " + f.ID + "'); read-only and stop verbs stay available during the freeze.")
}

// resolveTeam 解析请求的治理 Team。resolved=false 表示寻址失败（空字段或
// 行不存在）——放行交由受理位拒绝；存储故障 fail-closed（E_INTERNAL）。
func (g *FreezeGuard) resolveTeam(ctx context.Context, req any, scope freezeScope) (string, bool, error) {
	ref := reqField(req, scope.field)
	if ref == "" {
		return "", false, nil
	}
	run := g.db.Runner()
	switch scope.kind {
	case scopeTeam:
		return ref, true, nil
	case scopeProject:
		return g.teamOfProject(ctx, run, ref)
	case scopeApp:
		a, err := g.apps.Get(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, a.ProjectID)
	case scopeTask:
		tk, err := g.tasks.Get(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, tk.ProjectID)
	case scopeSchedule:
		s, err := g.schedules.Get(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, s.ProjectID)
	case scopeNetwork:
		n, err := g.networks.GetByID(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, n.ProjectID)
	case scopePeer:
		p, err := g.peers.Get(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		n, err := g.networks.GetByID(ctx, run, p.NetworkID)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, n.ProjectID)
	case scopeRoute:
		rt, err := g.routes.Get(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, rt.ProjectID)
	case scopeHookToken:
		if identity.TokenKind(ref) != "hook" {
			return "", false, nil // 非法凭证：接收面自会 401
		}
		h, err := g.hooks.GetByTokenSHA256(ctx, run, identity.HashToken(ref))
		if err != nil {
			return resolutionError(err)
		}
		a, err := g.apps.Get(ctx, run, h.AppID)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, a.ProjectID)
	case scopeDatabase:
		d, err := g.databases.Get(ctx, run, ref)
		if err != nil {
			return resolutionError(err)
		}
		return g.teamOfProject(ctx, run, d.ProjectID)
	default:
		return "", false, nil // 不可达：表构造面自约束
	}
}

// teamOfProject 回行到 Project 的 Team；行不存在（含 tombstone）按寻址
// 失败放行。
func (g *FreezeGuard) teamOfProject(ctx context.Context, run state.Runner, projectID string) (string, bool, error) {
	if projectID == "" {
		return "", false, nil
	}
	p, err := g.projects.Get(ctx, run, projectID)
	if err != nil {
		return resolutionError(err)
	}
	if p.TeamID == "" {
		return "", false, nil // Team 轴外的行（不可达，ADR-0028 后必有 Team）
	}
	return p.TeamID, true, nil
}

// resolutionError 分辨解析失败：NotFound 放行（受理位拒绝），存储故障
// fail-closed。
func resolutionError(err error) (string, bool, error) {
	if errors.Is(err, state.ErrNotFound) {
		return "", false, nil
	}
	return "", false, apperr.New("E_INTERNAL", "change freeze team resolution failed").WithCause(err)
}

// reqField 经 protoreflect 读请求的 string 字段（寻址锚；点路径形态
// "meta.project_id" 支持嵌套消息——流式首帧的 Team 锚在元信息帧内）。
func reqField(req any, name string) string {
	m, ok := req.(proto.Message)
	if !ok {
		return ""
	}
	v := m.ProtoReflect()
	parts := strings.Split(name, ".")
	for i, part := range parts {
		fd := v.Descriptor().Fields().ByName(protoreflect.Name(part))
		if fd == nil {
			return ""
		}
		if i == len(parts)-1 {
			if fd.Kind() != protoreflect.StringKind {
				return ""
			}
			return v.Get(fd).String()
		}
		if fd.Kind() != protoreflect.MessageKind {
			return ""
		}
		v = v.Get(fd).Message()
	}
	return ""
}
