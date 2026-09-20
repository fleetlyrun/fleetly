package statebackup

// X-7/MG-3（B6）生命周期与孤儿回收回归测试：
//   - Stop 等待在途 post-deploy 备份（引擎成功路径逸出的异步挂钩——此前
//     只等 daily 循环，关停时在途 VACUUM 与 Store.Close 竞态）；
//   - prune 的孤儿目录扫描：无台账行、mtime 超宽容期的 ULID 目录被回收 +
//     backup.pruned_orphan 审计；台账内目录、新鲜孤儿、非 ULID 目录不动。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestStopWaitsForInFlightPostDeploy 在途等待：慢校验（门闸注入）期间 Stop
// 阻塞；备份完成后 Stop 返回且台账落定（在途快照未被关停杀死）。
func TestStopWaitsForInFlightPostDeploy(t *testing.T) {
	mgr, st, _ := newTestManager(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	mgr.verifyFn = func(dbPath string) (snapshotFacts, error) {
		entered <- struct{}{}
		<-release
		return verifySnapshotFile(dbPath)
	}

	go mgr.RunPostDeploy(state.DeployRecord{ID: "d-inflight", AppName: "demo"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("post-deploy backup did not reach verify within 5s")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- mgr.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned while post-deploy backup in flight: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Stop 仍在等待（预算内阻塞）✓
	}

	close(release)
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return after in-flight backup finished")
	}
	rows := backupRows(t, st)
	if len(rows) != 1 || rows[0].Kind != state.BackupKindPostDeploy ||
		rows[0].VerifyStatus != state.BackupVerifyVerified {
		t.Fatalf("ledger after stop = %+v, want 1 verified post_deploy row", rows)
	}
}

// TestStopReturnsWhenBudgetExhausted 预算让位：在途备份超出 Stop 预算时
// Stop 按时返回（不无限阻塞关停链），在途 goroutine 随进程退出兜底。
func TestStopReturnsWhenBudgetExhausted(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	mgr.verifyFn = func(dbPath string) (snapshotFacts, error) {
		entered <- struct{}{}
		<-release
		return verifySnapshotFile(dbPath)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.RunPostDeploy(state.DeployRecord{ID: "d-stuck", AppName: "demo"})
	}()
	<-entered

	// Stop 带短 deadline（预算取小语义：min(ctx 剩余, 60s)）。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := mgr.Stop(ctx); err != nil {
		t.Fatalf("Stop with exhausted budget must return nil, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Stop blocked %s beyond its budget", elapsed)
	}
	// 断言后显式放行并 join 在途 goroutine：t.TempDir 的 RemoveAll 不得与
	// 在途写并发（Windows 下新建中的文件阻断目录删除——unlinkat 报
	// directory not empty 的形态即此竞态）。
	close(release)
	<-done
}

// TestPruneReclaimsOrphanBackupDirs 孤儿目录回收（MG-3 资源台账兜底对账）：
// 老（超宽容期）孤儿被删 + backup.pruned_orphan 审计；台账内目录、新鲜
// 孤儿（在途宽容）、非 ULID 词形目录均不动。
func TestPruneReclaimsOrphanBackupDirs(t *testing.T) {
	mgr, st, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := mgr.Trigger(ctx, state.BackupKindDaily); err != nil {
		t.Fatalf("seed trigger: %v", err)
	}
	rows := backupRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("seed ledger rows = %d, want 1", len(rows))
	}
	ledgerDir := filepath.Dir(rows[0].Path)

	oldOrphan := filepath.Join(mgr.Dir(), ulid.Make().String())
	freshOrphan := filepath.Join(mgr.Dir(), ulid.Make().String())
	junkDir := filepath.Join(mgr.Dir(), "operator-notes") // 非 ULID 词形
	for _, dir := range []string{oldOrphan, freshOrphan, junkDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, backupDBName), []byte("half-baked"), 0o600); err != nil {
			t.Fatalf("seed orphan content: %v", err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{oldOrphan, junkDir} { // junk 同样置老：词形守卫独立于年龄
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatalf("chtimes %s: %v", dir, err)
		}
	}

	// 再成功备份一拍 → prune（含孤儿扫描）执行。
	if _, err := mgr.Trigger(ctx, state.BackupKindDaily); err != nil {
		t.Fatalf("second trigger: %v", err)
	}

	if _, err := os.Stat(oldOrphan); !os.IsNotExist(err) {
		t.Fatalf("old orphan dir still present (stat err=%v)", err)
	}
	if _, err := os.Stat(freshOrphan); err != nil {
		t.Fatalf("fresh orphan removed (grace period violated): %v", err)
	}
	if _, err := os.Stat(junkDir); err != nil {
		t.Fatalf("non-ULID dir removed (shape guard violated): %v", err)
	}
	if _, err := os.Stat(ledgerDir); err != nil {
		t.Fatalf("ledger-backed dir removed: %v", err)
	}
	found := false
	for _, a := range auditActions(t, st) {
		if a.Action == "backup.pruned_orphan" {
			found = true
			if a.Result != "ok" {
				t.Fatalf("backup.pruned_orphan result = %s, want ok", a.Result)
			}
		}
	}
	if !found {
		t.Fatal("no backup.pruned_orphan audit entry")
	}
}
