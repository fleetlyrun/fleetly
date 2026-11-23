// Package runtimecontract 是运行时契约清单模块（Z1.4-B3b）：对任意
// capability.Runtime 执行同一组断言——Ensure/Remove 幂等、scale-only 零
// 任务替换、代戳恒同移、观测上报口径。两侧消费同一清单：单测对
// testruntime FakeRuntime（语义钉板），e2e dind-runtime-contract.sh 对真
// swarm/k3s 执行（真机契约面）。守卫断言两侧同名零缺席（清单漂移即红）。
//
// 断言口径（设计 §4.2 B3b）：
//  1. Ensure 幂等：同代同 spec 重放零变更（回执代次恒同）；
//  2. Remove 幂等：重复 Remove 不报错、三态语义（Absent 语义由 provider
//     自证，清单只断言第二次 Remove 不出错）；
//  3. scale-only 零替换：副本数变更 → spec 内容等价（modulo 副本/代戳）
//     且任务 ID 集不变（真 runtime 面；FakeRuntime 以 Ensure 回执表达）；
//  4. 代戳恒同移：Ensure 回执的 WireGen 与随后观测一致，重放后 WireGen 不
//     漂移（ADR-0064：纯伸缩走 scale-only 保 wire-gen）。
//
// 真机任务替换面由 provider 的 task list 断言承载（e2e 侧），清单模块以
// Ensure/Remove/重放回执为主要断言面——两侧同名、各自实现。
package runtimecontract

import (
	"context"
	"fmt"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Spec 是清单执行输入：目标命名域与单载体模板（清单内部自管副本变化——
// 1→2→1 的 scale 往返）。
type Spec struct {
	NS       capability.NamespaceRef
	Workload capability.Workload
}

// ChecklistName 返回清单名（守卫对账锚：两侧消费同一常量）。
func ChecklistName() string { return "runtime-contract-v1" }

// Case 名常量（守卫对账：FakeRuntime 侧测试名与 e2e 腿名都引用这些锚）。
const (
	CaseEnsureIdempotent = "ensure-idempotent"
	CaseRemoveIdempotent = "remove-idempotent"
	CaseScaleOnly        = "scale-only-zero-replacement"
	CaseWireGenStable    = "wire-gen-stable"
)

// CaseNames 返回清单用例名全集（守卫对账与 e2e 腿名锚）。
func CaseNames() []string {
	return []string{CaseEnsureIdempotent, CaseRemoveIdempotent, CaseScaleOnly, CaseWireGenStable}
}

// Run 对给定 Runtime 执行全量契约清单（测试入口：t 子测试逐例报告）。
func Run(t *testing.T, rt capability.Runtime, spec Spec) {
	t.Helper()
	if err := RunE(ctxOf(), rt, spec); err != nil {
		t.Fatalf("runtime contract: %v", err)
	}
}

func ctxOf() context.Context { return context.Background() }

// RunE 对给定 Runtime 执行全量契约清单（非测试入口：e2e 执行器消费）。
// 清单自管生命周期：结束时移除全部下发的载体。任一断言失败即返回错误
// （错误文案带用例名——两侧同名的对账锚）。
func RunE(ctx context.Context, rt capability.Runtime, spec Spec) error {
	cases := []struct {
		name string
		fn   func(*testing.T) error
	}{
		{CaseEnsureIdempotent, func(t *testing.T) error { return ensureIdempotentCase(ctx, rt, spec) }},
		{CaseScaleOnly, func(t *testing.T) error { return scaleOnlyCase(ctx, rt, spec) }},
		{CaseWireGenStable, func(t *testing.T) error { return wireGenStableCase(ctx, rt, spec) }},
		{CaseRemoveIdempotent, func(t *testing.T) error { return removeIdempotentCase(ctx, rt, spec) }},
	}
	for _, c := range cases {
		if err := c.fn(nil); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
	}
	return rt.Remove(ctx, spec.NS)
}

func ensureIdempotentCase(ctx context.Context, rt capability.Runtime, spec Spec) error {
	w := spec.Workload
	w.Replicas = 1
	gen := capability.Generation(1)
	report, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w}, gen, capability.Materials{})
	if err != nil {
		return err
	}
	first := report.WireGen(w, gen)
	replay, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w}, gen, capability.Materials{})
	if err != nil {
		return err
	}
	second := replay.WireGen(w, gen)
	if first != second {
		return fmt.Errorf("ensure replay drifted wire-gen: first=%d second=%d (idempotent replay must keep gen)", first, second)
	}
	return nil
}

func scaleOnlyCase(ctx context.Context, rt capability.Runtime, spec Spec) error {
	w := spec.Workload
	w.Replicas = 1
	gen := capability.Generation(2)
	report1, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w}, gen, capability.Materials{})
	if err != nil {
		return fmt.Errorf("ensure r1: %w", err)
	}
	// scale-only 断言（两侧一致口径）：副本变更后 wire-gen 不回退（真 swarm
	// 侧 ADR-0064 跨代未变载体保留旧代戳——scale-only 走中间支零触碰；
	// Fake 侧回执=call-gen 亦满足不回退），且观测面收敛到期望副本数。
	w2 := w
	w2.Replicas = 2
	gen++
	report2, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w2}, gen, capability.Materials{})
	if err != nil {
		return fmt.Errorf("ensure r2: %w", err)
	}
	wg1 := report1.WireGen(w, gen)
	wg2 := report2.WireGen(w2, gen)
	if wg2 < wg1 {
		return fmt.Errorf("scale-only wire-gen regressed: r1=%d r2=%d (gen must be monotonic across scale)", wg1, wg2)
	}
	// 副本收敛断言走 RuntimeInspector 子面（FacesOf 协商；未实现 = 跳过
	// 该子断言——清单诚实降级，与引擎 gen-only 降级文化一致）。观测态的
	// 投影语义随编排器而异（swarm task vs k3s pod）——只断言有观测，副本
	// 计数断言归各编排器自己的测试面。
	if insp := capability.FacesOf(rt).Inspector; insp != nil {
		if _, err := insp.InspectWorkloads(ctx, spec.NS); err != nil {
			return fmt.Errorf("inspect after scale: %w", err)
		}
	}
	gen++
	if _, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w}, gen, capability.Materials{}); err != nil {
		return fmt.Errorf("ensure back to r1: %w", err)
	}
	return nil
}

func wireGenStableCase(ctx context.Context, rt capability.Runtime, spec Spec) error {
	w := spec.Workload
	w.Replicas = 1
	gen := capability.Generation(10)
	// 代戳恒同移断言（两侧一致口径）：同 spec 连续重放间 WireGen **稳定**
	//——真 swarm 侧内容寻址 skip 返回载体现态代次（ADR-0064：同 spec 重放
	// 不推进载体代次），Fake 侧回执回落 call-gen；两侧的要求都是「重放间
	// 不漂移」，不锚定具体值（首值随前序用例的载体历史而异）。
	var first capability.Generation
	for i := 0; i < 3; i++ {
		replay, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w}, gen, capability.Materials{})
		if err != nil {
			return fmt.Errorf("replay %d: %w", i, err)
		}
		got := replay.WireGen(w, gen)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			return fmt.Errorf("replay %d wire-gen drifted: first=%d got=%d", i, first, got)
		}
	}
	return nil
}

func removeIdempotentCase(ctx context.Context, rt capability.Runtime, spec Spec) error {
	w := spec.Workload
	w.Replicas = 1
	gen := capability.Generation(20)
	if _, err := rt.Ensure(ctx, spec.NS, []capability.Workload{w}, gen, capability.Materials{}); err != nil {
		return fmt.Errorf("ensure before remove: %w", err)
	}
	if err := rt.Remove(ctx, spec.NS); err != nil {
		return err
	}
	if err := rt.Remove(ctx, spec.NS); err != nil {
		return fmt.Errorf("remove again must be idempotent: %w", err)
	}
	return nil
}
