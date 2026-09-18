package engine

// 发布状态机穷举测试（验收标准 2：转换矩阵含非法转换拒绝）：
// 全词表 × 全词表逐一断言合法集合，表外转移一律拒绝；终态无出边。

import (
	"errors"
	"testing"
)

func TestTransitionMatrixExhaustive(t *testing.T) {
	all := AllPhases()
	// 合法转移全集（release-semantics §2.3：building 直通由 preparing→
	// releasing 边承载；observing 不可取消）。
	legal := map[Phase]map[Phase]bool{
		PhaseQueued:    {PhasePreparing: true, PhaseFailed: true, PhaseCancelled: true},
		PhasePreparing: {PhaseBuilding: true, PhaseReleasing: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseBuilding:  {PhaseReleasing: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseReleasing: {PhaseObserving: true, PhaseFailed: true, PhaseCancelled: true},
		PhaseObserving: {PhaseSucceeded: true, PhaseFailed: true},
		PhaseSucceeded: {},
		PhaseFailed:    {},
		PhaseCancelled: {},
	}
	for _, from := range all {
		for _, to := range all {
			want := legal[from][to]
			if got := CanTransition(from, to); got != want {
				t.Fatalf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
			err := Transition(from, to)
			if want && err != nil {
				t.Fatalf("Transition(%s, %s) error = %v", from, to, err)
			}
			if !want {
				var illegal *ErrIllegalTransition
				if !errors.As(err, &illegal) {
					t.Fatalf("Transition(%s, %s) = %v, want *ErrIllegalTransition", from, to, err)
				}
			}
		}
	}
}

func TestLegalTransitionsMatchesTable(t *testing.T) {
	for _, from := range AllPhases() {
		got := LegalTransitions(from)
		// 表驱动实现的一致性：LegalTransitions 与 CanTransition 一一对应。
		for _, to := range AllPhases() {
			member := false
			for _, g := range got {
				if g == to {
					member = true
				}
			}
			if member != CanTransition(from, to) {
				t.Fatalf("LegalTransitions(%s) inconsistent for %s", from, to)
			}
		}
	}
}

func TestPhaseVocabulary(t *testing.T) {
	for _, p := range AllPhases() {
		if !p.Valid() {
			t.Fatalf("phase %s should be valid", p)
		}
	}
	if Phase("bogus").Valid() {
		t.Fatal("bogus phase should be invalid")
	}
	for _, terminal := range []Phase{PhaseSucceeded, PhaseFailed, PhaseCancelled} {
		if !terminal.Terminal() {
			t.Fatalf("%s should be terminal", terminal)
		}
	}
	for _, running := range []Phase{PhaseQueued, PhasePreparing, PhaseBuilding, PhaseReleasing, PhaseObserving} {
		if running.Terminal() {
			t.Fatalf("%s should not be terminal", running)
		}
	}
	if Transition(PhaseQueued, Phase("bogus")) == nil {
		t.Fatal("transition to unknown phase should be illegal")
	}
}
