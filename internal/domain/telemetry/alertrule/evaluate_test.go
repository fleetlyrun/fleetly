package alertrule

// evaluate_test.go 是评估决策面的白盒测试（ADR-0087 P3a 首单元：迁移表
// 全行覆盖——越限/回稳/pending 语义/无样本不动窗；零 fake/编排依赖
// import，纯函数直测）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evalBase 是迁移表测试的基准时刻（固定时钟——时间作参数传入，禁
// time.Now 的域纪律在测试同形）。
var evalBase = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

// pendingAt 构造落库起窗点文本（state.FormatTime 同口径：UTC RFC3339）。
func pendingAt(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// TestEvaluateTransitionTable 钉迁移表全行：给定行态（State × PendingSince）
// 与读数（sampled × value 相对 Threshold），裁决的观测/窗操作/迁移目标
// 逐行对表。语义源 = ADR-0041 + B2a W1b（pending_since 直接改列、重启续窗）
// + 修复批 F3（无样本不动窗）。
func TestEvaluateTransitionTable(t *testing.T) {
	inWindow := pendingAt(evalBase.Add(-10 * time.Second))   // 窗内（for 30s）
	fullWindow := pendingAt(evalBase.Add(-30 * time.Second)) // 窗满（elapsed = for）
	cases := []struct {
		name string
		rule Rule
		// 读数面
		value   float64
		sampled bool
		want    Verdict
	}{
		{
			name:    "no sample leaves the pending window untouched even while breaching",
			rule:    Rule{Threshold: 100, ForSecs: 30, State: StateOK, PendingSince: inWindow},
			value:   500, // 读数在，但 sampled=false（聚合面键缺席）
			sampled: false,
			want:    Verdict{},
		},
		{
			name:    "breach first sight starts the window without transitioning",
			rule:    Rule{Threshold: 100, ForSecs: 30, State: StateOK},
			value:   200,
			sampled: true,
			want:    Verdict{Observe: true, BeginPending: evalBase},
		},
		{
			name:  "breach with a corrupt pending column starts over as first sight",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateOK, PendingSince: "not-a-timestamp"},
			value: 200, sampled: true,
			want: Verdict{Observe: true, BeginPending: evalBase},
		},
		{
			name:  "breach while firing with no window starts a new window (no repeated fire)",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateFiring},
			value: 200, sampled: true,
			want: Verdict{Observe: true, BeginPending: evalBase},
		},
		{
			name:  "breach inside the window holds without resetting the start",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateOK, PendingSince: inWindow},
			value: 200, sampled: true,
			want: Verdict{Observe: true},
		},
		{
			name:  "breach at exactly the for-window boundary fires (elapsed >= for)",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateOK, PendingSince: fullWindow},
			value: 200, sampled: true,
			want: Verdict{Observe: true, Transition: StateFiring},
		},
		{
			name:  "breach past the window while already firing stays silent (edge semantics)",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateFiring, PendingSince: fullWindow},
			value: 200, sampled: true,
			want: Verdict{Observe: true},
		},
		{
			name:  "fallback clears the pending window without transitioning from ok",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateOK, PendingSince: inWindow},
			value: 50, sampled: true,
			want: Verdict{Observe: true, ClearPending: true},
		},
		{
			name:  "fallback resolves a firing rule",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateFiring},
			value: 50, sampled: true,
			want: Verdict{Observe: true, Transition: StateOK},
		},
		{
			name:  "fallback with a stale window on a firing rule clears and resolves in one tick",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateFiring, PendingSince: inWindow},
			value: 50, sampled: true,
			want: Verdict{Observe: true, ClearPending: true, Transition: StateOK},
		},
		{
			name:  "steady ok tick only observes",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateOK},
			value: 50, sampled: true,
			want: Verdict{Observe: true},
		},
		{
			name:  "value equal to the threshold is not a breach (strict >)",
			rule:  Rule{Threshold: 100, ForSecs: 30, State: StateOK, PendingSince: inWindow},
			value: 100, sampled: true,
			want: Verdict{Observe: true, ClearPending: true},
		},
		{
			name: "clock skew before the window start holds (negative elapsed counts as unfinished)",
			rule: Rule{Threshold: 100, ForSecs: 30, State: StateOK,
				PendingSince: pendingAt(evalBase.Add(10 * time.Second))},
			value: 200, sampled: true,
			want: Verdict{Observe: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rule.Evaluate(tc.value, tc.sampled, evalBase)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestEvaluateZeroForWindow 钉 ForSecs=0 的两拍语义：首拍越限只起窗（起窗
// 拍不迁移——BeginPending 与 Transition 互斥），第二拍越限即迁移 firing。
func TestEvaluateZeroForWindow(t *testing.T) {
	rule := Rule{Threshold: 100, ForSecs: 0, State: StateOK}

	first := rule.Evaluate(200, true, evalBase)
	assert.Equal(t, Verdict{Observe: true, BeginPending: evalBase}, first,
		"the window-starting tick must not transition in the same beat")

	second := Rule{Threshold: 100, ForSecs: 0, State: StateOK, PendingSince: pendingAt(evalBase)}
	assert.Equal(t, Verdict{Observe: true, Transition: StateFiring},
		second.Evaluate(200, true, evalBase.Add(time.Second)),
		"any elapsed time satisfies a zero for-window")
}

// TestEvaluateDoesNotMutateRule 钉决策纯度：Evaluate 不写 receiver（行写
// 执行留 engine——ADR-0087 决策 3 状态机搬家禁令）。
func TestEvaluateDoesNotMutateRule(t *testing.T) {
	rule := Rule{ID: "r1", Threshold: 100, ForSecs: 30, State: StateOK,
		PendingSince: pendingAt(evalBase.Add(-10 * time.Second))}
	before := rule
	_ = rule.Evaluate(200, true, evalBase)
	_ = rule.Evaluate(50, true, evalBase)
	assert.Equal(t, before, rule, "the verdict must leave the row untouched")
}

// TestEvaluateBeginPendingExclusivity 钉 Verdict 不变量：全表扫描中起窗
// 裁决从不与清窗/迁移同拍（执行面据此可安全串行落库）。
func TestEvaluateBeginPendingExclusivity(t *testing.T) {
	states := []string{StateOK, StateFiring, ""}
	pendings := []string{"", pendingAt(evalBase.Add(-60 * time.Second)), "corrupt"}
	values := []float64{0, 100, 250}
	for _, st := range states {
		for _, ps := range pendings {
			for _, v := range values {
				r := Rule{Threshold: 100, ForSecs: 30, State: st, PendingSince: ps}
				verdict := r.Evaluate(v, true, evalBase)
				if !verdict.BeginPending.IsZero() {
					require.False(t, verdict.ClearPending,
						"state=%q pending=%q value=%v: begin-pending must not co-occur with clear-pending", st, ps, v)
					require.Empty(t, verdict.Transition,
						"state=%q pending=%q value=%v: begin-pending must not co-occur with a transition", st, ps, v)
				}
				if verdict.Transition != "" {
					require.Contains(t, []string{StateFiring, StateOK}, verdict.Transition,
						"state=%q pending=%q value=%v: illegal transition target %q", st, ps, v, verdict.Transition)
				}
			}
		}
	}
}

// TestParsePendingSince 直测起窗点解析（空串/坏值 = nil——坏值按首见处理）。
func TestParsePendingSince(t *testing.T) {
	require.Nil(t, parsePendingSince(""))
	require.Nil(t, parsePendingSince("not-a-timestamp"))
	at := parsePendingSince(evalBase.Format(time.RFC3339))
	require.NotNil(t, at)
	assert.True(t, at.Equal(evalBase))
}
