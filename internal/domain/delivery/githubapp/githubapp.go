// Package githubapp 是 GitHub App 接入材料聚合的领域面（ADR-0073 阶段 B，
// ADR-0075 Repo 接口层）：私有 Git 仓库的构建期访问材料。私钥 age 信封
// 只写不读，读面只回指纹；account_login 受理即经 GitHub API 解析（系统
// 填充），是构建期 host+属主 解析的匹配键。双作用域：ProjectID 空 =
// 平台级行（平台管理授权，全局唯一），非空 = Project 级（行级 Project
// 授权，域内唯一）；解析时 Project 级遮蔽 Global。行类型与 Repo 接口在
// 此单源，SQLite 实现住 infrastructure/persistence/githubapp（密文列投影/
// rewrap 机械留实现侧）。
package githubapp

// App 是聚合行（PrivateKeyCiphertext 是 age 信封；Fingerprint 供回显
// 对账）。
type App struct {
	ID                    string
	Seq                   int64
	ProjectID             string // 空 = 平台级 Global
	AppID                 int64
	InstallationID        int64
	AccountLogin          string
	APIBase               string // 空 = 缺省 https://api.github.com
	PrivateKeyCiphertext  []byte
	PrivateKeyFingerprint string
	CreatedAt             string
	UpdatedAt             string
	DeletedAt             string
}

// Deleted 报告 tombstone 状态。
func (a *App) Deleted() bool { return a.DeletedAt != "" }
