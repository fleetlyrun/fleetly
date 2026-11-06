package pubtemplate

// Engine Catalog 的 pubsub 家族装配面（B4b，ADR-0096 决策 1/8；design
// §2.3/§4.1）：目录是投影非存储——DataEnginesService.ListDataEngines 从
// dbtemplate.Catalog() 与本面现场装配合并（零缓存零漂移）。dialect 自名
// （nats——engine 名 = 实现名，不造合成名；kafka 协议族互示兼容的方言轴
// 分叉值 B6 接入）。capabilities：backup 真值源 = Family.
// DataCapabilities.Backup；browse 恒 false（dbbrowser 注册表外语官既有
// 拒——ADR-0051；kafka-ui 重/nats 无轻量 web，browse 面挂账独立评估，
// design §4.6）；restore_compat = RestoreCompat 版本轴规则在册；
// migrate = backup 诚实推论（backup=false ⇒ migrate=false；nats backup
// 在册 → migrate 挂后续批次开面，本批恒 false 诚实缺省）。license_note
// 空（nats 是 Go-nats 类许可下的二进制再分发形态——无 BSL/AGPL 类披露
// 义务；redpanda BSL 类披露自 kafka 族起出现）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// CatalogFamily 是 pubsub 家族在目录里的 family 词面。
const CatalogFamily = enginetemplate.FamilyPubSub

// CatalogCapabilities 是单引擎的能力自描述（目录回显面；dbtemplate 同形
// ——interfaces 层映射为 proto EngineCapabilities）。
type CatalogCapabilities struct {
	Backup        bool
	Migrate       bool
	Browse        bool
	RestoreCompat bool
}

// CatalogEntry 是单个引擎的目录投影行（dbtemplate.CatalogEntry 同形——
// 两家族装配面在 DataEnginesService 合并）。
type CatalogEntry struct {
	Family       string
	Engine       string
	Dialect      string
	Versions     []enginetemplate.VersionSpec
	Knobs        []enginetemplate.Knob
	Port         int32
	Capabilities CatalogCapabilities
	LicenseNote  string
}

// Catalog 装配 pubsub 家族的目录投影（引擎序 = Engines() 排序稳定；副本
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
			Family:   CatalogFamily,
			Engine:   engine,
			Dialect:  engine,
			Versions: append([]enginetemplate.VersionSpec(nil), specs...),
			Knobs:    tpl.Knobs(),
			Port:     tpl.Meta().Port,
			Capabilities: CatalogCapabilities{
				Backup:        Family.DataCapabilities.Backup,
				Migrate:       false,
				Browse:        false,
				RestoreCompat: true,
			},
		})
	}
	return out
}
