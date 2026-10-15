// Package catalog 是 App 模板目录快照聚合的领域面（F3.3，ADR-0050 决策
// 4）：单行表（id=1），RefreshTemplates 成功后的整体替换语义。解析序 =
// 快照在场优先、内嵌目录兜底（内嵌在 internal/apptemplate，不落库）。
// RegistryService.ListRegistryCatalog 模板目录快照与 TemplatesService
// 同族，归属 delivery 上下文。行类型、ErrNoSnapshot 哨兵与 Repo 接口在
// 此单源，SQLite 实现住 infrastructure/persistence/catalog（ADR-0075
// Repo 接口层）。
package catalog

import "errors"

// ErrNoSnapshot 是快照缺席的哨兵（回退内嵌目录的正常态，非错误面）。
var ErrNoSnapshot = errors.New("catalog: no refreshed snapshot stored")

// Snapshot 是快照行（body 是清单 JSON 原文；条目级 digest 已在写入前核对）。
type Snapshot struct {
	Digest    string
	Body      string
	FetchedAt string
}
