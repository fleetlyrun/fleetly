// rules_deployment_test.go 是部署面规则白盒测试（零 I/O、纯函数直测；
// 立案③残留缺口批）。
package diagnosis

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/finding"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyVolumeError 复刻 torchwood 走查实录的部署失败错误正文（engine
// volumes.go ErrLegacyVolumeCarrier 面——staging d1 failed 的实况形态）。
const legacyVolumeError = `legacy name-keyed volume carrier present: volume "legacy-volume" still lives in its pre-ADR-0097 name-keyed carrier (docker volumes cannot be renamed); move the data to a volume created after the upgrade, or remove the runtime-side carrier and redeploy (fresh volume)`

// failedDeploymentFact 是走查形态的事实行（d1 failed、错误正文在行上）。
func failedDeploymentFact() LatestDeploymentFact {
	return LatestDeploymentFact{
		ID: "app1-d1", State: deployment.StateFailed,
		Error: legacyVolumeError, Generation: 2, FinishedAt: "2026-11-03T09:15:00Z",
	}
}

// TestLatestDeploymentFailedRule：最新部署 failed + 有 error → 条目（App 级
// 锚定：无 workload 轴，证据链是部署行引用；错误首行进 Params，全文留在
// 部署行）。
func TestLatestDeploymentFailedRule(t *testing.T) {
	facts := Facts{
		Observations:     Some([]WorkloadObservationFact{}),
		Events:           Some([]EventFact{}),
		LatestDeployment: Some(failedDeploymentFact()),
		Now:              testNow,
	}
	got := Evaluate(facts)
	require.Len(t, got, 1, "a failed latest deployment with no observations yields exactly the deployment finding")
	f := got[0]
	assert.Equal(t, "F_DEPLOYMENT_LATEST_FAILED", f.ID())
	assert.Equal(t, finding.SeverityWarning, f.Severity())
	assert.Equal(t, finding.FaultClassDeclaration, f.FaultClass, "undecidable error text defaults to the declaration class (where to look first)")
	assert.Empty(t, f.WorkloadID, "app-anchored: no workload axis")
	assert.Empty(t, f.Process)
	assert.Equal(t, "app1-d1", f.Params["deployment"])
	assert.Equal(t, "failed", f.Params["state"])
	assert.Equal(t, legacyVolumeError, f.Params["error"], "single-line error rides the params whole")
	assert.Equal(t, "2", f.Params["generation"])
	assert.Equal(t, "2026-11-03T09:15:00Z", f.Params["finished_at"])
	assert.Equal(t, "app1-d1", f.Fix.Params["deployment"], "fix prefills the deployment id for the 'apps deployment <id>' move")
	assert.Empty(t, f.Fix.Verb, "viewing/redeploying goes through the deploy sheet, not a direct verb")
	assert.NotEmpty(t, f.Fix.ConsolePath)
	assert.Contains(t, f.Evidence, Evidence{Kind: EvidenceDeployment, Ref: "app1-d1"})
	assert.NotContains(t, f.Evidence, Evidence{Kind: EvidenceWorkload, Ref: ""}, "no empty workload evidence for app-anchored findings")
}

// TestLatestDeploymentFailedRuleSilent：零噪声面——非 failed（succeeded/
// cancelled/在途态）、failed 无 error 正文、无部署行（Some 零值）、面 None
// 一律沉默。
func TestLatestDeploymentFailedRuleSilent(t *testing.T) {
	for _, st := range []deployment.State{
		deployment.StateSucceeded, deployment.StateCancelled,
		deployment.StateSuperseded, deployment.StateQueued,
		deployment.StatePreparing, deployment.StateBuilding,
		deployment.StateReleasing, deployment.StateObserving,
		deployment.StateRollingBack,
	} {
		d := failedDeploymentFact()
		d.State = st
		assert.Empty(t, ruleLatestDeploymentFailed.Check(Facts{LatestDeployment: Some(d)}),
			"state %s must be silent (succeeded/in-flight/superseded carry no failure to explain)", st)
	}
	noErr := failedDeploymentFact()
	noErr.Error = ""
	assert.Empty(t, ruleLatestDeploymentFailed.Check(Facts{LatestDeployment: Some(noErr)}),
		"a failed row without error text is not this rule's input")
	assert.Empty(t, ruleLatestDeploymentFailed.Check(Facts{LatestDeployment: Some(LatestDeploymentFact{})}),
		"readable and no deployments yet: silence (never deployed is not a failure)")
	assert.Empty(t, ruleLatestDeploymentFailed.Check(Facts{}),
		"face none is silent (unreadable does not mean failed)")
}

// TestLatestDeploymentFailedIdempotent：同部署 id 重复诊断不重复发——读时
// 计算无状态，重复 Evaluate 恒产同一条目（无累积/无去重窗）。
func TestLatestDeploymentFailedIdempotent(t *testing.T) {
	facts := Facts{
		Observations:     Some([]WorkloadObservationFact{}),
		Events:           Some([]EventFact{}),
		LatestDeployment: Some(failedDeploymentFact()),
		Now:              testNow,
	}
	for i := 0; i < 3; i++ {
		got := Evaluate(facts)
		require.Len(t, got, 1, "evaluation %d: the same failed deployment yields exactly one finding, never accumulating", i)
		assert.Equal(t, "app1-d1", got[0].Params["deployment"])
	}
}

// TestErrorFirstLine：多行错误正文只携首行（全文留在部署行，不转写放大）。
func TestErrorFirstLine(t *testing.T) {
	assert.Equal(t, "head line", errorFirstLine("head line\nsecond line\nthird"))
	assert.Equal(t, "only line", errorFirstLine("only line"))
	assert.Equal(t, "", errorFirstLine(""))
	d := failedDeploymentFact()
	d.Error = "first failure line\nstack below"
	got := ruleLatestDeploymentFailed.Check(Facts{LatestDeployment: Some(d)})
	require.Len(t, got, 1)
	assert.Equal(t, "first failure line", got[0].Params["error"])
}
