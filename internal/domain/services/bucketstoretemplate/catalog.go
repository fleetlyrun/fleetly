package bucketstoretemplate

// Engine Catalog 的 bucketstore 类型装配面（B5，ADR-0096 决策 1/8；design
// §2.3/§4.6）：目录是投影非存储——DataEnginesService.ListDataEngines 从
// databasetemplate.Catalog / pubsubtemplate.Catalog 与本面现场装配合并（零缓存零
// 漂移）。dialect = `s3`（S3 兼容自名——engine 名 = 实现名 silo、方言轴 =
// 连接/注入形态复用单元，ADR-0093/0096 决策 7；未来兼容实现同 dialect
// 互示）。capabilities 全族诚实缺省：backup/migrate=false（silo 转储挂账，
// design §4.5 L4 表——backup=false ⇒ migrate=false 能力位推论）、browse
// 恒 false（dbbrowser 注册表外语官既有拒——ADR-0051；轻量 S3 web 工具
// 挂账独立评估）、restore_compat=false（无备份面即无版本装载矩阵）。
// license_note = AGPLv3 披露（MinIO fork 继承——红队 C-AT-06 全引擎化
// 裁定；托管合规裁定归运营方）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// CatalogKind 是 bucketstore 类型在目录里的 kind 词面。
const CatalogKind = dataservicetemplate.KindBucketStore

// CatalogDialectS3 是 S3 方言族词面（silo 与未来 S3 兼容实现的互示兼容轴）。
const CatalogDialectS3 = "s3"

// CatalogLicenseNoteSilo 是 silo 的许可披露（AGPLv3——MinIO fork 继承；
// 目录显著露出，合规裁定归运营方）。
const CatalogLicenseNoteSilo = "AGPLv3 (silo is a MinIO fork; the AGPL network clause covers platform-hosted deployments — the operating team owns the compliance ruling)"

// CatalogCapabilities 是单引擎的能力自描述（目录回显面；pubsubtemplate 同形
// ——interfaces 层映射为 proto EngineCapabilities）。
type CatalogCapabilities struct {
	Backup        bool
	Migrate       bool
	Browse        bool
	RestoreCompat bool
}

// CatalogEntry 是单个引擎的目录投影行（pubsubtemplate.CatalogEntry 同形——
// 三类型装配面在 DataEnginesService 合并）。
type CatalogEntry struct {
	Kind         string
	Engine       string
	Dialect      string
	Versions     []dataservicetemplate.VersionSpec
	Knobs        []dataservicetemplate.Knob
	Port         int32
	Capabilities CatalogCapabilities
	LicenseNote  string
}

// Catalog 装配 bucketstore 类型的目录投影（引擎序 = Engines() 排序稳定；副本
// 防调用方改注册表）。
func Catalog() []CatalogEntry {
	engines := Engines()
	out := make([]CatalogEntry, 0, len(engines))
	for _, engine := range engines {
		tpl, err := For(engine, "")
		if err != nil {
			continue // 注册表失不变量防御跳过（门禁在测试期钉死）
		}
		specs, _ := VersionsFor(engine)
		out = append(out, CatalogEntry{
			Kind:     CatalogKind,
			Engine:   engine,
			Dialect:  CatalogDialectS3,
			Versions: append([]dataservicetemplate.VersionSpec(nil), specs...),
			Knobs:    tpl.Knobs(),
			Port:     tpl.Meta().Port,
			Capabilities: CatalogCapabilities{
				Backup:        Kind.DataCapabilities.Backup,
				Migrate:       false,
				Browse:        false,
				RestoreCompat: false,
			},
			LicenseNote: CatalogLicenseNoteSilo,
		})
	}
	return out
}
