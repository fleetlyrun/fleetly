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
