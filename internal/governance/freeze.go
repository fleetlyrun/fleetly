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

	"github.com/fleetlyrun/fleetly/internal/anchor"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/freeze"
)

// freezeScope 声明一个冻结面动词的 Team 解析方式（field 是请求字段名；
// kind 走 anchor 解析图——归属链单源，架构评审第二轮候选 3）。
type freezeScope struct {
	field string
	kind  anchor.Kind
}

// FrozenVerbs 是封禁面（ADR-0017 附录 A.3：Workload 与结构变更族；豁免面
// 与读面由 freezeguard 反扫守卫对账——本表是冻结执法唯一清单，导出供
// 守卫双向对账）。
var FrozenVerbs = map[string]freezeScope{
	// structure：全部变更动词。
	"/fleetly.structure.v1.ProjectsService/CreateProject":               {field: "team_id", kind: anchor.KindTeam},
	"/fleetly.structure.v1.ProjectsService/DeleteProject":               {field: "id", kind: anchor.KindProject},
	"/fleetly.structure.v1.AppsService/CreateApp":                       {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.AppsService/DeleteApp":                       {field: "id", kind: anchor.KindApp},
	"/fleetly.structure.v1.SecretsService/PutSecret":                    {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.SecretsService/DeleteSecret":                 {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.ConfigsService/PutConfig":                    {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.SharedVariablesService/PutSharedVariable":    {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.SharedVariablesService/DeleteSharedVariable": {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.VolumesService/CreateVolume":                 {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.NetworksService/CreateNetwork":               {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.NetworksService/DeclareNetworkPeer":          {field: "network_id", kind: anchor.KindNetwork},
	"/fleetly.structure.v1.NetworksService/ApproveNetworkPeer":          {field: "id", kind: anchor.KindPeer},
	"/fleetly.structure.v1.NetworksService/RevokeNetworkPeer":           {field: "id", kind: anchor.KindPeer},
	"/fleetly.structure.v1.DatabasesService/CreateDatabase":             {field: "project_id", kind: anchor.KindProject},
	"/fleetly.structure.v1.DatabasesService/DeleteDatabase":             {field: "id", kind: anchor.KindDatabase},
	"/fleetly.delivery.v1.DeploymentsService/Deploy":                    {field: "app_id", kind: anchor.KindApp},
	"/fleetly.delivery.v1.DeploymentsService/Rollback":                  {field: "app_id", kind: anchor.KindApp},
	"/fleetly.delivery.v1.HooksService/SetGitHook":                      {field: "app_id", kind: anchor.KindApp},
	"/fleetly.delivery.v1.HooksService/RotateHookToken":                 {field: "app_id", kind: anchor.KindApp},
	"/fleetly.delivery.v1.HooksService/ReceiveWebhook":                  {field: "token", kind: anchor.KindHookToken},
	"/fleetly.automation.v1.TasksService/CreateTask":                    {field: "project_id", kind: anchor.KindProject},
	"/fleetly.automation.v1.TasksService/ScaleTask":                     {field: "id", kind: anchor.KindTask},
	"/fleetly.automation.v1.TasksService/DeleteTask":                    {field: "id", kind: anchor.KindTask},
	"/fleetly.automation.v1.SchedulesService/CreateSchedule":            {field: "project_id", kind: anchor.KindProject},
	"/fleetly.automation.v1.SchedulesService/DeleteSchedule":            {field: "id", kind: anchor.KindSchedule},
	"/fleetly.automation.v1.SchedulesService/TriggerSchedule":           {field: "id", kind: anchor.KindSchedule},
	"/fleetly.edge.v1.RoutesService/CreateRoute":                        {field: "project_id", kind: anchor.KindProject},
	"/fleetly.edge.v1.RoutesService/DeleteRoute":                        {field: "id", kind: anchor.KindRoute},
	// 上传产物接入（F1.10，ADR-0019 附录 A.2）：client-streaming 写面——
	// Team 锚在首帧 meta.project_id（流式拦截器首帧 RecvMsg 执法；点路径
	// 读嵌套字段）。
	"/fleetly.delivery.v1.BuildsService/UploadSource": {field: "meta.project_id", kind: anchor.KindProject},
}

// FreezeGuard 是冻结执法器（拦截器一份实现覆盖全部封禁面动词）。
type FreezeGuard struct {
	db      *state.DB
	log     *slog.Logger
	freezes *freeze.Repo
	anchor  *anchor.Anchor
}

// NewFreezeGuard 构造执法器（归属解析图与 repo 从 DB 时钟派生）。
func NewFreezeGuard(db *state.DB, log *slog.Logger) *FreezeGuard {
	clock := db.Clock()
	return &FreezeGuard{
		db: db, log: log,
		freezes: freeze.New(clock),
		anchor:  anchor.New(clock),
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

// resolveTeam 解析请求的治理 Team（归属链经 anchor 解析图单源，方法表
// 见 internal/anchor）。resolved=false 表示寻址失败（空字段、行缺失或
// Team 轴外的行）——放行交由受理位拒绝；存储故障 fail-closed（E_INTERNAL）。
func (g *FreezeGuard) resolveTeam(ctx context.Context, req any, scope freezeScope) (string, bool, error) {
	ref := reqField(req, scope.field)
	if ref == "" {
		return "", false, nil
	}
	team, err := g.anchor.TeamOf(ctx, g.db.Runner(), scope.kind, ref)
	if err != nil {
		return resolutionError(err)
	}
	if team == "" {
		return "", false, nil // Team 轴外的行（不可达，ADR-0028 后必有 Team）
	}
	return team, true, nil
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
