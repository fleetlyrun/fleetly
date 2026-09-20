package eventcode

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// docEvents 是验收标准的内联文档清单（手抄自设计文档，逐条注明出处）。
var docEvents = map[string]string{ // event → 文档出处
	// release-semantics.md §2.7：deployment.* 16 个
	"deployment.queued":             "release-semantics §2.7",
	"deployment.release_started":    "release-semantics §2.7",
	"deployment.healthy":            "release-semantics §2.7",
	"deployment.switched":           "release-semantics §2.7",
	"deployment.observe_started":    "release-semantics §2.7",
	"deployment.succeeded":          "release-semantics §2.7",
	"deployment.failed":             "release-semantics §2.7",
	"deployment.warning":            "release-semantics §2.7",
	"deployment.cancelled":          "release-semantics §2.7",
	"deployment.rollback_started":   "release-semantics §2.7",
	"deployment.rollback_finished":  "release-semantics §2.7",
	"deployment.rollback_failed":    "release-semantics §2.7",
	"deployment.recovery_scheduled": "release-semantics §2.7",
	"deployment.recovery_blocked":   "release-semantics §2.7",
	"deployment.substrate_halted":   "release-semantics §2.7",
	"deployment.superseded":         "release-semantics §2.7",

	// release-semantics.md §2.7：app.* 3 个
	"app.degraded":             "release-semantics §2.7",
	"app.instability_detected": "release-semantics §2.7",
	"app.recovered":            "release-semantics §2.7",

	// stateful-placement.md §2.8：placement.* 5 / node.* 4 / volume.* 4
	"placement.bound":      "stateful-placement §2.8",
	"placement.changed":    "stateful-placement §2.8",
	"placement.blocked":    "stateful-placement §2.8",
	"placement.recovered":  "stateful-placement §2.8",
	"placement.unresolved": "stateful-placement §2.8",
	"node.joined":          "stateful-placement §2.8",
	"node.down":            "stateful-placement §2.8",
	"node.up":              "stateful-placement §2.8",
	"node.removed":         "stateful-placement §2.8",
	"volume.created":       "stateful-placement §2.8",
	"volume.detached":      "stateful-placement §2.8",
	"volume.orphaned":      "stateful-placement §2.8",
	"volume.discarded":     "stateful-placement §2.8",

	// state-model.md §2.9（对账漂移）/ §2.7（恢复完成）
	"reconcile.drift_detected": "state-model §2.9",
	"restore.completed":        "state-model §2.7",

	// architecture.md §4.3（cron 触发前哨「记 skipped + 事件」，v0.2）
	"cron.skipped": "architecture §4.3",

	// T2.15 实现期新增（文档外事件名单独列出，待 T0.5 契约冻结确认）：架构
	// §2.5 不变量「路由发布严格晚于健康门；发布失败不回滚部署、单独告警 +
	// 审计」。证书签发/续期不设新事件名（走审计记录）。
	"route.published":      "T2.15 实现期新增（architecture §2.5 路由发布时机；待 T0.5 冻结确认）",
	"route.publish_failed": "T2.15 实现期新增（architecture §2.5 路由失败单独告警；待 T0.5 冻结确认）",

	// S17-D1 实现期新增（评审类 D 超时与取消闭环）：webhook 受理转异步后
	// 拉源失败只能走事件流披露（官方不重投）。
	"app.webhook_fetch_failed": "S17-D1 实现期新增（评审类 D；webhook 异步拉源失败披露）",

	// S18-A10 实现期新增（评审类 A 运行时断言层，§9 裁决并入 janitor）：
	// 非终态行超龄停留的显性化告警。
	"engine.stale_nonterminal": "S18-A10 实现期新增（评审类 A；部署非终态超龄告警）",
	"build.stale_nonterminal":  "S18-A10 实现期新增（评审类 A；构建非终态超龄告警）",

	// B6/H10 实现期新增（MG-3 横切结构修复）：app 删除生命周期第二拍的
	// 终局事件（引擎 deleting 回收 duty 发出）。
	"app.deleted": "B6/H10 实现期新增（MG-3；app 删除第二拍终局）",
}

// TestDocEventSetMatchesRegistry：注册表事件集与文档清单逐一致。
func TestDocEventSetMatchesRegistry(t *testing.T) {
	regNames := Default().Names()
	if len(regNames) != len(docEvents) {
		t.Fatalf("registry has %d events, doc list has %d", len(regNames), len(docEvents))
	}
	for _, name := range regNames {
		if _, ok := docEvents[name]; !ok {
			t.Errorf("registry event %q 不在文档清单内（文档外事件名须单独列出并标注待 T0.5 冻结确认）", name)
		}
	}
	for name, source := range docEvents {
		if _, ok := Default().Get(name); !ok {
			t.Errorf("doc event %q（%s）未录入注册表：遗漏", name, source)
		}
	}
}

// TestDuplicateRegistrationRejected：重复注册 fail-fast。
func TestDuplicateRegistrationRejected(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(Event{Name: "test.duplicate", Summary: "s"})
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate event registration must panic (fail-fast)")
		}
	}()
	r.MustRegister(Event{Name: "test.duplicate", Summary: "s2"})
}

// TestInvalidFormatRejected：非法格式 fail-fast（大写、缺 namespace 段、
// 多段、空串、数字开头）。
func TestInvalidFormatRejected(t *testing.T) {
	cases := []string{
		"",
		"UPPER.case",
		"noDot",
		"two.dots.here",
		".leading",
		"trailing.",
		"1number.start",
		"deployment.failed_extra_dash-", // 非法字符
	}
	for _, name := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("invalid event name %q must panic at registration", name)
				}
			}()
			NewRegistry().MustRegister(Event{Name: name, Summary: "s"})
		}()
	}
}

// TestGoldenSnapshot 事件集 golden 快照（防静默变更）。
func TestGoldenSnapshot(t *testing.T) {
	golden := filepath.Join("testdata", "events.golden")
	got := Default().Snapshot()
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o750); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // golden 为 testdata 固定路径
	if err != nil {
		t.Fatalf("read golden (run go test -update to regenerate): %v", err)
	}
	if string(want) != got {
		t.Fatalf("event set drifted from golden:\n--- golden ---\n%s\n--- registry ---\n%s", want, got)
	}
}

// TestNamespaces 命名空间覆盖核对：deployment/app/placement/node/volume/
// reconcile/restore/cron 八个域均非空。
func TestNamespaces(t *testing.T) {
	want := []string{"deployment", "app", "placement", "node", "volume", "reconcile", "restore", "cron"}
	seen := make(map[string]int)
	for _, name := range Default().Names() {
		seen[strings.SplitN(name, ".", 2)[0]]++
	}
	for _, ns := range want {
		if seen[ns] == 0 {
			t.Errorf("namespace %q has no registered events", ns)
		}
	}
}
