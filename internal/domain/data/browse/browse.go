// Package browse 是 Browse 会话回收台账聚合的领域面（F3.6，ADR-0051
// 决策 1，ADR-0075 Repo 接口层）：行承载"哪个库开了什么形态的会话、何时
// 到期"——凭证不在行上（Ensure 期从 Secret 单真源重新解封渲染；Launcher
// Ticket 形态是受理面铸造物，不落行）。回收即删行（会话非资源行，无
// 软删）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/browse。
package browse

import "time"

// Session 是一行回收台账。
type Session struct {
	ID         string
	Seq        int64
	ProjectID  string
	DatabaseID string
	Engine     string
	ReadOnly   bool
	CreatedAt  string
	ExpiresAt  string
}

// ExpiryTime 解析行上到期时刻（RFC3339 读回面）。
func (s *Session) ExpiryTime() (time.Time, error) {
	return time.Parse(time.RFC3339, s.ExpiresAt)
}

// CreatedTime 解析行上受理时刻。
func (s *Session) CreatedTime() (time.Time, error) {
	return time.Parse(time.RFC3339, s.CreatedAt)
}
