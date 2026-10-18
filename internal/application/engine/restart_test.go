package engine

// ADR-0083 执行时 seq-only 判定钉测群：seq-only 跳迁移（PreDeployDone +
// kind=restart 审计）/ 非 seq-only 照常跑迁移 / 执行基线已被推进的场景
// （受理时 From 过期）判定仍正确 / BG 策略进程按 BG 舞步。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
)

// tRestartJobsSpec 合成带 preDeployJobs 的镜像直投 spec（ref 换镜像、seq
// 换重启序数——diff 判定的两个旋钮；seq=0 时 spec 不带该键）。
func tRestartJobsSpec(ref string, seq uint32) string {
	s := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"` + ref + `"}},` +
		`"processes":[{"name":"web","image":"` + ref + `","replicas":1`
	if seq > 0 {
		s += fmt.Sprintf(`,"restart_seq":%d`, seq)
	}
	s += `}],"pre_deploy_jobs":[{"name":"migrate","ttl":"600s","process":{"image":"busybox:1.37","command":["/migrate"]}},` +
		`{"name":"seed","ttl":"300s","process":{"image":"busybox:1.37","command":["/seed"]}}]}`
	return s
}

// auditRowsForDeployment 收某部署的审计行（Resource 全等过滤）。
func auditRowsForDeployment(t *testing.T, e *Engine, id string) []audit.Entry {
	t.Helper()
	entries, err := e.repos.Audits.ListFiltered(context.Background(), audit.Filter{
		Resource: "deployment/" + id, Limit: 100,
	})
	require.NoError(t, err)
	return entries
}

// assertRestartMarked 断言部署的审计流携带 "; kind=restart"（ADR-0083
// 步骤 7：审计入 lineage——行上无 Kind 列，kind 只活在审计流）。
func assertRestartMarked(t *testing.T, e *Engine, id string) {
	t.Helper()
	marked := false
	for _, row := range auditRowsForDeployment(t, e, id) {
		if row.Action == "deployment.transit" && row.AfterFP == "preparing; kind=restart" {
			marked = true
		}
	}
	assert.True(t, marked, "the in-place restart marking must surface in the audit stream")
}

// driveJobsToSucceeded 把带两 job 的部署驱动到 succeeded（串行 converge 驱动序）。
func driveJobsToSucceeded(t *testing.T, e *Engine, d *deployment.Deployment, clock interface {
	Advance(time.Duration)
}) {
	t.Helper()
	ctx := context.Background()
	e.step(ctx) // queued → preparing → releasing（铸 job0）
	d = getDeployment(t, e, d.ID)
	require.Equal(t, deployment.StateReleasing, d.State)
	completeJobTask(t, e, d)
	e.step(ctx) // job0 成功 → 下一拍才铸 job1（串行）
	d = getDeployment(t, e, d.ID)
	completeJobTask(t, e, d)
	e.step(ctx) // done → materialize → L1 deadline
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", d.Generation))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)
}

// TestRestartSeqOnlySkipsPreDeployJobs：基线（jobs 已跑）成功后，仅 bump
// restart_seq 的新 Revision 受理 → prepare 判定命中 → 游标直落 done、
// job 零铸造（deployment.pre_deploy_job 事件缺席）→ carrier 相位直进 →
// succeeded；审计携带 "; kind=restart"（步骤 7：审计入 lineage）。
func TestRestartSeqOnlySkipsPreDeployJobs(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()

	// 基线：jobs 全跑完 → succeeded（TestPreDeploySerialConverge 的驱动序）。
	rev1 := freezeSpec(t, e, 1, tRestartJobsSpec("nginx:1.26", 0))
	d1, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	driveJobsToSucceeded(t, e, d1, clock)
	require.Equal(t, 2, taskCount(t, e), "baseline ran both pre-deploy jobs")

	// 重启 Revision：仅 restart_seq 1（0→1 是唯一 diff）。
	rev2 := freezeSpec(t, e, 2, tRestartJobsSpec("nginx:1.26", 1))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx) // queued → preparing（判定命中）→ releasing（游标 done 直进 carrier 相位）
	d2 = getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateReleasing, d2.State)
	assert.Equal(t, deployment.PreDeployDone, d2.PreDeploy, "seq-only restart lands the done cursor (migrations never re-run)")
	assert.Empty(t, filterPrefix(eventNames(t, e, d2.ID), "deployment.pre_deploy_job"),
		"seq-only restart must not mint pre-deploy jobs")
	assert.Equal(t, 2, taskCount(t, e), "no new job tasks")

	// carrier 相位照常：running → 观察窗满 → succeeded。
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", d2.Generation))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d2.ID).State)
	assertRestartMarked(t, e, d2.ID)
}

// TestNonSeqOnlyChangeRunsPreDeployJobs：镜像 + seq 双变的 Revision 不是
// 重启——迁移照常重跑（免迁移判定不因 seq 在场而放宽）。
func TestNonSeqOnlyChangeRunsPreDeployJobs(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()

	rev1 := freezeSpec(t, e, 1, tRestartJobsSpec("nginx:1.26", 0))
	d1, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	driveJobsToSucceeded(t, e, d1, clock)

	// 镜像变更 + seq bump：diff ⊄ restart_seq → 非 restart。
	rev2 := freezeSpec(t, e, 2, tRestartJobsSpec("nginx:1.27", 1))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	d2 = getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateReleasing, d2.State)
	_, _, anchored := d2.PreDeployAnchor()
	assert.True(t, anchored, "a non-seq-only change must re-run pre-deploy jobs (cursor waiting, not done)")
	assert.NotEqual(t, deployment.PreDeployDone, d2.PreDeploy)
	for _, row := range auditRowsForDeployment(t, e, d2.ID) {
		assert.NotContains(t, row.AfterFP, "kind=restart",
			"a non-seq-only deployment must not be marked kind=restart")
	}
}

// TestRestartSeqOnlyRecomputesBaselineAtExecution：执行基线已被推进的
// 场景（受理时 From 过期）——判定对实际最新 succeeded 基线重算（对抗
// 审查 B8）：受理锚 rev1（内容差异巨大），执行时基线已是 rev2（与目标
// 仅差 seq）→ 命中 restart、迁移免跑。若误用受理时 From 判定，
// diff(rev1→rev3) 含镜像变更，jobs 会被错误重跑。
func TestRestartSeqOnlyRecomputesBaselineAtExecution(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()

	// rev1（jobs 腿）成功；rev2（换镜像、jobs 同款——真实内容变更照常重跑）
	// 成功——基线推进到 rev2（jobs 在册）。
	rev1 := freezeSpec(t, e, 1, tRestartJobsSpec("nginx:1.26", 0))
	d1, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	driveJobsToSucceeded(t, e, d1, clock)

	rev2 := freezeSpec(t, e, 2, tRestartJobsSpec("nginx:1.27", 0))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	driveJobsToSucceeded(t, e, d2, clock)

	// 直铸一条 queued 行：From 锚过期（rev1——受理期形态），目标是
	// rev3 = rev2 + restart_seq。基线在受理后被更早部署推进的形态由
	// 夹具直接呈现。
	rev3 := freezeSpec(t, e, 3, tRestartJobsSpec("nginx:1.27", 1))
	seq, err := e.repos.NextSeq(ctx, "deployments")
	require.NoError(t, err)
	gen, err := e.repos.Deployments.NextGeneration(ctx, tAppID)
	require.NoError(t, err)
	stale := &deployment.Deployment{
		ID: entityid.Derive(tAppID, "d", seq), AppID: tAppID,
		FromRevision: rev1, FromGeneration: d1.Generation,
		ToRevision: rev3, State: deployment.StateQueued, Generation: gen,
	}
	require.NoError(t, e.repos.Deployments.Create(ctx, stale))

	e.step(ctx) // 拾取：对实际基线 rev2 重算 diff(rev2→rev3) = seq-only → restart
	d3 := getDeployment(t, e, stale.ID)
	require.Equal(t, deployment.StateReleasing, d3.State)
	assert.Equal(t, deployment.PreDeployDone, d3.PreDeploy,
		"the execution-time judgment must recompute against the actual latest succeeded baseline")
	assert.Empty(t, filterPrefix(eventNames(t, e, stale.ID), "deployment.pre_deploy_job"),
		"jobs must not re-run even though the admission-time From anchor differs by image")
	assertRestartMarked(t, e, stale.ID)
}

// TestRestartSeqOnlyBlueGreenKeepsBGDance：BG 策略进程的 seq-only 变更
// 照走双代窗（窗口 Ensure 两代成员、新代代次化载体）——kind=restart 只
// 执游标与审计，物化舞步不因重启序数改变（边界裁决）。
func TestRestartSeqOnlyBlueGreenKeepsBGDance(t *testing.T) {
	e, rt, clock := newTestEngine(t)
	ctx := context.Background()

	// BG 基线（首部署 gen=1，新代代次化名）。
	rev1 := freezeSpec(t, e, 1, bgSpecFor("nginx:1.27"))
	d1, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev1})
	require.NoError(t, err)
	e.step(ctx)
	d1 = getDeployment(t, e, d1.ID)
	require.Equal(t, deployment.StateReleasing, d1.State)
	e.handleObservation(ctx, workloadEventRunning(genScopedWorkloadID(tAppID+"-web", 1), 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d1.ID).State)

	// seq-only BG 重启（nginx:1.27 + restart_seq=1）：窗口照开（两代成员），
	// 新代 g2 滚动替换。
	rev2 := freezeSpec(t, e, 2, bgSpecForSeq("nginx:1.27", 1))
	d2, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: rev2})
	require.NoError(t, err)
	e.step(ctx)
	d2 = getDeployment(t, e, d2.ID)
	require.Equal(t, deployment.StateReleasing, d2.State)
	assert.Equal(t, deployment.PreDeployDone, d2.PreDeploy)

	win := rt.calls()[len(rt.calls())-1]
	require.Len(t, win.ByID, 2, "the seq-only restart still opens the blue-green window")
	newID := genScopedWorkloadID(tAppID+"-web", 2)
	newW, ok := win.ByID[newID]
	require.True(t, ok, "the new generation carrier is generation-scoped")
	assert.Equal(t, uint32(1), newW.RestartSeq, "the new generation carries the bumped restart_seq")
	assertRestartMarked(t, e, d2.ID)
}

// bgSpecForSeq 是 bgSpecFor 的带重启序数变体（ADR-0083 BG 钉测夹具）。
func bgSpecForSeq(ref string, seq uint32) string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"` + ref + `"}},` +
		fmt.Sprintf(`"processes":[{"name":"web","image":"%s","replicas":1,"restart_seq":%d,`, ref, seq) +
		`"ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}],"strategy":"DEPLOY_STRATEGY_BLUE_GREEN"}]}`
}
