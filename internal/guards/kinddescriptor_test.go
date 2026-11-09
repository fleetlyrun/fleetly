package guards

// kind descriptor 注册表对账契约测试（ADR-0096 决策 1/8；B2e 应用模块归一批
// 2026-11-06）。descriptor 本体住 domain（dataservicetemplate，红队 B-AT-01），
// 本契约测试把它与事件码注册表、循环表登记簿三方对账——「每类型一个 descriptor、
// 每类型四件事件、每类型一循环」从散文变成机械红灯。
//
// 律文：
//
//	①在册类型基线 kindQuotaBaseline（类型 → 配额）：B2e = {database:16}；B4b
//	  登记 pubsub（8）、B5 登记 bucketstore（4）时同批扩本表——两批已落（ADR-0096
//	  决策 8 配额值在此钉板，双向保鲜——多登/漏登/改值皆红）；
//	②每在册类型的 EventPrefix 四件（created/updated/deleted/credentials_
//	  rotated）必须在 eventcode 注册表在册（决策 8「各四」与 database.
//	  credentials_rotated 先例对齐）——EventPrefix 成员的消费面；
//	③三枚类型词面各有一条循环表登记行：循环名 = 类型词、step = dataservices
//	  分类型步 `e.dataservices.StepFor(dataservicetemplate.Kind<X>)`（ringRegistry
//	  同包可读）——占位循环先于 descriptor 存在（机制先行），登记即活、循环表
//	  零改是本律的承诺面。
//
// 防盲：在册类型数下限 1（database 恒在）；kindRingSteps 三行与类型常量
// 一一对应（常量改名即编译红）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/eventcode"
)

// kindQuotaBaseline 是在册类型 → per-Project 配额的冻结表（①）。
var kindQuotaBaseline = map[string]int{
	dataservicetemplate.KindDatabase:    16, // ADR-0029 决策 10 / ADR-0017 配额族（B2e 自 gRPC 常量迁 descriptor，值不动）
	dataservicetemplate.KindPubSub:      8,  // ADR-0096 决策 8（B4b 随 pubsubtemplate 登记解注）
	dataservicetemplate.KindBucketStore: 4,  // ADR-0096 决策 8（B5 随 bucketstoretemplate 登记解注）
}

// kindEventSuffixes 是每类型事件最小集（②，ADR-0096 决策 8「各四」）。
var kindEventSuffixes = []string{"created", "updated", "deleted", "credentials_rotated"}

// kindRingSteps 是类型词 → 循环表 step 渲染串（③；ringregistry 的调用形
// 渲染口径 `链(选择子)`）。
var kindRingSteps = map[string]string{
	dataservicetemplate.KindDatabase:    "e.dataservices.StepFor(dataservicetemplate.KindDatabase)",
	dataservicetemplate.KindPubSub:      "e.dataservices.StepFor(dataservicetemplate.KindPubSub)",
	dataservicetemplate.KindBucketStore: "e.dataservices.StepFor(dataservicetemplate.KindBucketStore)",
}

func TestKindDescriptorRegistryBaseline(t *testing.T) {
	// 显式锚定登记方：database 类型由 databasetemplate 包初始化期登记（var 初始化
	// 式）；引用 Kind 让依赖进本文件而非倚赖同包其他测试的 import。
	if databasetemplate.Kind.Kind != dataservicetemplate.KindDatabase {
		t.Fatalf("databasetemplate.Kind must describe the database kind, got %+v", databasetemplate.Kind)
	}
	if pubsubtemplate.Kind.Kind != dataservicetemplate.KindPubSub {
		t.Fatalf("pubsubtemplate.Kind must describe the pubsub kind, got %+v", pubsubtemplate.Kind)
	}
	if bucketstoretemplate.Kind.Kind != dataservicetemplate.KindBucketStore {
		t.Fatalf("bucketstoretemplate.Kind must describe the bucketstore kind, got %+v", bucketstoretemplate.Kind)
	}
	kinds := dataservicetemplate.Kinds()
	if len(kinds) < 1 {
		t.Fatalf("kind descriptor registry is empty — guard is blind or databasetemplate stopped registering (ADR-0096 decision 1)")
	}
	// ①类型集 + 配额对账（双向）。
	got := map[string]int{}
	for _, k := range kinds {
		got[k.Kind] = k.Quota
		if _, ok := kindQuotaBaseline[k.Kind]; !ok {
			t.Errorf("kind %q is registered but not in kindQuotaBaseline — a new data-service kind must land with its quota pinned here in the same batch (ADR-0096 decision 8)", k.Kind)
		}
	}
	for kind, quota := range kindQuotaBaseline {
		q, ok := got[kind]
		switch {
		case !ok:
			t.Errorf("kindQuotaBaseline lists %q but no descriptor is registered — stale baseline or a template package stopped registering (双向保鲜)", kind)
		case q != quota:
			t.Errorf("kind %q registers quota %d, baseline freezes %d — quota changes are product decisions (ADR-0096 decision 8), update the baseline deliberately", kind, q, quota)
		}
	}
	// ②事件码前缀四件。
	for _, k := range kinds {
		for _, suffix := range kindEventSuffixes {
			name := k.EventPrefix + "." + suffix
			if _, ok := eventcode.Get(name); !ok {
				t.Errorf("kind %q declares event prefix %q but %q is not in the eventcode registry — each data-service kind carries the four lifecycle events (ADR-0096 decision 8)", k.Kind, k.EventPrefix, name)
			}
		}
	}
	// ③每类型一循环：三枚类型词面各一条循环表登记行，step 绑定同类型分类型步。
	for kind, wantStep := range kindRingSteps {
		found := false
		for _, row := range ringRegistry {
			if row.name != kind {
				continue
			}
			found = true
			if row.step != wantStep {
				t.Errorf("ring %q is registered with step %q, want the kind step %q — one ring per kind, bound to its own kind word (ADR-0096 decision 1)", kind, row.step, wantStep)
			}
		}
		if !found {
			t.Errorf("no ring is registered under kind name %q — the ring table carries one placeholder ring per data-service kind ahead of its descriptor (ADR-0096 decision 1, 10→12)", kind)
		}
	}
}
