package guards

// family descriptor 注册表对账守卫（ADR-0096 决策 1/8；B2e 卫星归一批
// 2026-11-06）。descriptor 本体住 domain（dataservicetemplate，红队 B-AT-01），
// 本守卫把它与事件码注册表、环表登记簿三方对账——「每族一个 descriptor、
// 每族四件事件、每族一环」从散文变成机械红灯。
//
// 律文：
//
//	①在册族基线 familyQuotaBaseline（族 → 配额）：B2e = {database:16}；B4b
//	  登记 pubsub（8）、B5 登记 bucketstore（4）时同批扩本表——两批已落（ADR-0096
//	  决策 8 配额值在此钉板，双向保鲜——多登/漏登/改值皆红）；
//	②每在册族的 EventFamily 四件（created/updated/deleted/credentials_
//	  rotated）必须在 eventcode 注册表在册（决策 8「各四」与 database.
//	  credentials_rotated 先例对齐）——EventFamily 成员的消费面；
//	③三枚家族词面各有一条环表登记行：环名 = 家族词、step = dataservices
//	  分族步 `e.dataservices.StepFor(dataservicetemplate.Family<X>)`（ringRegistry
//	  同包可读）——占位环先于 descriptor 存在（机制先行），登记即活、环表
//	  零改是本律的承诺面。
//
// 防盲：在册族数下限 1（database 恒在）；familyRingSteps 三行与家族常量
// 一一对应（常量改名即编译红）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/eventcode"
)

// familyQuotaBaseline 是在册族 → per-Project 配额的冻结表（①）。
var familyQuotaBaseline = map[string]int{
	dataservicetemplate.FamilyDatabase:    16, // ADR-0029 决策 10 / ADR-0017 配额族（B2e 自 gRPC 常量迁 descriptor，值不动）
	dataservicetemplate.FamilyPubSub:      8,  // ADR-0096 决策 8（B4b 随 pubsubtemplate 登记解注）
	dataservicetemplate.FamilyBucketStore: 4,  // ADR-0096 决策 8（B5 随 bucketstoretemplate 登记解注）
}

// familyEventSuffixes 是每族事件最小集（②，ADR-0096 决策 8「各四」）。
var familyEventSuffixes = []string{"created", "updated", "deleted", "credentials_rotated"}

// familyRingSteps 是家族词 → 环表 step 渲染串（③；ringregistry 的调用形
// 渲染口径 `链(选择子)`）。
var familyRingSteps = map[string]string{
	dataservicetemplate.FamilyDatabase:    "e.dataservices.StepFor(dataservicetemplate.FamilyDatabase)",
	dataservicetemplate.FamilyPubSub:      "e.dataservices.StepFor(dataservicetemplate.FamilyPubSub)",
	dataservicetemplate.FamilyBucketStore: "e.dataservices.StepFor(dataservicetemplate.FamilyBucketStore)",
}

func TestFamilyDescriptorRegistryBaseline(t *testing.T) {
	// 显式锚定登记方：database 族由 databasetemplate 包初始化期登记（var 初始化
	// 式）；引用 Family 让依赖进本文件而非倚赖同包其他测试的 import。
	if databasetemplate.Family.Family != dataservicetemplate.FamilyDatabase {
		t.Fatalf("databasetemplate.Family must describe the database family, got %+v", databasetemplate.Family)
	}
	if pubsubtemplate.Family.Family != dataservicetemplate.FamilyPubSub {
		t.Fatalf("pubsubtemplate.Family must describe the pubsub family, got %+v", pubsubtemplate.Family)
	}
	if bucketstoretemplate.Family.Family != dataservicetemplate.FamilyBucketStore {
		t.Fatalf("bucketstoretemplate.Family must describe the bucketstore family, got %+v", bucketstoretemplate.Family)
	}
	families := dataservicetemplate.Families()
	if len(families) < 1 {
		t.Fatalf("family descriptor registry is empty — guard is blind or databasetemplate stopped registering (ADR-0096 decision 1)")
	}
	// ①族集 + 配额对账（双向）。
	got := map[string]int{}
	for _, f := range families {
		got[f.Family] = f.Quota
		if _, ok := familyQuotaBaseline[f.Family]; !ok {
			t.Errorf("family %q is registered but not in familyQuotaBaseline — a new data-service family must land with its quota pinned here in the same batch (ADR-0096 decision 8)", f.Family)
		}
	}
	for family, quota := range familyQuotaBaseline {
		q, ok := got[family]
		switch {
		case !ok:
			t.Errorf("familyQuotaBaseline lists %q but no descriptor is registered — stale baseline or a template package stopped registering (双向保鲜)", family)
		case q != quota:
			t.Errorf("family %q registers quota %d, baseline freezes %d — quota changes are product decisions (ADR-0096 decision 8), update the baseline deliberately", family, q, quota)
		}
	}
	// ②事件码族四件。
	for _, f := range families {
		for _, suffix := range familyEventSuffixes {
			name := f.EventFamily + "." + suffix
			if _, ok := eventcode.Get(name); !ok {
				t.Errorf("family %q declares event family %q but %q is not in the eventcode registry — each data-service family carries the four lifecycle events (ADR-0096 decision 8)", f.Family, f.EventFamily, name)
			}
		}
	}
	// ③每族一环：三枚家族词面各一条环表登记行，step 绑定同族分族步。
	for family, wantStep := range familyRingSteps {
		found := false
		for _, row := range ringRegistry {
			if row.name != family {
				continue
			}
			found = true
			if row.step != wantStep {
				t.Errorf("ring %q is registered with step %q, want the family step %q — one ring per family, bound to its own family word (ADR-0096 decision 1)", family, row.step, wantStep)
			}
		}
		if !found {
			t.Errorf("no ring is registered under family name %q — the ring table carries one placeholder ring per data-service family ahead of its descriptor (ADR-0096 decision 1, 10→12)", family)
		}
	}
}
