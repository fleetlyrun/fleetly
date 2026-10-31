// diagnosis_test.go 是规则白盒测试（零 I/O、纯函数直测）。
package diagnosis

import (
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/finding"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func intPtr(i int) *int { return &i }

func someObs(obs ...WorkloadObservationFact) Option[[]WorkloadObservationFact] {
	return Some(obs)
}

func idsOf(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.ID())
	}
	return out
}

// TestRulesInvariants：规则清单静态收口——id 唯一、Check 非空、Rules =
// explainers + unexplained（兜底居末）、全部条目实例引用在册 id（instantiate
// panic 路径另测）。
func TestRulesInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules {
		assert.NotEmpty(t, r.ID)
		assert.NotNil(t, r.Check, "rule %s has no Check", r.ID)
		assert.False(t, seen[r.ID], "duplicate rule id %s", r.ID)
		seen[r.ID] = true
	}
	require.Len(t, Rules, len(explainers)+1, "Rules must be explainers + unexplained")
	assert.Equal(t, "unexplained", Rules[len(Rules)-1].ID, "unexplained must close the rule list")
	for i, r := range explainers {
		assert.Equal(t, r.ID, Rules[i].ID)
	}
}

// TestCrashLoopRule：终态计数 + 终止态推断，不信单一 waiting reason。
func TestCrashLoopRule(t *testing.T) {
	looping := WorkloadObservationFact{
		WorkloadID: "w1", Process: "web", State: capability.WorkloadFailed,
		ExitCode: intPtr(1), Reason: "app exited with error",
		RestartCount: CrashLoopThreshold, LastExitAt: testNow.Add(-time.Minute),
	}

	t.Run("threshold met and still terminated", func(t *testing.T) {
		got := ruleCrashLoop.Check(Facts{Observations: someObs(looping)})
		require.Len(t, got, 1)
		assert.Equal(t, "F_WORKLOAD_CRASH_LOOP", got[0].ID())
		assert.Equal(t, finding.FaultClassApp, got[0].FaultClass)
		assert.Equal(t, "1", got[0].Params["exit_code"])
		assert.Equal(t, "3", got[0].Params["restart_count"])
		assert.Equal(t, "web", got[0].Fix.Params["process"], "restart verb prefills process")
		assert.Equal(t, "restart", got[0].Fix.Verb)
	})

	t.Run("one below threshold is silent", func(t *testing.T) {
		o := looping
		o.RestartCount = CrashLoopThreshold - 1
		assert.Empty(t, ruleCrashLoop.Check(Facts{Observations: someObs(o)}))
	})

	t.Run("recovered loop is silent regardless of count", func(t *testing.T) {
		o := looping
		o.RestartCount = 99
		o.State = capability.WorkloadRunning
		assert.Empty(t, ruleCrashLoop.Check(Facts{Observations: someObs(o)}))
	})

	t.Run("degraded counts as still terminated", func(t *testing.T) {
		o := looping
		o.State = capability.WorkloadDegraded
		assert.Len(t, ruleCrashLoop.Check(Facts{Observations: someObs(o)}), 1)
	})

	t.Run("stopped now is not a loop", func(t *testing.T) {
		o := looping
		o.State = capability.WorkloadStopped
		assert.Empty(t, ruleCrashLoop.Check(Facts{Observations: someObs(o)}))
	})

	t.Run("exit 126 and 127 take the bad-command variant", func(t *testing.T) {
		for _, code := range []int{ExitNotExecutable, ExitCommandNotFound} {
			o := looping
			o.ExitCode = intPtr(code)
			got := ruleCrashLoop.Check(Facts{Observations: someObs(o)})
			require.Len(t, got, 1, "exit %d", code)
			assert.Equal(t, "F_WORKLOAD_CRASH_LOOP_BAD_COMMAND", got[0].ID())
			assert.Equal(t, finding.FaultClassDeclaration, got[0].FaultClass)
			assert.NotEmpty(t, got[0].Params["exit_code_meaning"])
			assert.Empty(t, got[0].Fix.Verb, "declaration fix goes through the deploy sheet, not a direct verb")
		}
	})

	t.Run("sigkill and sigterm meanings surface in params", func(t *testing.T) {
		for _, code := range []int{ExitSIGKILL, ExitSIGTERM} {
			o := looping
			o.ExitCode = intPtr(code)
			got := ruleCrashLoop.Check(Facts{Observations: someObs(o)})
			require.Len(t, got, 1, "exit %d", code)
			assert.Equal(t, "F_WORKLOAD_CRASH_LOOP", got[0].ID())
			assert.Contains(t, got[0].Params["exit_code_meaning"], "SIG")
		}
	})

	t.Run("face none is silent", func(t *testing.T) {
		assert.Empty(t, ruleCrashLoop.Check(Facts{}))
	})

	t.Run("waiting reason alone without terminal count is not a loop", func(t *testing.T) {
		// 「不信单一 waiting reason」：reason 像崩溃，但窗口内终态次数不足 → 沉默。
		o := WorkloadObservationFact{
			WorkloadID: "w9", Process: "web", State: capability.WorkloadFailed,
			Reason: "task: non-zero exit (1)", RestartCount: 1,
		}
		assert.Empty(t, ruleCrashLoop.Check(Facts{Observations: someObs(o)}))
	})
}

// TestOOMRule：两 variant 按 OOMKilled 标记分档；恢复了的 OOM 不报。
func TestOOMRule(t *testing.T) {
	base := WorkloadObservationFact{
		WorkloadID: "w2", Process: "worker", State: capability.WorkloadFailed,
		ExitCode: intPtr(ExitSIGKILL), Reason: "task: non-zero exit (137)",
		RestartCount: 1, LastExitAt: testNow.Add(-time.Minute),
	}

	t.Run("137 without marker is the no-limit variant", func(t *testing.T) {
		got := ruleOOM.Check(Facts{Observations: someObs(base)})
		require.Len(t, got, 1)
		assert.Equal(t, "F_WORKLOAD_OOM_WITHOUT_LIMIT", got[0].ID())
	})

	t.Run("OOMKilled marker is the with-limit variant", func(t *testing.T) {
		o := base
		o.Reason = "OOMKilled"
		got := ruleOOM.Check(Facts{Observations: someObs(o)})
		require.Len(t, got, 1)
		assert.Equal(t, "F_WORKLOAD_OOM_WITH_LIMIT", got[0].ID())
	})

	t.Run("out of memory text without marker is the no-limit variant", func(t *testing.T) {
		o := base
		o.ExitCode = nil
		o.Reason = "out of memory"
		got := ruleOOM.Check(Facts{Observations: someObs(o)})
		require.Len(t, got, 1)
		assert.Equal(t, "F_WORKLOAD_OOM_WITHOUT_LIMIT", got[0].ID())
	})

	t.Run("recovered oom is silent", func(t *testing.T) {
		o := base
		o.State = capability.WorkloadRunning
		assert.Empty(t, ruleOOM.Check(Facts{Observations: someObs(o)}))
	})

	t.Run("other exits without oom text are silent", func(t *testing.T) {
		o := base
		o.ExitCode = intPtr(1)
		o.Reason = "app exited with error"
		assert.Empty(t, ruleOOM.Check(Facts{Observations: someObs(o)}))
	})

	t.Run("face none is silent", func(t *testing.T) {
		assert.Empty(t, ruleOOM.Check(Facts{}))
	})
}

// TestHealthCheckRule：reason 健康探针失败原文 + 仍在失败态。
func TestHealthCheckRule(t *testing.T) {
	failing := WorkloadObservationFact{
		WorkloadID: "w3", Process: "web", State: capability.WorkloadFailed,
		Reason: "health check failed: context deadline exceeded", RestartCount: 1,
	}
	got := ruleHealthCheck.Check(Facts{Observations: someObs(failing)})
	require.Len(t, got, 1)
	assert.Equal(t, "F_WORKLOAD_HEALTH_CHECK_FAILING", got[0].ID())
	assert.Equal(t, finding.FaultClassApp, got[0].FaultClass)

	assert.Empty(t, ruleHealthCheck.Check(Facts{Observations: someObs(WorkloadObservationFact{
		WorkloadID: "w3b", State: capability.WorkloadFailed, Reason: "app exited with error",
	})}), "no health text, no finding")
	assert.Empty(t, ruleHealthCheck.Check(Facts{Observations: someObs(WorkloadObservationFact{
		WorkloadID: "w3c", State: capability.WorkloadRunning, Reason: "health check failed",
	})}), "recovered probe, no finding")
	assert.Empty(t, ruleHealthCheck.Check(Facts{}), "face none is silent")
}

// TestImagePullRule：拉取失败原文识别（external 归属）。
func TestImagePullRule(t *testing.T) {
	for _, reason := range []string{
		"No such image: registry.example.com/acme/app:latest",
		"manifest unknown",
		"image pull backoff",
		"pull access denied for acme/app",
		"failed to pull and push image",
		"pull rate limit exceeded",
	} {
		got := ruleImagePull.Check(Facts{Observations: someObs(WorkloadObservationFact{
			WorkloadID: "w4", State: capability.WorkloadFailed, Reason: reason,
		})})
		require.Len(t, got, 1, "reason %q", reason)
		assert.Equal(t, "F_WORKLOAD_IMAGE_PULL_FAILING", got[0].ID())
		assert.Equal(t, finding.FaultClassExternal, got[0].FaultClass)
	}
	assert.Empty(t, ruleImagePull.Check(Facts{Observations: someObs(WorkloadObservationFact{
		WorkloadID: "w4b", State: capability.WorkloadFailed, Reason: "app exited with error",
	})}))
	assert.Empty(t, ruleImagePull.Check(Facts{}))
}

// TestStoppedRule：看门狗语义解释化（advice 档 + scale 预填）。
func TestStoppedRule(t *testing.T) {
	stopped := WorkloadObservationFact{
		WorkloadID: "w5", Process: "sidecar", State: capability.WorkloadStopped,
		ExitCode: intPtr(ExitSIGTERM), Reason: "received SIGTERM",
		LastExitAt: testNow.Add(-20 * time.Minute),
	}
	got := ruleStopped.Check(Facts{Observations: someObs(stopped), Now: testNow})
	require.Len(t, got, 1)
	assert.Equal(t, "F_WORKLOAD_STOPPED_EXPLAINED", got[0].ID())
	assert.Equal(t, finding.SeverityAdvice, got[0].Severity())
	assert.Equal(t, finding.FaultClassDeclaration, got[0].FaultClass)
	assert.Equal(t, "scale", got[0].Fix.Verb)
	assert.Equal(t, "1", got[0].Fix.Params["replicas"])
	assert.Equal(t, "sidecar", got[0].Fix.Params["process"])
	assert.Contains(t, got[0].Params["exit_code_meaning"], "SIGTERM")

	assert.Empty(t, ruleStopped.Check(Facts{Observations: someObs(WorkloadObservationFact{
		WorkloadID: "w5b", State: capability.WorkloadRunning,
	})}))
	assert.Empty(t, ruleStopped.Check(Facts{}))
}

// TestUnexplainedGaps：完备性兜底的最小陈述形态。
func TestUnexplainedGaps(t *testing.T) {
	failing := WorkloadObservationFact{
		WorkloadID: "w6", Process: "cron", State: capability.WorkloadFailed,
		ExitCode: intPtr(2), Reason: "worker exited unexpectedly", RestartCount: 1,
	}
	events := []EventFact{
		{Seq: 40, Code: "workload.container_exited", At: testNow.Add(-30 * time.Minute)},
		{Seq: 41, Code: "workload.container_exited", At: testNow.Add(-15 * time.Minute), Detail: "exit_code=2"},
	}

	t.Run("uncovered failure gets the minimal statement with latest event", func(t *testing.T) {
		got := unexplainedGaps(Facts{Observations: someObs(failing), Events: Some(events)}, map[string]bool{})
		require.Len(t, got, 1)
		f := got[0]
		assert.Equal(t, "F_DIAGNOSIS_UNEXPLAINED", f.ID())
		assert.Equal(t, "failed", f.Params["state"])
		assert.Equal(t, "2", f.Params["exit_code"])
		assert.Equal(t, "workload.container_exited", f.Params["recent_event"], "latest event by seq")
		assert.NotEmpty(t, f.Fix.ConsolePath, "fallback must still suggest an action")
		assert.Contains(t, f.Evidence, Evidence{Kind: EvidenceEvent, Ref: "seq:41"})
	})

	t.Run("covered workload is not double-reported", func(t *testing.T) {
		got := unexplainedGaps(Facts{Observations: someObs(failing), Events: Some(events)}, map[string]bool{"w6": true})
		assert.Empty(t, got)
	})

	t.Run("events face none means no event evidence and no guessing", func(t *testing.T) {
		got := unexplainedGaps(Facts{Observations: someObs(failing), Events: None[[]EventFact]()}, map[string]bool{})
		require.Len(t, got, 1)
		assert.NotContains(t, got[0].Params, "recent_event")
		for _, e := range got[0].Evidence {
			assert.NotEqual(t, EvidenceEvent, e.Kind)
		}
	})

	t.Run("observations none is silent", func(t *testing.T) {
		assert.Empty(t, unexplainedGaps(Facts{}, map[string]bool{}))
	})

	t.Run("non-failure states are not fallback inputs", func(t *testing.T) {
		for _, s := range []capability.WorkloadState{
			capability.WorkloadRunning, capability.WorkloadPending,
			capability.WorkloadStopped, capability.WorkloadCompleted,
		} {
			o := failing
			o.State = s
			assert.Empty(t, unexplainedGaps(Facts{Observations: someObs(o)}, map[string]bool{}), "state %s", s)
		}
	})

	t.Run("orphaned is a failure-state input", func(t *testing.T) {
		o := failing
		o.State = capability.WorkloadOrphaned
		assert.Len(t, unexplainedGaps(Facts{Observations: someObs(o)}, map[string]bool{}), 1)
	})
}

// TestNoneFacesSilentAndSurfaced：三态——各面 None 规则沉默、面名透出。
func TestNoneFacesSilentAndSurfaced(t *testing.T) {
	failing := WorkloadObservationFact{
		WorkloadID: "w7", State: capability.WorkloadFailed, ExitCode: intPtr(1),
		RestartCount: CrashLoopThreshold,
	}
	someEvents := Some([]EventFact{{Seq: 1, Code: "workload.container_exited", At: testNow}})

	t.Run("observations none silences everything including the fallback", func(t *testing.T) {
		facts := Facts{
			Observations: None[[]WorkloadObservationFact](),
			Events:       someEvents,
			Alerts:       Some([]AlertFact{}),
			Components:   Some([]ComponentHealthFact{}),
			Now:          testNow,
		}
		assert.Empty(t, Evaluate(facts))
		assert.Equal(t, []string{FaceObservations}, facts.UnreadableFaces())
	})

	t.Run("all faces none surfaces every face name", func(t *testing.T) {
		facts := Facts{Now: testNow}
		assert.Empty(t, Evaluate(facts))
		assert.Equal(t, []string{FaceObservations, FaceEvents, FaceAlerts, FaceComponents}, facts.UnreadableFaces())
	})

	t.Run("empty observations is not none", func(t *testing.T) {
		facts := Facts{
			Observations: Some([]WorkloadObservationFact{}),
			Events:       someEvents,
			Alerts:       Some([]AlertFact{}),
			Components:   Some([]ComponentHealthFact{}),
			Now:          testNow,
		}
		assert.Empty(t, Evaluate(facts), "readable-empty snapshot yields no findings")
		assert.Empty(t, facts.UnreadableFaces())
	})

	t.Run("events none does not silence observation rules", func(t *testing.T) {
		facts := Facts{
			Observations: someObs(failing),
			Events:       None[[]EventFact](),
			Alerts:       Some([]AlertFact{}),
			Components:   Some([]ComponentHealthFact{}),
			Now:          testNow,
		}
		got := Evaluate(facts)
		assert.ElementsMatch(t, []string{"F_WORKLOAD_CRASH_LOOP"}, idsOf(got), "crash-loop explains; no unexplained on top")
		assert.Equal(t, []string{FaceEvents}, facts.UnreadableFaces())
	})

	t.Run("alerts and components none are shape-only in v1", func(t *testing.T) {
		withFaces := Facts{
			Observations: someObs(failing),
			Events:       Some([]EventFact{}),
			Alerts:       None[[]AlertFact](),
			Components:   None[[]ComponentHealthFact](),
			Now:          testNow,
		}
		assert.Equal(t, []string{FaceAlerts, FaceComponents}, withFaces.UnreadableFaces())
		assert.ElementsMatch(t, []string{"F_WORKLOAD_CRASH_LOOP"}, idsOf(Evaluate(withFaces)))
	})
}

// TestEvaluateDeterministic：同输入同输出（读时计算、零状态）。
func TestEvaluateDeterministic(t *testing.T) {
	facts := Facts{
		Observations: someObs(
			WorkloadObservationFact{WorkloadID: "a", State: capability.WorkloadFailed, ExitCode: intPtr(1), RestartCount: 5},
			WorkloadObservationFact{WorkloadID: "b", State: capability.WorkloadStopped},
		),
		Events: Some([]EventFact{{Seq: 1, Code: "workload.container_exited", At: testNow}}),
		Now:    testNow,
	}
	first := idsOf(Evaluate(facts))
	for i := 0; i < 3; i++ {
		assert.Equal(t, first, idsOf(Evaluate(facts)))
	}
	assert.Equal(t, []string{"F_WORKLOAD_CRASH_LOOP", "F_WORKLOAD_STOPPED_EXPLAINED"}, first, "rule order x observation order")
}

// TestInstantiateUnknownIDPanics：规则引用未入册条目是编程错误。
func TestInstantiateUnknownIDPanics(t *testing.T) {
	assert.Panics(t, func() {
		instantiate("F_NOPE", WorkloadObservationFact{WorkloadID: "w"}, nil, nil)
	})
}

// TestInstantiateMergesFixParams：实例预填覆盖模板同键、保留模板异键。
func TestInstantiateMergesFixParams(t *testing.T) {
	f := instantiate("F_WORKLOAD_STOPPED_EXPLAINED",
		WorkloadObservationFact{WorkloadID: "w", Process: "sidecar"},
		map[string]string{"process": "sidecar"}, nil)
	assert.Equal(t, map[string]string{"replicas": "1", "process": "sidecar"}, f.Fix.Params)
	// 注册表模板不被改写（instantiate 拷贝合并，非共享 map）。
	entry, ok := finding.Get("F_WORKLOAD_STOPPED_EXPLAINED")
	require.True(t, ok)
	assert.Equal(t, map[string]string{"replicas": "1"}, entry.Fix.Params)
}

// TestRewriteByIncident：platform 改写——fault_class 改写 + evidence 回链事故
// id；未命中原样；入参不被修改。
func TestRewriteByIncident(t *testing.T) {
	crashed := instantiate("F_WORKLOAD_CRASH_LOOP", WorkloadObservationFact{
		WorkloadID: "affected-1", Process: "web", State: capability.WorkloadFailed,
		ExitCode: intPtr(1), RestartCount: 5,
	}, nil, nil)
	other := instantiate("F_WORKLOAD_CRASH_LOOP", WorkloadObservationFact{
		WorkloadID: "unaffected-1", Process: "web", State: capability.WorkloadFailed,
		ExitCode: intPtr(1), RestartCount: 5,
	}, nil, nil)
	original := []Finding{crashed, other}

	out := RewriteByIncident(original, []Incident{{
		ID:                  "incident-7",
		AffectedWorkloadIDs: []string{"affected-1", "affected-2"},
	}})

	require.Len(t, out, 2)
	assert.Equal(t, finding.FaultClassPlatform, out[0].FaultClass, "incident-explained finding rewrites to platform")
	assert.Equal(t, finding.FaultClassApp, out[0].Entry.FaultClass, "registry entry stays canonical")
	assert.Contains(t, out[0].Evidence, Evidence{Kind: EvidenceIncident, Ref: "incident-7"})
	assert.Equal(t, finding.FaultClassApp, out[1].FaultClass, "unaffected finding unchanged")
	assert.NotContains(t, out[1].Evidence, Evidence{Kind: EvidenceIncident, Ref: "incident-7"})

	// 入参切片与元素不被修改（纯函数）。
	assert.Equal(t, finding.FaultClassApp, original[0].FaultClass)
	assert.Equal(t, crashed.Evidence, original[0].Evidence)

	// 空事故表 = 原样返回。
	assert.Equal(t, original, RewriteByIncident(original, nil))
}

// TestLatestEvent：Seq 最大者胜。
func TestLatestEvent(t *testing.T) {
	_, ok := latestEvent(nil)
	assert.False(t, ok)
	e, ok := latestEvent([]EventFact{
		{Seq: 5, Code: "a"},
		{Seq: 9, Code: "b"},
		{Seq: 7, Code: "c"},
	})
	require.True(t, ok)
	assert.Equal(t, int64(9), e.Seq)
}

// TestExitCodeMeaning：四档语义、其余不猜。
func TestExitCodeMeaning(t *testing.T) {
	assert.Equal(t, "command is not executable", ExitCodeMeaning(126))
	assert.Equal(t, "command not found", ExitCodeMeaning(127))
	assert.Contains(t, ExitCodeMeaning(137), "SIGKILL")
	assert.Contains(t, ExitCodeMeaning(143), "SIGTERM")
	assert.Empty(t, ExitCodeMeaning(0))
	assert.Empty(t, ExitCodeMeaning(1))
}
