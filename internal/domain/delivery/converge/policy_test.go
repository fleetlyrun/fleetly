package converge

// 节流策略白盒（ADR-0082 §4/§5：冷却窗三径 + 失败阈值 + posture 闸门 +
// 触发/签名账三态）。纯白盒：零 fake、零编排依赖（import 可扫项）；时间
// 全作参数——构造时刻断言边界（窗满 ≥、阈值 N ≥）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---- 冷却窗判定（三径 + 边界）----

func TestCooldownOpen(t *testing.T) {
	enter := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	window := 10 * time.Minute
	base := CooldownInput{
		Cooling:        true,
		Until:          enter,
		Window:         window,
		FailSig:        FailureSignature{Sig: "spec|image mismatch", Present: true},
		PostureAtEnter: PostureAll,
		PostureNow:     PostureAll,
		Trigger:        Trigger{Sig: "spec|image mismatch"}, // 同签名有签名触发
	}

	t.Run("not-cooling-opens", func(t *testing.T) {
		// 未记账冷却：恒放行（其余字段无关）。
		in := base
		in.Cooling = false
		assert.True(t, CooldownOpen(in))
	})

	t.Run("within-window-blocks", func(t *testing.T) {
		in := base
		in.Now = enter.Add(window - time.Nanosecond) // 窗内最后一纳秒
		assert.False(t, CooldownOpen(in))
	})

	t.Run("window-elapsed-boundary-opens", func(t *testing.T) {
		// 径①窗满：边界取 ≥（窗满那一刻即放行）。
		in := base
		in.Now = enter.Add(window)
		assert.True(t, CooldownOpen(in))
	})

	t.Run("signature-change-opens", func(t *testing.T) {
		// 径②：有签名触发 × 与最近有签名失败异签名 → 放行（窗未满也放）。
		in := base
		in.Now = enter.Add(time.Minute)
		in.Trigger = Trigger{Sig: "spec|other mismatch"}
		assert.True(t, CooldownOpen(in))
	})

	t.Run("signature-absent-ledger-opens", func(t *testing.T) {
		// 径②：有签名触发 × 账本无签名（跨重启丢账形态）→ 放行。
		in := base
		in.Now = enter.Add(time.Minute)
		in.FailSig = FailureSignature{}
		assert.True(t, CooldownOpen(in))
	})

	t.Run("same-signature-blocks", func(t *testing.T) {
		// 径②反向：同签名有签名触发 → 不解除（窗内拦截）。
		in := base
		in.Now = enter.Add(time.Minute)
		assert.False(t, CooldownOpen(in))
	})

	t.Run("unsignaled-trigger-never-opens", func(t *testing.T) {
		// 径②反向：无签名触发（基线/隔离重放）既不匹配也不得解除冷却——
		// 即使账本无签名（不得凭空签名释放冷却，R02-M1）。
		in := base
		in.Now = enter.Add(time.Minute)
		in.Trigger = Trigger{}
		in.FailSig = FailureSignature{}
		assert.False(t, CooldownOpen(in))
	})

	t.Run("posture-switch-opens", func(t *testing.T) {
		// 径③：进窗快照 ≠ 当前 posture → 放行（all→detect→all 往返清零）。
		in := base
		in.Now = enter.Add(time.Minute)
		in.PostureNow = PostureDetect
		assert.True(t, CooldownOpen(in))
	})

	t.Run("posture-same-blocks", func(t *testing.T) {
		in := base
		in.Now = enter.Add(time.Minute)
		assert.False(t, CooldownOpen(in))
	})

	t.Run("multiple-paths-open", func(t *testing.T) {
		// 多径并存（窗未满 + 签名变化）：任一径即放行。
		in := base
		in.Now = enter.Add(time.Minute)
		in.Trigger = Trigger{Sig: "absent|app-web"}
		in.PostureNow = PostureDetect
		assert.True(t, CooldownOpen(in))
	})
}

// ---- 失败阈值裁决（计数推进/阈值边界/一次语义/签名账三态）----

func TestAdjudicateFail(t *testing.T) {
	t.Run("count-advances-below-threshold", func(t *testing.T) {
		// 计数推进：1 → 2（未达 N=3，不进窗、无沿）。
		v := AdjudicateFail(FailInput{Fails: 1, Threshold: 3, Trigger: Trigger{Sig: "s"}})
		assert.Equal(t, 2, v.Fails)
		assert.False(t, v.EnterCooldown)
		assert.False(t, v.EmitFailureEvent)
		assert.False(t, v.AlertEdge)
	})

	t.Run("threshold-boundary-enters-cooldown", func(t *testing.T) {
		// 阈值边界：新计数 = N 即进窗（≥ 语义；第 N 次失败熔断）。
		v := AdjudicateFail(FailInput{Fails: 2, Threshold: 3, Trigger: Trigger{Sig: "s"}})
		assert.Equal(t, 3, v.Fails)
		assert.True(t, v.EnterCooldown)
		assert.True(t, v.EmitFailureEvent)
		assert.True(t, v.AlertEdge)
		// 首败即熔断边界：N=1。
		v = AdjudicateFail(FailInput{Fails: 0, Threshold: 1, Trigger: Trigger{Sig: "s"}})
		assert.Equal(t, 1, v.Fails)
		assert.True(t, v.EnterCooldown)
	})

	t.Run("already-cooling-no-reentry", func(t *testing.T) {
		// 窗内再失败：不重进窗、不重发事件、不重起爆（一次语义第二道防线）。
		v := AdjudicateFail(FailInput{Fails: 5, Threshold: 3, AlreadyCooling: true, Trigger: Trigger{Sig: "s"}})
		assert.Equal(t, 6, v.Fails) // 计数照加（释放时统一清零）
		assert.False(t, v.EnterCooldown)
		assert.False(t, v.EmitFailureEvent)
		assert.False(t, v.AlertEdge)
	})

	t.Run("event-seen-gates-emit", func(t *testing.T) {
		// 一次语义防线一：本轮冷却已发 converge_failed → 不再发。
		v := AdjudicateFail(FailInput{Fails: 2, Threshold: 3, EventSeen: true, Trigger: Trigger{Sig: "s"}})
		assert.True(t, v.EnterCooldown)
		assert.False(t, v.EmitFailureEvent)
	})

	t.Run("alert-firing-gates-edge", func(t *testing.T) {
		// 告警沿只一次：已 firing（别的 App 先触发的平台级系统行）→ 不再起爆。
		v := AdjudicateFail(FailInput{Fails: 2, Threshold: 3, AlertFiring: true, Trigger: Trigger{Sig: "s"}})
		assert.True(t, v.EnterCooldown)
		assert.True(t, v.EmitFailureEvent)
		assert.False(t, v.AlertEdge)
	})

	t.Run("signature-change-restarts-count", func(t *testing.T) {
		// 签名变化重触发：与上次有签名失败异签名 = 新触发（计数重开为 1）。
		v := AdjudicateFail(FailInput{
			Fails: 2, Threshold: 3,
			FailSig: FailureSignature{Sig: "spec|a", Present: true},
			Trigger: Trigger{Sig: "spec|b"},
		})
		assert.Equal(t, 1, v.Fails)
		assert.Equal(t, FailureSignature{Sig: "spec|b", Present: true}, v.FailSig)
		assert.False(t, v.EnterCooldown)
	})

	t.Run("same-signature-continues-count", func(t *testing.T) {
		// 同签名：计数继续推进 + 签名账保持。
		v := AdjudicateFail(FailInput{
			Fails: 1, Threshold: 3,
			FailSig: FailureSignature{Sig: "spec|a", Present: true},
			Trigger: Trigger{Sig: "spec|a"},
		})
		assert.Equal(t, 2, v.Fails)
		assert.Equal(t, FailureSignature{Sig: "spec|a", Present: true}, v.FailSig)
	})

	t.Run("unsignaled-failure-keeps-signature-ledger", func(t *testing.T) {
		// 无签名失败（基线/隔离重放）：只计数、不抹签名账——否则一次无签名
		// 重放失败会把账清成「无签名」，下一次同签名失败被误判成「签名
		// 变化」而解除冷却（R02-M1）。
		v := AdjudicateFail(FailInput{
			Fails: 1, Threshold: 3,
			FailSig: FailureSignature{Sig: "spec|a", Present: true},
			Trigger: Trigger{},
		})
		assert.Equal(t, 2, v.Fails)
		assert.Equal(t, FailureSignature{Sig: "spec|a", Present: true}, v.FailSig, "unsignaled failure must not wipe the signature ledger")
	})

	t.Run("signaled-failure-updates-ledger", func(t *testing.T) {
		// 有签名失败：签名账更新为本次签名（账本无签名时同样落账）。
		v := AdjudicateFail(FailInput{Fails: 0, Threshold: 3, Trigger: Trigger{Sig: "spec|a"}})
		assert.Equal(t, FailureSignature{Sig: "spec|a", Present: true}, v.FailSig)
	})
}

// ---- posture 闸门（ADR-0082 §5）----

func TestDetect(t *testing.T) {
	assert.False(t, Detect(PostureAll), "all is the default converging posture")
	assert.True(t, Detect(PostureDetect), "detect blocks runtime writes")
	// 只认 detect 字面：空串按 all 处置（配置值域执法在 engine
	// ParseConvergePosture fail-fast，裸判定不做第二套值域）。
	assert.False(t, Detect(""))
	assert.False(t, Detect("bogus"))
}

// ---- 触发面（R02-M1 词面）----

func TestTriggerSignaled(t *testing.T) {
	assert.False(t, Trigger{}.Signaled())
	assert.False(t, Trigger{Sig: ""}.Signaled())
	assert.True(t, Trigger{Sig: "spec|x"}.Signaled())
}
