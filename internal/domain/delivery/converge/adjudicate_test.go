package converge

// 裁决表白盒（P3a 批第 4 单元，ADR-0087 决策 1/3/7：自 application/engine
// converge_test.go 的裁决表五测试迁移——策略表归域白盒，engine 侧只保机制
// 钉测）。覆盖：8 判定行 golden（表即断言）+ 信号类→行映射逐条 + 枚举完备
// 性 + 闸门映射与优先序 + 字段⇄生产者双向保鲜 + 优先序组合 + 信号值字面与
// 聚合签名。纯白盒：零 fake、零编排依赖（import 可扫项）。

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var verdictGoldenUpdate = flag.Bool("update", false, "rewrite converge verdict golden")

// ---- 裁决表 golden（ADR-0082 §1：8 判定行 + 2 非行——表即断言）----

func TestVerdictGolden(t *testing.T) {
	rows := []struct {
		name string
		in   Input
	}{
		{"01-spec-mismatch", Input{SpecMismatch: true}},
		{"02-absent-carrier", Input{Absent: true}},
		{"03-gen-deviation", Input{GenDeviated: true}},
		{"04-rollout-stalled-terminal", Input{RolloutStalled: true}},
		// 行5（spec 相符 + stopped）是信号层非行：稳态看门狗只发事件、不产
		// 收敛信号，判定函数无此分支（ADR-0082 修订版 §1）。
		{"06-active-deployment-incl-failed-rollback", Input{Driving: true}},
		{"07-frozen", Input{Frozen: true}},
		{"08-extra-carriers-in-domain", Input{ExtraCarriers: true}},
		// 行9（域外孤儿只登记）明确未落码：观测面只有域内枚举、无
		// WorkloadOrphaned 事件码（ADR-0082 修订版 §1）。
		{"10-cancel-legacy-partial-issue", Input{CancelLegacy: true}},
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s %s\n", r.name, Adjudicate(r.in))
	}
	path := filepath.Join("testdata", "converge-verdicts.golden")
	if *verdictGoldenUpdate {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750)) //nolint:gosec // 测试产物目录
		require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/domain/delivery/converge -update")
	assert.Equal(t, string(want), b.String(),
		"converge verdict table drifted from golden (ADR-0082 §1: 8 judged rows + 2 non-rows are pinned)")
}

// ---- 裁决表接线守卫（映射/字段完备性——改真实映射即红）----

// 信号类 → 裁决表字段的映射逐条钉死：某类被误映射到别的行（如 gen 键置
// SpecMismatch）即红——这是"表即断言"在信号侧的执行面。
func TestVerdictSignalMapping(t *testing.T) {
	cases := []struct {
		name    string
		signals Signals
		want    Input
		verdict string
	}{
		{"spec", Signals{SignalSpec.Key("app-web"): "spec|image"}, Input{SpecMismatch: true}, VerdictConverge},
		{"absent", Signals{SignalAbsent.Key(""): "absent|app-web"}, Input{Absent: true}, VerdictConverge},
		{"gen", Signals{SignalGen.Key("app-web"): "gen|1|0"}, Input{GenDeviated: true}, VerdictConverge},
		{"rollout", Signals{SignalRollout.Key("app-web"): "rollout|paused"}, Input{RolloutStalled: true}, VerdictConverge},
		{"extra", Signals{SignalExtra.Key(""): "extra|app-ghost"}, Input{ExtraCarriers: true}, VerdictConverge},
		{"replay", Signals{SignalReplay.Key(""): "replay|dep-2"}, Input{CancelLegacy: true}, VerdictReplayBaseline},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SignalInput(tc.signals)
			// 逐字段相等即钉死映射（多置位/错置位都红）。
			assert.Equal(t, tc.want, got, "signal class must map to exactly its own verdict-table row")
			assert.Equal(t, tc.verdict, Adjudicate(got))
			assert.True(t, Actionable(Adjudicate(got)), "signal rows must be actionable")
		})
	}
}

// 枚举完备性：AllSignalClasses 的每一类都必须能被信号侧映射识别（新增枚举
// 而未在 SignalInput 登记 → 输入零值 → 非收敛行 → 红）。
func TestVerdictSignalClassTotality(t *testing.T) {
	classes := AllSignalClasses()
	require.Len(t, classes, 6, "六类信号：spec/absent/gen/rollout/extra/replay")
	seen := map[string]bool{}
	for _, c := range classes {
		key := c.Key("x")
		require.False(t, seen[key], "signal class key %q must be unique", key)
		seen[key] = true
		in := SignalInput(Signals{key: "v"})
		require.NotEqual(t, Input{}, in,
			"signal class %q has no verdict-table field mapping (register it in SignalInput)", c)
		assert.True(t, Actionable(Adjudicate(in)),
			"signal class %q must resolve to an actionable verdict", c)
	}
	// 未知信号类不伪造输入（不得凭未知键发起收敛）。
	assert.Equal(t, Input{}, SignalInput(Signals{"bogus|x": "v"}))
}

// 闸门侧映射（冻结/驱动权两行 + 优先序：冻结先于驱动权）。
func TestVerdictGateMapping(t *testing.T) {
	cases := []struct {
		name            string
		frozen, driving bool
		verdict         string
	}{
		{"frozen", true, false, VerdictSuspend},
		{"driving", false, true, VerdictSkip},
		{"clear", false, false, VerdictSteady},
		{"frozen-before-driving", true, true, VerdictSuspend},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.verdict, Adjudicate(GateInput(tc.frozen, tc.driving)))
			assert.False(t, Actionable(Adjudicate(GateInput(tc.frozen, tc.driving))),
				"gate rows never initiate a converge action")
		})
	}
}

// 字段 ⇄ 生产者双向保鲜：反射出的字段名集必须与 verdictFieldProducers 的
// 键集一一对应（加字段不登记即红，表项无字段即红）。
func TestVerdictFieldProducersComplete(t *testing.T) {
	typ := reflect.TypeOf(Input{})
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		fields[typ.Field(i).Name] = true
	}
	require.NotEmpty(t, fields)
	for name := range fields {
		_, ok := verdictFieldProducers[name]
		assert.True(t, ok, "verdict field %q has no registered producer (add it to verdictFieldProducers)", name)
	}
	for name := range verdictFieldProducers {
		assert.True(t, fields[name], "verdictFieldProducers entry %q has no matching input field", name)
	}
	assert.Len(t, verdictFieldProducers, len(fields), "verdict fields and producers must be one-to-one")
}

// 优先序组合钉死（闸门行 > 信号行；Cancel 遗留在多余载体之前——同拍多信号
// 时以显式信号裁决优先）：单行 golden 只钉孤立行，组合优先序在此白盒。
func TestVerdictPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		in      Input
		verdict string
	}{
		{"frozen-before-signal", Input{Frozen: true, SpecMismatch: true, Absent: true}, VerdictSuspend},
		{"driving-before-signal", Input{Driving: true, SpecMismatch: true, ExtraCarriers: true}, VerdictSkip},
		{"cancel-legacy-before-extra", Input{CancelLegacy: true, ExtraCarriers: true}, VerdictReplayBaseline},
		{"spec-first-of-signals", Input{SpecMismatch: true, Absent: true, GenDeviated: true, RolloutStalled: true}, VerdictConverge},
		{"all-rows-clear-is-steady", Input{}, VerdictSteady},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.verdict, Adjudicate(tc.in))
		})
	}
	// Actionable 的两面：行动值（converge/replay-baseline）与非行动值。
	for _, v := range []string{VerdictConverge, VerdictReplayBaseline} {
		assert.True(t, Actionable(v), "%s must be actionable", v)
	}
	for _, v := range []string{VerdictSteady, VerdictSkip, VerdictSuspend} {
		assert.False(t, Actionable(v), "%s must not be actionable", v)
	}
}

// ---- 信号类键与解析 ----

func TestSignalClassKeyAndParse(t *testing.T) {
	// 键构造：无逐载体维度 = 裸类名；有 suffix = 类名|suffix。
	assert.Equal(t, "spec", SignalSpec.Key(""))
	assert.Equal(t, "spec|app-web", SignalSpec.Key("app-web"))
	assert.Equal(t, "replay", SignalReplay.Key(""))
	// 解析与构造互逆；未知键 = 无类。
	for _, c := range AllSignalClasses() {
		got, ok := SignalClassOf(c.Key("suffix"))
		require.True(t, ok, "key of class %s must parse", c)
		assert.Equal(t, c, got)
	}
	_, ok := SignalClassOf("bogus|x")
	assert.False(t, ok, "unknown key must not resolve to a class")
}

// ---- 信号值字面与聚合签名 ----

func TestSignalValuesAndAggregate(t *testing.T) {
	// 信号值字面（行2/行8/行10 的值单源）。
	assert.Equal(t, "absent|app-web,app-db", AbsentSignal([]string{"app-web", "app-db"}))
	assert.Equal(t, "extra|app-ghost", ExtraSignal([]string{"app-ghost"}))
	assert.Equal(t, "replay|dep-2", ReplaySignal("dep-2"))

	// 聚合签名：空集空串；键序稳定（与遍历序无关）；值参与拼接。
	assert.Equal(t, "", Signals{}.Aggregate())
	a := Signals{"spec|b": "spec|image", "absent": "absent|a"}.Aggregate()
	b := Signals{"absent": "absent|a", "spec|b": "spec|image"}.Aggregate()
	assert.Equal(t, a, b, "aggregate must be iteration-order independent")
	assert.Equal(t, "absent=absent|a\x1fspec|b=spec|image", a)
}
