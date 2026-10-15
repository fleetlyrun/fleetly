// Package freeze 是 Change Freeze 聚合的领域面（CONTEXT.md Change Freeze
// 词条：变更冻结窗——按资源所属 Team 封禁变更型动词的治理刹车；ADR-0017
// 附录 A.3，F1.9；ADR-0075 Repo 接口层）。team_id 空串是全局冻结行；每
// scope 至多一条活跃行（部分唯一索引把守）；lift 落 lifted_at，历史行
// 保留可回读。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/freeze。
package freeze

// Freeze 是聚合行（lifted_at 空 = 活跃冻结）。
type Freeze struct {
	ID        string
	Seq       int64
	TeamID    string // '' = 全局冻结（封禁全部 Team）
	Reason    string
	CreatedBy string
	CreatedAt string
	LiftedAt  string
}

// Active 报告是否活跃冻结。
func (f *Freeze) Active() bool { return f.LiftedAt == "" }
