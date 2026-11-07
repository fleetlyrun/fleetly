package pubtemplate

// Engine Catalog 的 pubsub 家族装配面（B4b，ADR-0096 决策 1/8；design
// §2.3/§4.1）：目录是投影非存储——DataEnginesService.ListDataEngines 从
// dbtemplate.Catalog() 与本面现场装配合并（零缓存零漂移）。dialect 自
// Template.Dialect（nats 自名；kafka 协议族互示兼容——B6b redpanda 接入
// 后同 "kafka" 值）。capabilities：backup 真值源 = 引擎级
// Template.DataCapabilities（B6a 拆分——kafka false 挂账）；browse 恒
// false（dbbrowser 注册表外语官既有拒——ADR-0051；kafka-ui 重/nats 无
// 轻量 web，browse 面挂账独立评估，design §4.6）；restore_compat =
// RestoreCompat 版本轴规则在册；migrate = backup 诚实推论
//（backup=false ⇒ migrate=false；nats backup 在册 → migrate 挂后续批次
// 开面，本批恒 false 诚实缺省）。license_note：nats/kafka 空（宽松许可
// 二进制再分发形态无披露义务）；redpanda BSL 1.1 披露（B6b，红队 C-AT-06
// ——Additional Use Grant + 4 年 Change Date 完整文案）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// CatalogFamily 是 pubsub 家族在目录里的 family 词面。
const CatalogFamily = enginetemplate.FamilyPubSub

// CatalogLicenseNoteRedpanda 是 redpanda 的许可披露（BSL 1.1，design §4.6
// 红队 C-AT-06 完整化：Additional Use Grant 禁 Streaming/Queuing Service
// 形态；Change Date 各版发布起 4 年逐版转 Apache 2.0——完整披露，托管合规
// 裁定归运营方，代码面不设限）。
const CatalogLicenseNoteRedpanda = "BSL 1.1 (Additional Use Grant: offering a Streaming Service or Queuing Service to third parties is not permitted; each release converts to Apache 2.0 at its Change Date, 4 years after that release — the operating team owns the compliance ruling)"

// catalogLicenseNotes 是引擎级 license_note 披露表（空 = 无披露义务——
// nats/kafka 宽松许可二进制再分发形态；redpanda BSL 类自 B6b 起露出）。
var catalogLicenseNotes = map[string]string{
	"redpanda": CatalogLicenseNoteRedpanda,
}

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
// 防调用方改注册表）。B6a 起 capabilities/dialect 按引擎级真值：kafka
// backup=false（挂账——无官方 dump 工具）⇒ migrate=false（诚实推论）；
// dialect 轴 kafka 协议族互示兼容（kafka/redpanda——B6b redpanda 接入后
// 同 dialect 值），nats 自名。
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
			Dialect:  tpl.Dialect(),
			Versions: append([]enginetemplate.VersionSpec(nil), specs...),
			Knobs:    tpl.Knobs(),
			Port:     tpl.Meta().Port,
			Capabilities: CatalogCapabilities{
				Backup:  tpl.DataCapabilities().Backup,
				Migrate: false, // 诚实推论半边：backup=false ⇒ migrate=false；nats 备份在册但 migrate 面挂后续批次
				Browse:  false,
				// restore_compat 随能力位：无备份面即无「备份跨版本装载」可
				// 言（kafka/redpanda false——目录不虚标恢复轴）。
				RestoreCompat: tpl.DataCapabilities().Backup,
			},
			LicenseNote: catalogLicenseNotes[engine],
		})
	}
	return out
}
