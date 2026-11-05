package dbtemplate

// Engine Catalog 的 database 家族装配面（B2c，ADR-0094 决策 4 + design
// §2.3）：目录是投影非存储——服务端（DataEnginesService.ListDataEngines）
// 从本注册表现场装配，零缓存零漂移；Console/CLI 引擎取值一律走目录。
// 本批仅 database 家族（pubtemplate/buckettemplate 随 B4b/B5 接入——届时
// 装配面上移 family descriptor 注册表，ADR-0096 决策 1 的统一托管服务
// 机制面）；dialect 字段 database 家族 = engine 自名（Kafka 协议族互示
// 兼容的方言轴自 pubsub 家族起才有分叉值）。
//
// capabilities 是引擎能力自描述（受理面与 UI 消费）：database 家族五引擎
// 全员 backup（Backup/Restore 渲染面）+ browse（dbbrowser 注册表全覆盖，
// totality 由其守卫钉死）+ restore_compat（RestoreCompat 版本轴规则）；
// migrate（跨引擎统一转储契约，design §4.5）随后续批次开面，本批恒 false
// ——诚实缺省，不虚报能力。license_note 空 = 无披露义务（redpanda BSL
// 类披露自 pubsub 家族起出现）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// CatalogFamily 是 database 家族在目录里的 family 词面。
const CatalogFamily = "database"

// CatalogCapabilities 是单引擎的能力自描述（目录回显面）。
type CatalogCapabilities struct {
	// Backup：转储/恢复渲染面在册（Backup/Restore spec）。
	Backup bool
	// Migrate：跨引擎统一转储契约（design §4.5，后续批次）。
	Migrate bool
	// Browse：数据浏览器方言映射在册（dbbrowser 注册表）。
	Browse bool
	// RestoreCompat：同引擎跨版本恢复受理（RestoreCompat 版本轴规则）。
	RestoreCompat bool
}

// CatalogEntry 是单个引擎的目录投影行（interfaces 层映射为 proto
// EngineCatalog；本形状住模板包 = 引擎知识不出域）。
type CatalogEntry struct {
	// Family 是家族词面（database；pubsub/bucket_store 随 B4b/B5）。
	Family string
	// Engine 是引擎名（值域键，与 Create*.Request.engine 同域）。
	Engine string
	// Dialect 是方言自名（database 家族 = engine 名；协议族兼容互示自
	// pubsub 家族起分叉，如 redpanda 对 kafka 协议）。
	Dialect string
	// Versions 是在册版本矩阵（声明序旧→新；含 Default/Deprecated/Note）。
	Versions []enginetemplate.VersionSpec
	// Knobs 是旋钮白名单 schema（ADR-0095 决策 1/10：目录驱动表单）。
	Knobs []enginetemplate.Knob
	// Port 是模板服务端口。
	Port int32
	// Capabilities 是能力自描述。
	Capabilities CatalogCapabilities
	// LicenseNote 是许可披露（空 = 无披露义务）。
	LicenseNote string
}

// Catalog 装配 database 家族的目录投影（引擎序 = Engines() 排序稳定；
// 副本防调用方改注册表——投影非存储，逐次现场装配）。
func Catalog() []CatalogEntry {
	engines := Engines()
	out := make([]CatalogEntry, 0, len(engines))
	for _, engine := range engines {
		tpl, err := For(engine, "")
		if err != nil {
			// For(engine, "") 失败 = 注册表失不变量（无 Default）；目录
			// 面不兜底不编造，跳过即遗漏会被门禁（每引擎恰一 Default）
			// 在测试期钉死——此处防御性跳过保持启动面可用。
			continue
		}
		specs, _ := VersionsFor(engine)
		entry := CatalogEntry{
			Family:   CatalogFamily,
			Engine:   engine,
			Dialect:  engine,
			Versions: append([]enginetemplate.VersionSpec(nil), specs...),
			Knobs:    tpl.Knobs(),
			Port:     tpl.Meta().Port,
			Capabilities: CatalogCapabilities{
				Backup:        true,
				Migrate:       false,
				Browse:        true,
				RestoreCompat: true,
			},
		}
		out = append(out, entry)
	}
	return out
}
