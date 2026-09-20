package state

// deployments 行驱动测试：状态词表、CAS 迁移（终态不可逆、竞争恰好一个
// 胜出）、互斥查询、标志位与时间锚、派生状态 CAS 与 revisions seq 分配。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newDeployStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "dep.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDeploymentLifecycleAndCAS(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "depapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	rec, err := st.CreateDeployment(ctx, DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy",
		SpecHash: "hash1", ComposePath: "/tmp/compose.yaml",
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if rec.Status != DeployQueued {
		t.Fatalf("initial status = %s, want queued", rec.Status)
	}

	// CAS：queued → preparing 恰好一次；第二次竞争落败。
	to := DeployPreparing
	from := DeployQueued
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &to, PrevStatus: &from}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &to, PrevStatus: &from}); !errors.Is(err, ErrDeploymentStateTransition) {
		t.Fatalf("double claim err = %v, want ErrDeploymentStateTransition", err)
	}

	// 字段补丁（无状态谓词）：期望态哈希与看门狗锚。
	now := time.Now().UTC()
	deadline := now.Add(300 * time.Second)
	specHash := "hash1"
	envHash := "envhash"
	desiredHash := "desiredhash"
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{
		ReleaseStartedAt:   &now,
		WatchdogDeadlineAt: &deadline,
		SpecHash:           &specHash,
		EnvSnapshotHash:    &envHash,
		DesiredHash:        &desiredHash,
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	// 全链推进到 succeeded（按合法链 CAS 逐步推进）。
	chain := []DeploymentStatus{DeployBuilding, DeployReleasing, DeployObserving, DeploySucceeded}
	prev := DeployPreparing
	for _, next := range chain {
		n := next
		p := prev
		if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &n, PrevStatus: &p}); err != nil {
			t.Fatalf("transition %s -> %s: %v", prev, next, err)
		}
		prev = next
	}
	// 终态不可逆。
	f := DeployFailed
	term := DeploySucceeded
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &f, PrevStatus: &term}); !errors.Is(err, ErrDeploymentStateTransition) {
		t.Fatalf("terminal exit err = %v, want ErrDeploymentStateTransition", err)
	}

	// 回读：全部字段保真。
	row, err := st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != DeploySucceeded || row.SpecHash != "hash1" || row.DesiredHash != "desiredhash" || row.EnvSnapshotHash != "envhash" {
		t.Fatalf("row = %+v", row)
	}
	if row.WatchdogDeadlineAt.IsZero() || !row.WatchdogDeadlineAt.Equal(deadline) {
		t.Fatalf("watchdog deadline = %v, want %v", row.WatchdogDeadlineAt, deadline)
	}

	// 互斥查询：终态不算在途。
	has, err := st.AppHasNonTerminalDeployment(ctx, app.ID)
	if err != nil || has {
		t.Fatalf("AppHasNonTerminalDeployment = %v (%v), want false", has, err)
	}
}

func TestDeploymentFailureRecordFields(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "failapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	rec, err := st.CreateDeployment(ctx, DeployRecord{AppID: app.ID, AppName: app.Name, Kind: "deploy"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	verdict := VerdictUnstable
	recovery := RecoveryRestore
	halted := true
	started := time.Now().UTC()
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{
		Verdict:           &verdict,
		Recovery:          &recovery,
		SubstrateHalted:   &halted,
		DowntimeStartedAt: &started,
		Flags:             func() *int64 { v := DeployFlagPostWindowAlerted; return &v }(),
	}); err != nil {
		t.Fatalf("patch failure fields: %v", err)
	}
	row, _ := st.GetDeployment(ctx, rec.ID)
	if row.Verdict != VerdictUnstable || row.Recovery != RecoveryRestore || !row.SubstrateHalted {
		t.Fatalf("row = %+v", row)
	}
	if row.Flags != DeployFlagPostWindowAlerted {
		t.Fatalf("flags = %d", row.Flags)
	}
	if !row.DowntimeStartedAt.Equal(started) {
		t.Fatalf("downtime started = %v", row.DowntimeStartedAt)
	}
	// kind 校验。
	if _, err := st.CreateDeployment(ctx, DeployRecord{AppID: app.ID, Kind: "bogus"}); err == nil {
		t.Fatal("invalid kind accepted")
	}
}

// TestDeploymentPhaseStartedAtAnchor（H11）：准备/构建预算锚点的补丁写入与
// 回读保真；新行/存量行锚点为 NULL → 零值（消费方回落 created_at 的载体）。
func TestDeploymentPhaseStartedAtAnchor(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "anchorapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	rec, err := st.CreateDeployment(ctx, DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy",
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	// 新行锚点未写（列 NULL）→ 零值。
	row, err := st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.PhaseStartedAt.IsZero() {
		t.Fatalf("fresh row phase_started_at = %v, want zero（NULL → 回落语义）", row.PhaseStartedAt)
	}
	// 拾取补丁：CAS 同拍写锚点（queued → preparing）。
	to := DeployPreparing
	from := DeployQueued
	anchor := time.Now().UTC()
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{
		Status: &to, PrevStatus: &from, PhaseStartedAt: &anchor,
	}); err != nil {
		t.Fatalf("claim with anchor: %v", err)
	}
	row, err = st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Status != DeployPreparing || !row.PhaseStartedAt.Equal(anchor) {
		t.Fatalf("row status=%s anchor=%v, want preparing/%v", row.Status, row.PhaseStartedAt, anchor)
	}
}

// TestTxCreateDeploymentAtomicWithEventAudit（H13 修复，state-model §2.9）：
// Tx.CreateDeployment 与事件/审计同事务组合的原语测试——
// ① 成功路径：建行 + deployment.queued 事件同一事务提交，两者一并可见；
// ② fail-closed 路径：部署行 INSERT 已执行后事件写失败（故障注入：回调内
// 取消 ctx——Tx.grabConn 见 ctx.Done 即拒绝执行后续写）→ 整体回滚，部署
// 行不存在、事件不留痕：发布入队不存在「引擎可执行但事件/审计缺失」的
// 中间态。
func TestTxCreateDeploymentAtomicWithEventAudit(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "atomapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// ① 成功路径：建行与事件同事务提交。
	var rec DeployRecord
	if err := st.InTx(ctx, func(tx *Tx) error {
		r, err := tx.CreateDeployment(ctx, DeployRecord{
			AppID: app.ID, AppName: app.Name, Kind: "deploy", SpecHash: "hash1",
		})
		if err != nil {
			return err
		}
		rec = r
		_, err = tx.AppendEvent(ctx, Event{
			Name: "deployment.queued", Subject: "deployment:" + r.ID,
			Payload: `{"deployment":"` + r.ID + `","app":"` + app.Name + `"}`,
		})
		return err
	}); err != nil {
		t.Fatalf("healthy tx: %v", err)
	}
	if row, err := st.GetDeployment(ctx, rec.ID); err != nil || row.Status != DeployQueued {
		t.Fatalf("deployment after commit = %+v (%v), want queued", row, err)
	}
	events, err := st.EventsSince(ctx, 0, 10)
	if err != nil || len(events) != 1 || events[0].Subject != "deployment:"+rec.ID {
		t.Fatalf("events after commit = %+v (%v), want one queued event", events, err)
	}

	// ② fail-closed：部署行 INSERT 成功 → 事件写注入失败 → 整体回滚。
	fctx, cancel := context.WithCancel(ctx)
	var orphanID string
	err = st.InTx(fctx, func(tx *Tx) error {
		r, err := tx.CreateDeployment(fctx, DeployRecord{
			AppID: app.ID, AppName: app.Name, Kind: "deploy", SpecHash: "hash2",
		})
		if err != nil {
			return err
		}
		orphanID = r.ID
		cancel() // 故障注入：随后的事件写必失败（ctx 已取消）
		_, err = tx.AppendEvent(fctx, Event{
			Name: "deployment.queued", Subject: "deployment:" + r.ID,
		})
		return err
	})
	if err == nil {
		t.Fatal("event write failure must fail the whole enqueue tx (fail-closed)")
	}
	if _, err := st.GetDeployment(ctx, orphanID); !errors.Is(err, ErrDeploymentNotFound) {
		t.Fatalf("deployment row must roll back with event write failure, got: %v", err)
	}
	events, err = st.EventsSince(ctx, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events after rollback = %+v (%v), want only the committed one", events, err)
	}
}

func TestAppDerivedStateCASAndConflict(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "derived")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	got, err := st.GetAppDerivedState(ctx, app.ID)
	if err != nil || got != "" {
		t.Fatalf("initial derived = %q (%v)", got, err)
	}
	// CAS 翻转：空 → degraded 一次成功；重复同前值冲突。
	if err := st.InTx(ctx, func(tx *Tx) error {
		return tx.SetAppDerivedState(ctx, app.ID, "", AppStateDegraded)
	}); err != nil {
		t.Fatalf("flip to degraded: %v", err)
	}
	err = st.InTx(ctx, func(tx *Tx) error {
		return tx.SetAppDerivedState(ctx, app.ID, "", AppStateRunning)
	})
	if !errors.Is(err, ErrAppDerivedStateConflict) {
		t.Fatalf("stale CAS err = %v, want ErrAppDerivedStateConflict", err)
	}
	// degraded → recovered。
	err = st.InTx(ctx, func(tx *Tx) error {
		return tx.SetAppDerivedState(ctx, app.ID, AppStateDegraded, AppStateRunning)
	})
	if err != nil {
		t.Fatalf("flip to running: %v", err)
	}
}

func TestRevisionSeqPerApp(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	appA, _ := st.CreateApp(ctx, "", "reva")
	appB, _ := st.CreateApp(ctx, "", "revb")
	var seqA1, seqA2, seqB1 int64
	if err := st.InTx(ctx, func(tx *Tx) error {
		rev, err := tx.CreateRevision(ctx, RevisionWrite{AppID: appA.ID, ComposeNormalized: "{}", Overlay: "{}", DesiredHash: "h1"})
		if err != nil {
			return err
		}
		seqA1 = rev.Seq
		return nil
	}); err != nil {
		t.Fatalf("create rev a1: %v", err)
	}
	if err := st.InTx(ctx, func(tx *Tx) error {
		rev, err := tx.CreateRevision(ctx, RevisionWrite{AppID: appB.ID, ComposeNormalized: "{}", Overlay: "{}", DesiredHash: "h2"})
		if err != nil {
			return err
		}
		seqB1 = rev.Seq
		rev2, err := tx.CreateRevision(ctx, RevisionWrite{AppID: appA.ID, ComposeNormalized: "{}", Overlay: "{}", DesiredHash: "h3"})
		if err != nil {
			return err
		}
		seqA2 = rev2.Seq
		return nil
	}); err != nil {
		t.Fatalf("create rev b1/a2: %v", err)
	}
	if seqA1 != 1 || seqA2 != 2 || seqB1 != 1 {
		t.Fatalf("seqs = a1:%d a2:%d b1:%d, want per-app increment", seqA1, seqA2, seqB1)
	}
	rev, err := st.revisionIDFor(t, ctx, appA.ID, seqA2)
	if err != nil {
		t.Fatalf("find revision: %v", err)
	}
	row, err := st.GetRevision(ctx, rev)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if !row.Verified || row.DesiredHash != "h3" || row.Seq != 2 {
		t.Fatalf("revision row = %+v", row)
	}
}

// revisionIDFor 是测试辅助（按 app+seq 定位 revision ID）。
func (s *Store) revisionIDFor(t *testing.T, ctx context.Context, appID string, seq int64) (string, error) {
	t.Helper()
	const q = `SELECT id FROM revisions WHERE app_id = ? AND seq = ?`
	var id string
	err := s.db.QueryRowContext(ctx, q, appID, seq).Scan(&id)
	return id, err
}

// TestLatestDeploymentsByAppBatch S18-A4：批量 per-app 最近部署窗口——逐
// app 窗口内 created_at 倒序（与 ListAppDeployments 同序）、窗口截断生效、
// 无部署 app 不在 map、JOIN 回填应用名、空 ID 集直接空 map 不发查询。
func TestLatestDeploymentsByAppBatch(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	a1, err := st.CreateApp(ctx, "", "batch-a")
	if err != nil {
		t.Fatalf("create app a: %v", err)
	}
	a2, err := st.CreateApp(ctx, "", "batch-b")
	if err != nil {
		t.Fatalf("create app b: %v", err)
	}
	a3, err := st.CreateApp(ctx, "", "batch-c")
	if err != nil {
		t.Fatalf("create app c: %v", err)
	}

	// a1：三条部署（h1 最旧 → h3 最新；同 app 顺序入队）。a2：一条。
	for _, h := range []string{"h1", "h2", "h3"} {
		if _, err := st.CreateDeployment(ctx, DeployRecord{
			AppID: a1.ID, AppName: a1.Name, Kind: "deploy", SpecHash: h,
		}); err != nil {
			t.Fatalf("create deployment %s: %v", h, err)
		}
	}
	if _, err := st.CreateDeployment(ctx, DeployRecord{
		AppID: a2.ID, AppName: a2.Name, Kind: "deploy", SpecHash: "only",
	}); err != nil {
		t.Fatalf("create deployment a2: %v", err)
	}

	got, err := st.LatestDeploymentsByApp(ctx, []string{a1.ID, a2.ID, a3.ID}, 25)
	if err != nil {
		t.Fatalf("LatestDeploymentsByApp: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("apps with rows = %d, want 2 (a3 无部署不在 map)", len(got))
	}
	rowsA1 := got[a1.ID]
	if len(rowsA1) != 3 {
		t.Fatalf("a1 rows = %d, want 3", len(rowsA1))
	}
	for i, wantHash := range []string{"h3", "h2", "h1"} {
		if rowsA1[i].SpecHash != wantHash {
			t.Fatalf("a1 row %d spec_hash = %s, want %s (created_at DESC)", i, rowsA1[i].SpecHash, wantHash)
		}
		if rowsA1[i].AppName != "batch-a" {
			t.Fatalf("a1 row %d app_name = %q, want JOIN 回填 batch-a", i, rowsA1[i].AppName)
		}
	}
	if rows := got[a2.ID]; len(rows) != 1 || rows[0].SpecHash != "only" {
		t.Fatalf("a2 rows = %+v, want single only", rows)
	}

	// 窗口截断：perApp=2 → 只保留最新两条。
	capped, err := st.LatestDeploymentsByApp(ctx, []string{a1.ID}, 2)
	if err != nil {
		t.Fatalf("LatestDeploymentsByApp capped: %v", err)
	}
	if rows := capped[a1.ID]; len(rows) != 2 || rows[0].SpecHash != "h3" || rows[1].SpecHash != "h2" {
		t.Fatalf("capped rows = %+v, want [h3 h2]", rows)
	}

	// 空 ID 集：空 map、无错误。
	empty, err := st.LatestDeploymentsByApp(ctx, nil, 25)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty batch = (%d, %v), want (0, nil)", len(empty), err)
	}
}

// TestDeploymentStatusWriteRequiresPrevStatus M3-2 回归：裸状态写（Status
// 无 PrevStatus）必须拒绝——该形态绕过转移表校验与 CAS 竞争保护；仅就地
// 字段 patch（Flags/CancelRequested，无 Status）不受影响。
func TestDeploymentStatusWriteRequiresPrevStatus(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "prev-status-app")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	rec, err := st.CreateDeployment(ctx, DeployRecord{
		AppID: app.ID, AppName: app.Name, Kind: "deploy",
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// 裸状态写（即使目标状态与当前行状态相同）：拒绝。
	same := DeployQueued
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &same}); err == nil {
		t.Fatal("bare status write (no PrevStatus) must be rejected (M3-2)")
	}
	other := DeployPreparing
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &other}); err == nil {
		t.Fatal("bare status write to different status must be rejected (M3-2)")
	}
	// 行状态未被裸写改动。
	got, err := st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Status != DeployQueued {
		t.Fatalf("status = %s, want queued (裸写不得生效)", got.Status)
	}

	// 仅字段 patch（无 Status）：不受影响——CancelRequested / Flags 就地更新。
	set := true
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{CancelRequested: &set}); err != nil {
		t.Fatalf("CancelRequested-only patch: %v", err)
	}
	flags := DeployFlagInstabilityWarning
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Flags: &flags}); err != nil {
		t.Fatalf("Flags-only patch: %v", err)
	}
	got, err = st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("re-get deployment: %v", err)
	}
	if !got.CancelRequested || got.Flags != DeployFlagInstabilityWarning {
		t.Fatalf("field patch not applied: cancel=%v flags=%d", got.CancelRequested, got.Flags)
	}

	// 带 PrevStatus 的合法 CAS：不受影响。
	from := DeployQueued
	to := DeployPreparing
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &to, PrevStatus: &from}); err != nil {
		t.Fatalf("CAS with PrevStatus: %v", err)
	}
}

// TestGitSHADedupConcurrentSingleRow M3-4 回归：并发两路同 sha 入队（各在
// 自身事务内 COUNT 复查 + INSERT——与 DeployFromCommit 的 DedupeSHA 事务
// 形态一致），断言仅落一行部署（BEGIN IMMEDIATE 写锁串行化下，后到事务
// 的 COUNT 看到先行事务已提交的行并放弃）。
func TestGitSHADedupConcurrentSingleRow(t *testing.T) {
	ctx := context.Background()
	st := newDeployStore(t)
	app, err := st.CreateApp(ctx, "", "dedup-app")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	// enqueueDedup 复刻 DeployFromCommit 的 M3-4 事务原语形态（COUNT 复查
	// + CreateDeployment 同 InTx；命中判重返回 ErrDuplicateGitDeployment）。
	enqueueDedup := func() error {
		return st.InTx(ctx, func(tx *Tx) error {
			n, err := tx.CountGitDeploymentsForSHA(ctx, app.ID, sha)
			if err != nil {
				return err
			}
			if n > 0 {
				return ErrDuplicateGitDeployment
			}
			_, err = tx.CreateDeployment(ctx, DeployRecord{
				AppID: app.ID, AppName: app.Name, Kind: "deploy",
				SourceGitSHA: sha, SourceGitRef: "refs/heads/main",
			})
			return err
		})
	}

	const rounds = 8
	errs := make(chan error, rounds)
	for i := 0; i < rounds; i++ {
		go func() { errs <- enqueueDedup() }()
	}
	wins, dups := 0, 0
	for i := 0; i < rounds; i++ {
		if err := <-errs; err == nil {
			wins++
		} else if errors.Is(err, ErrDuplicateGitDeployment) {
			dups++
		} else {
			t.Fatalf("unexpected enqueue error: %v", err)
		}
	}
	if wins != 1 || dups != rounds-1 {
		t.Fatalf("wins = %d, dups = %d, want 1 win / %d dups (仅一行落库)", wins, dups, rounds-1)
	}
	n, err := st.CountGitDeploymentsForSHA(ctx, app.ID, sha)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("deployments for sha = %d, want 1", n)
	}
}
