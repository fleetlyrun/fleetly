// unexplained_guard_test.go 是完备性承诺的守卫承载（设计 §2.1）：对
// 「失败态输入全集」形态断言兜底永不空——观测面可读且存在失败态观测时，
// Evaluate 产出永不为空；没有任何解释性规则命中时，兜底条目以最小陈述
// 形态（观测态 + 最近相关事件 + 建议动作）覆盖每个未解释 workload。
package diagnosis

import (
	"fmt"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failureUniverse 枚举失败态输入全集：失败态 × 退出码（含 nil/0）× reason
// 原文（含 OOM/拉取/健康/无语义四类 + 空）× 窗口终态次数（阈值上下）×
// 事件面三态。
func failureUniverse() []Facts {
	states := []capability.WorkloadState{
		capability.WorkloadFailed,
		capability.WorkloadDegraded,
		capability.WorkloadOrphaned,
	}
	exitCodes := []*int{nil, intPtr(0), intPtr(1), intPtr(ExitNotExecutable), intPtr(ExitCommandNotFound), intPtr(ExitSIGKILL), intPtr(ExitSIGTERM)}
	reasons := []string{
		"",
		"OOMKilled",
		"no such image: registry.example.com/acme/app",
		"health check failed: context deadline exceeded",
		"worker exited unexpectedly",
	}
	restarts := []int{0, 1, CrashLoopThreshold - 1, CrashLoopThreshold, 9}
	eventFaces := []Option[[]EventFact]{
		None[[]EventFact](),
		Some([]EventFact{}),
		Some([]EventFact{{Seq: 41, Code: "workload.container_exited", At: testNow, Detail: "exit_code=2"}}),
	}

	var out []Facts
	for _, s := range states {
		for _, code := range exitCodes {
			for _, reason := range reasons {
				for _, rc := range restarts {
					for _, ev := range eventFaces {
						out = append(out, Facts{
							Observations: someObs(WorkloadObservationFact{
								WorkloadID:   "universe-1",
								Process:      "web",
								State:        s,
								ExitCode:     code,
								Reason:       reason,
								RestartCount: rc,
								LastExitAt:   testNow.Add(-time.Minute),
							}),
							Events: ev,
							Now:    testNow,
						})
					}
				}
			}
		}
	}
	return out
}

// TestUnexplainedCompleteness：失败态输入全集下兜底永不空（unknown 永远
// 不是全部答案）。
func TestUnexplainedCompleteness(t *testing.T) {
	universe := failureUniverse()
	require.Greater(t, len(universe), 1000, "failure universe enumeration is the guard's teeth; do not shrink it casually")

	for _, facts := range universe {
		got := Evaluate(facts)
		require.NotEmpty(t, got,
			"completeness promise broken: failing input (%s / exit=%v / reason=%q / restarts=%d / events=%s) produced no finding at all",
			facts.Observations.value[0].State, derefInt(facts.Observations.value[0].ExitCode),
			facts.Observations.value[0].Reason, facts.Observations.value[0].RestartCount,
			eventFaceShape(facts.Events))

		// 无解释性规则命中时：每个失败态 workload 恰有一条兜底，且是最小
		// 陈述形态（观测态 + 最近相关事件 + 建议动作）。
		explained := RunExplainers(facts)
		if len(explained) == 0 {
			unexplained := ruleUnexplained.Check(facts)
			require.Len(t, unexplained, 1, "uncovered failing workload must yield exactly one fallback finding")
			f := unexplained[0]
			assert.Equal(t, "F_DIAGNOSIS_UNEXPLAINED", f.ID())
			assert.Equal(t, "universe-1", f.WorkloadID)
			assert.NotEmpty(t, f.Params["state"], "fallback must state the observed state")
			assert.NotEmpty(t, f.Fix.ConsolePath, "fallback must suggest an action")
			if events, ok := facts.Events.Value(); ok && len(events) > 0 {
				assert.Equal(t, "workload.container_exited", f.Params["recent_event"], "readable non-empty events must be cited")
				assert.Contains(t, f.Evidence, Evidence{Kind: EvidenceEvent, Ref: "seq:41"})
			} else {
				assert.NotContains(t, f.Params, "recent_event", "no readable events means no event claim (None ≠ empty guessing)")
			}
		}

		// 有解释性规则命中时：兜底不得对已解释 workload 叠加。
		covered := map[string]bool{}
		for _, f := range explained {
			covered[f.WorkloadID] = true
		}
		for _, f := range ruleUnexplained.Check(facts) {
			assert.False(t, covered[f.WorkloadID], "fallback stacked on explained workload %s", f.WorkloadID)
		}
	}
}

// eventFaceShape 是失败信息的可读形态。
func eventFaceShape(o Option[[]EventFact]) string {
	if o.IsNone() {
		return "none"
	}
	v, _ := o.Value()
	return fmt.Sprintf("some(%d)", len(v))
}
