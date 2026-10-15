// Package user 是 User 聚合的领域面（CONTEXT.md User 词条：人类身份；
// 经 membership 在 Team 内持 Role 获权）。行类型与 Repo 接口在此单源，
// SQLite 实现住 infrastructure/persistence/user（ADR-0075 Repo 接口层）。
package user

// User 是聚合行。PasswordHash 是密码凭证第二形态（C6 第一期；bcrypt；
// 空 = 未设密——密码登录诚实拒绝）。
type User struct {
	ID           string
	Seq          int64
	Name         string
	CreatedAt    string
	PasswordHash string
}
