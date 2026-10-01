package engine

// 守卫 D（crash-recovery 测试类别，审计 §10/M-3）：hermetic 夹具形态 =
// 真 SQLite + 新 Engine 同库重启 + 有界拍断言确定终态。D-4 构建孤儿线是
// 本类别第一批用例；后续带进程内状态的线在此层复用 restartEngine。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// restartEngine 模拟进程重启：有界排空旧实例（Stop：构建 goroutine 经
// runCtx 取消优雅回 queued）后，以同库/同 Runtime/同 Builder/同数据根
// 构造新实例。进程内状态（观测缓存、构建输入登记）随旧实例丢弃——
// 这正是被测的恢复面。未 Start 的实例 Stop 为 no-op，等价硬杀丢弃。
func restartEngine(t *testing.T, e *Engine) *Engine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, e.Stop(ctx), "engine must drain within the bound")
	return New(Deps{DB: e.db, Runtime: e.runtime, Builder: e.builder, Logger: e.log}, e.opts)
}

// driveToTerminal 有界拍手动驱动新引擎直至 Deployment 终态：每拍注入
// running 观测 + 推进假时钟（L1/L3 门依赖时间流逝）。
func driveToTerminal(t *testing.T, e *Engine, d *deployment.Deployment) *deployment.Deployment {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.DriveOnce(ctx)
		e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", d.Generation))
		advanceClock(t, e, 30*time.Second)
		got := getDeployment(t, e, d.ID)
		if got.State.Terminal() {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment did not reach a terminal state within bounded ticks (state=%s err=%s)", got.State, got.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// D-4 用例 ①（构建在途重启）：构建在途 → Stop → 同库新引擎 → DriveOnce
// 有界拍内输入幂等重建、Build 到终态 succeeded、部署继续推进到终态成功
// （重建不产生第二条 Build 行）。
func TestCrashRecoveryBuildInFlightRestart(t *testing.T) {
	fb := newBlockingBuilder("sha256:built")
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, revSeq, contextDir := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B4")

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // → building + Build 行受理（旧实例登记输入）
	b, ok := lastBuild(t, e, revID)
	require.True(t, ok)
	require.Equal(t, build.StateQueued, b.State)

	e.Start(ctx) // 真实节拍：构建循环拾取执行
	<-fb.entered // 构建在途（goroutine 挂在 builder 内）
	require.Equal(t, deployment.StateBuilding, getDeployment(t, e, d.ID).State)

	e2 := restartEngine(t, e) // Stop：runCtx 取消 → 构建优雅回 queued、排水完成
	b, ok = lastBuild(t, e2, revID)
	require.True(t, ok)
	require.Equal(t, build.StateQueued, b.State, "graceful shutdown returns the in-flight build to queued")
	fb.unblock() // 旧 goroutine 已退出；放行后续（新实例的）构建

	final := driveToTerminal(t, e2, d)
	require.Equal(t, deployment.StateSucceeded, final.State)

	// 重建是登记层面的幂等：Build 行仍只此一条，且到终态带 digest。
	builds, err := e2.builds.ListByRevision(ctx, e2.db.Runner(), revID)
	require.NoError(t, err)
	require.Len(t, builds, 1, "input rebuild must not duplicate the build row")
	require.Equal(t, build.StateSucceeded, builds[0].State)
	assert.Equal(t, "sha256:built", builds[0].Digest)
	calls := fb.snapshot()
	require.Len(t, calls, 2, "one cancelled attempt plus one replayed run")
	assert.Equal(t, LocalImageRef(tAppID, revSeq), calls[1].Target)
	assert.Equal(t, contextDir, calls[1].ContextDir)
}

// D-4 用例 ②（取消后孤儿行）：部署取消后遗留的 Build 行（重启形态：进程
// 内登记已丢）一拍内到终态 cancelled；连续两拍状态不再变化（无
// queued↔building 振荡），且永不触达 builder。
func TestCrashRecoveryOrphanBuildAfterCancel(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:built"}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B5")

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // → building + Build 行受理
	b, ok := lastBuild(t, e, revID)
	require.True(t, ok)
	require.Equal(t, build.StateQueued, b.State)

	_, err = e.Cancel(ctx, d.ID) // 取消部署：Build 行成为孤儿
	require.NoError(t, err)

	e2 := restartEngine(t, e) // 登记随旧实例丢弃（未 Start，Stop 为 no-op）
	e2.DriveOnce(ctx)         // buildStep 前置检：无登记 + 无活跃归属 → 同步一跳终态
	b, ok = lastBuild(t, e2, revID)
	require.True(t, ok)
	require.Equal(t, build.StateCancelled, b.State)
	assert.Contains(t, b.Error, "owning deployment is no longer active")
	assert.True(t, b.State.Terminal())

	e2.DriveOnce(ctx) // 第二拍：状态稳定，无进一步迁移（无振荡）。
	b2, ok := lastBuild(t, e2, revID)
	require.True(t, ok)
	assert.Equal(t, build.StateCancelled, b2.State)
	assert.Equal(t, b.UpdatedAt, b2.UpdatedAt, "no transitions after the terminal hop")
	assert.Empty(t, fb.snapshot(), "orphan build must never reach the builder")
}

// D-4 补充（硬杀形态）：进程被杀（无 Stop 排水、无 Start 重置）遗留
// building 行——新引擎 DriveOnce 首拍由 driveBuilding 幂等重建输入并重置
// queued，随后构建执行到终态、部署推进成功。
func TestCrashRecoveryKilledBuildingRow(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:built"}
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B6")

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.step(ctx) // → building + queued Build 行
	// 硬杀等价：行被拍到 building（本进程无执行 goroutine），实例整体丢弃。
	b, ok := lastBuild(t, e, revID)
	require.True(t, ok)
	_, err = e.transitBuild(ctx, b, []build.State{build.StateQueued}, build.StateBuilding, nil)
	require.NoError(t, err)

	e2 := restartEngine(t, e) // 未 Start：Stop no-op，登记与执行者一并消失
	final := driveToTerminal(t, e2, d)
	require.Equal(t, deployment.StateSucceeded, final.State)

	builds, err := e2.builds.ListByRevision(ctx, e2.db.Runner(), revID)
	require.NoError(t, err)
	require.Len(t, builds, 1)
	require.Equal(t, build.StateSucceeded, builds[0].State)
	assert.Equal(t, "sha256:built", builds[0].Digest)
}

// Q-7 停机排水：构建 goroutine 计入 wg——构建中 Stop(有界 ctx) 有界完成
// （不默等构建超时），Build 行经优雅退出路径回 queued（重放语义）。
func TestStopDuringBuildDrainsBounded(t *testing.T) {
	fb := newBlockingBuilder("sha256:built")
	e, _ := newBuildEngine(t, fb)
	ctx := context.Background()
	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000B9")

	d, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)
	e.Start(ctx)
	<-fb.entered // 构建在途
	require.Equal(t, deployment.StateBuilding, getDeployment(t, e, d.ID).State)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, e.Stop(stopCtx), "drain must complete within the bound")

	b, ok := lastBuild(t, e, revID)
	require.True(t, ok)
	assert.Equal(t, build.StateQueued, b.State, "graceful exit returns the build to queued for replay")
	fb.unblock() // 夹具清理（goroutine 已退出）
}
