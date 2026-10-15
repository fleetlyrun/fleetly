// Package audit 是审计聚合的领域面（架构 §6：Token/人/Agent 的一切写操作
// 留痕；来源枚举学 zane-ops，外加 platform 自治动作的 system 来源）。完整
// 执法（操作者解析/Scope 绑定）随账号批接入；写路径四件一拍落行。行类型、
// 来源词表、Team 轴 ctx 载具与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/audit（ADR-0075 Repo 接口层路线）。
//
// 归属裁决（P1b 批二）：audit 本是跨切面技术聚合（全部写路径共用，不专属
// 身份域），按 proto 服务族对号优先落 identity 上下文——AuditQueryService
// 住 identity（2026-10-14 映射表裁决）；跨切面性质由此注记，词表真源
// 仍是 CONTEXT.md Audit 词条。
package audit

import "context"

// Source 是写操作触发来源（只增枚举；F0.7 验收的来源过滤消费本列）。
type Source string

const (
	SourceManual   Source = "manual"   // 人工直接操作
	SourceAPI      Source = "api"      // API 直调（非 CLI）
	SourceCLI      Source = "cli"      // CLI（Agent 与人类共用面）
	SourceWebhook  Source = "webhook"  // Git webhook 触发
	SourceSchedule Source = "schedule" // Schedule 触发
	SourceSystem   Source = "system"   // 平台自治动作（看门狗/自动回滚/收口）
)

// Entry 是一条审计记录（前后值指纹：敏感值不进审计，只留指纹）。
type Entry struct {
	ID       string
	Seq      int64
	Actor    string // 操作者（Token 名/用户名；system 动作为空）
	Source   Source
	Action   string // 如 "project.create"
	Resource string // 如 "project/<id>"
	BeforeFP string // 变更前指纹（创建为空）
	AfterFP  string // 变更后指纹（删除为空）
	// Detail 是动作详情 JSON（ADR-0049 首用 = exec.session 的进程/实例/
	// 节点/命令面——exec 审计的命令面入账）。空串 = 无详情（既有行为零变化）。
	Detail string
	// TeamID 是操作者的 Team 轴（ADR-0035）：写入时由 Append 从 ctx 铸入
	// （调用方显式置空 = system/无身份动作）。'' = 平台级行（ListAudit 对
	// 全部 Team 可见——内容是动作名+资源 ID+指纹，无值载荷）。
	TeamID    string
	CreatedAt string
}

// ctxTeamKey 是审计 Team 轴的 ctx 载键（ADR-0035）：authn 拦截器为已认证
// 请求注入调用方 Team，webhook 面显式注入 App 归属 Team。独立于 authn 包
// 定义（audit 是叶子 repo，不 import authn——方向只许 authn → audit）。
type ctxTeamKey struct{}

// WithTeam 在 ctx 标注审计 Team 轴（请求路径由 authn 拦截器统一注入，
// 非请求路径（webhook）按资源归属显式注入）。
func WithTeam(ctx context.Context, teamID string) context.Context {
	return context.WithValue(ctx, ctxTeamKey{}, teamID)
}

// TeamFromContext 返回 ctx 标注的审计 Team（无标注 = 空串 = 平台级行）。
func TeamFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxTeamKey{}).(string); ok {
		return v
	}
	return ""
}

// Filter 是审计查询面（F0.7）：actor/resource 精确、action 前缀、source
// 枚举、Team 轴（ADR-0035）；全部可空。
type Filter struct {
	Actor        string
	Source       string
	ActionPrefix string
	Resource     string
	// Team 非空 = 行级过滤（team_id = Team 或 ''平台级行；ADR-0035 决策 6）。
	Team  string
	Limit int
}
