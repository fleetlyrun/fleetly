package writes

// 事实发射 kernel（ADR-0064 序列收口的 P2 刀 1 形态，架构收敛报告 §刀1
// "事实发射单源"）：一次写事实单声明（FactRow）→ 扇出 outbox 流面 +
// audit 取证面双行。此前 Events/Audits 双切片配对靠人肉——四族 CRUD
// 曾漏声明事件（2026-10-03 漂移史的配对面）；单声明后"声明一条事实、
// 两行同源落库"是 kernel 拥有的不变量，偏离双真（LedgerOnly/
// StreamOnly）必须显式带旗并注释理由（批三守卫执法）。
//
// 分层：domain 环内包，只依赖 domain/telemetry/outbox 与 domain/identity/
// audit 的 Repo 接口（P1b 批二起两侧句柄上移 domain）。操作者解析
// （authn ctx）留应用层——FactRow.Actor/Source 由调用方填充，domain
// 不 import authn。日志同理留给调用方：Emit 返回 error，kernel 零
// 系统副作用（domain 三红线）。
//
// 两轴分立（P0-2 / ADR-0035）：outbox 行 team_id 是资源轴（FactRow.
// TeamID，调用方按聚合归属解析填充）；audit 行 team_id 是操作者轴
//（ctx 铸入，audit.Append 统一铸入点）——同一事实两行各自正确的轴，
// 互不代填。
//
// 同源 fact_id 取舍：两行无可写关联列——outbox 表结构与 Event 读面
// proto 冻结（本批红线），audit 侧 detail 铸入会改既有行字节面（行为
// 不变式禁止）。关联面以 Name+Aggregate+ID+createdAt 邻近对账承载
//（尽力而为：双行在同一 Sequence/Emit 调用内紧邻落库，同一事务内
// createdAt 秒精度同值；跨行对账走聚合锚+时间邻近）。
//
// 事务边界由调用方承载：Sequence 收调用方事务/运行器（事实按声明序
// 扇出，任一行失败即返回、后续事实零执行，调用方事务整单回滚）；
// Emit 是非事务 best-effort 面（观测事实/登录审计等无聚合写伴生的
// 事实）。检查→聚合写的段序由两写前门（api acceptance commit /
// engine commitWrite、commitTransition）自行编排——facts 惰性求值，
// 调用方先落写再入 Sequence，载荷/指纹即可依赖写后行。

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// errBothFlags 是双旗同立的编程错误（什么都不发射 = 静默丢事实）。
var errBothFlags = errors.New("writes: FactRow with both LedgerOnly and StreamOnly set emits nothing; declare one side explicitly")

// FactRow 是一次写事实的单声明形态：默认双真——outbox 流面行（保留窗
// 内）+ audit 取证面行（永续）同源落库；LedgerOnly/StreamOnly 显式
// 偏离双真，声明处须注释理由（批三守卫执法）。零值段合法（flag 决定
// 哪面消费哪段）。
type FactRow struct {
	Name      string       // 事件名（eventcode 注册表执法面）；LedgerOnly 时可空
	Aggregate string       // 聚合名；也是 audit Resource 的缺省锚（Aggregate/ID）
	ID        string       // 聚合行 ID
	Payload   []byte       // 流面载荷（保留窗内；LedgerOnly 时可空）
	Action    string       // 审计动词（"project.create"——动词态与事件名的既成态是两个词面，不可互推）；StreamOnly 时可空
	Resource  string       // 审计资源锚（"project/<id>"）；空 = Aggregate + "/" + ID
	Actor     string       // 操作者（Token 名/用户名；空 = system 动作）。操作者解析留应用层——domain 不 import authn
	Source    audit.Source // 来源；空 = SourceSystem
	Detail    string       // 审计详情 JSON（ADR-0049；可空）
	BeforeFP  string       // 取证面：变更前指纹（创建为空）
	AfterFP   string       // 取证面：变更后指纹（删除为空）
	TeamID    string       // 资源轴（outbox 行 team_id；"" = 平台级事件）。audit 行 Team 轴仍由 ctx 铸（操作者轴）——两轴各自正确
	// LedgerOnly 只落取证面（audit 单行）；声明处须注释理由。
	LedgerOnly bool
	// StreamOnly 只落流面（outbox 单行）；声明处须注释理由。
	StreamOnly bool
}

// Repos 是 kernel 的两个落库面（调用方注入）。flag 决定触碰哪面：未
// 触碰的 repo 可为零值（如 LedgerOnly 发射不装配 Outbox）。
type Repos struct {
	Outbox outbox.Repo
	Audits audit.Repo
}

// Sequence 在调用方事务/运行器内按声明序扇出全部事实。facts 惰性
// 求值（每条在其落库时点求值——载荷/指纹可依赖事务内写的返回行）；
// 任一行失败即返回，后续事实零求值零执行（调用方事务整单回滚）。
func Sequence(ctx context.Context, repos Repos, facts ...func() FactRow) error {
	for _, fact := range facts {
		if err := emit(ctx, repos, fact()); err != nil {
			return err
		}
	}
	return nil
}

// Emit 是非事务 best-effort 发射面（观测事实/登录审计等无聚合写伴生
// 的事实）：扇出与 flag 语义同 Sequence，run 通常是独立执行面
// （*sql.DB）。错误返回由调用方自记（kernel 不持有日志——domain 零
// 系统副作用）。
func Emit(ctx context.Context, repos Repos, f FactRow) error {
	return emit(ctx, repos, f)
}

// emit 扇出一条事实：默认双真——outbox 行（资源轴 TeamID）居前、
// audit 行（操作者轴，ctx 铸 Team）殿后；LedgerOnly 只落 audit、
// StreamOnly 只落 outbox。双旗同立拒绝（什么都不发射 = 静默丢事实）。
func emit(ctx context.Context, repos Repos, f FactRow) error {
	if f.LedgerOnly && f.StreamOnly {
		return errBothFlags
	}
	if !f.LedgerOnly {
		if _, err := repos.Outbox.Append(ctx, f.TeamID, f.Name, f.Aggregate, f.ID, f.Payload); err != nil {
			return err
		}
	}
	if !f.StreamOnly {
		entry := &audit.Entry{
			Actor:    f.Actor,
			Source:   f.Source,
			Action:   f.Action,
			Resource: f.Resource,
			Detail:   f.Detail,
			BeforeFP: f.BeforeFP,
			AfterFP:  f.AfterFP,
		}
		if entry.Source == "" {
			entry.Source = audit.SourceSystem // system 动作缺省来源（与 engine 侧既有铸型一致）
		}
		if entry.Resource == "" {
			entry.Resource = f.Aggregate + "/" + f.ID // 资源锚缺省 = 聚合/行 ID
		}
		if err := repos.Audits.Append(ctx, entry); err != nil {
			return err
		}
	}
	return nil
}
