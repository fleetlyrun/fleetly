// Package configrepo 是 Config 聚合的领域面（CONTEXT.md Config 词条：版本化
// 的非敏感挂载文件实体，可回读、有配额——OT-3）。行类型与 Repo 接口在此
// 单源，SQLite 实现住 infrastructure/persistence/config（ADR-0075 Repo
// 接口层路线）。包名 configrepo 避开与 internal/config（平台配置）的同名
// 混淆（沿 persistence 侧既有命名裁决）。
package configrepo

// Config 是聚合行（不可变版本：只有 INSERT 与读）。
type Config struct {
	ID        string
	Seq       int64
	ProjectID string
	Name      string
	Version   int64
	Content   []byte
	CreatedAt string
}
