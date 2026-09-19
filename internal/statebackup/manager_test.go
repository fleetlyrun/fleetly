package statebackup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newTestManager 构造真实 Store + 真实密钥 + 临时目录的 Manager（校验走
// 真实路径——VACUUM INTO 产物可被重新打开校验；失败注入经 verifyFn 接缝）。
func newTestManager(t *testing.T, mutate ...func(*Config)) (*Manager, *state.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "fleetly.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	cfg := Config{Dir: filepath.Join(dir, "backups"), Keep: 2}
	for _, m := range mutate {
		m(&cfg)
	}
	mgr, err := NewManager(cfg, cfg.Dir, box.Path(), "test-v1", st, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, st, dir
}

// testLogger 是静默日志器（告警面由台账/审计断言承载，日志不进断言）。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// readManifest 解码 manifest.json（断言辅助）。
func readManifest(t *testing.T, raw []byte) Manifest {
	t.Helper()
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

// verifyLedger 读取台账与审计，供断言。
func backupRows(t *testing.T, st *state.Store) []state.StateBackup {
	t.Helper()
	rows, err := st.ListStateBackups(context.Background(), 0)
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	return rows
}

func auditActions(t *testing.T, st *state.Store) []state.AuditRecord {
	t.Helper()
	rows, err := st.RecentAudits(context.Background(), 50)
	if err != nil {
		t.Fatalf("recent audits: %v", err)
	}
	return rows
}

// TestTriggerVerifiedHappyPath 全链路：真实快照 + 真实回读校验 → 台账
// verified + manifest 落盘（含密钥指纹/schema 版本/表行数）+ 组件健康。
func TestTriggerVerifiedHappyPath(t *testing.T) {
	mgr, st, dir := newTestManager(t)
	// 台账里已有行（快照抽查含 state_backups 行数 > 0 的可对照事实）。
	if _, err := st.RecordStateBackup(context.Background(), state.BackupWrite{
		Kind: state.BackupKindManual, Path: "synthetic", Verify: state.BackupVerifyVerified,
	}); err != nil {
		t.Fatalf("seed ledger row: %v", err)
	}

	rec, err := mgr.Trigger(context.Background(), state.BackupKindPreUpgrade)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.VerifyStatus != state.BackupVerifyVerified {
		t.Fatalf("verify_status = %s, want verified", rec.VerifyStatus)
	}
	if rec.Kind != state.BackupKindPreUpgrade {
		t.Fatalf("kind = %s, want pre_upgrade", rec.Kind)
	}

	// manifest 事实核对。
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(rec.Path), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	man := readManifest(t, raw)
	if man.SHA256 != rec.SHA256 || man.SizeBytes != rec.SizeBytes {
		t.Fatalf("manifest sha256/size mismatch with ledger: %+v vs %+v", man, rec)
	}
	if man.VerifyStatus != state.BackupVerifyVerified || man.Error != "" {
		t.Fatalf("manifest verify status = %s (%q), want verified/empty", man.VerifyStatus, man.Error)
	}
	if man.SchemaVersion < 8 {
		t.Fatalf("manifest schema_version = %d, want >= 8", man.SchemaVersion)
	}
	if len(man.KeyFingerprint) != 64 {
		t.Fatalf("manifest key_fingerprint = %q, want sha256 hex of key file", man.KeyFingerprint)
	}
	// 抽查行数 = 快照时刻的台账事实：种子行 1 行（本次备份行在快照之后落账，
	// 不在快照内——一致性快照的时点语义）。
	if man.Tables["state_backups"] != 1 {
		t.Fatalf("manifest state_backups count = %d, want 1 (snapshot-time fact)", man.Tables["state_backups"])
	}
	// 平台版本透传。
	if man.PlatformVersion != "test-v1" {
		t.Fatalf("manifest platform_version = %q", man.PlatformVersion)
	}
	_ = dir

	// 组件健康：verified 备份存在 → 健康。
	if err := mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth after verified backup: %v", err)
	}

	// 审计：backup.completed (ok)。
	found := false
	for _, a := range auditActions(t, st) {
		if a.Action == "backup.completed" {
			found = true
			if a.Result != "ok" {
				t.Fatalf("backup.completed result = %s, want ok", a.Result)
			}
		}
	}
	if !found {
		t.Fatal("no backup.completed audit entry")
	}
}

// TestVerifyFailureIsNeverGreen 负面测试（T2.22 验收标准 2）：verify 失败
// 注入 → 台账 failed 行 + backup.failed(result=error) 审计 + backup 组件
// 不健康 + manifest 如实标 failed——「verify 失败但台账绿色」路径不存在。
func TestVerifyFailureIsNeverGreen(t *testing.T) {
	mgr, st, _ := newTestManager(t)
	mgr.verifyFn = func(dbPath string) (snapshotFacts, error) {
		return snapshotFacts{}, errors.New("injected verify failure")
	}

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err == nil {
		t.Fatal("Trigger must return error on verify failure")
	}
	if rec.VerifyStatus != state.BackupVerifyFailed {
		t.Fatalf("ledger verify_status = %s, want failed (green-fake-success path exists!)", rec.VerifyStatus)
	}
	if rec.Error == "" || !strings.Contains(rec.Error, "injected verify failure") {
		t.Fatalf("ledger error = %q, want injected reason", rec.Error)
	}

	// 审计面：backup.failed result=error。
	found := false
	for _, a := range auditActions(t, st) {
		if a.Action == "backup.failed" {
			found = true
			if a.Result != "error" {
				t.Fatalf("backup.failed result = %s, want error", a.Result)
			}
			if !strings.Contains(a.DiffSummary, "injected verify failure") {
				t.Fatalf("audit diff_summary missing failure reason: %q", a.DiffSummary)
			}
		}
	}
	if !found {
		t.Fatal("no backup.failed audit entry — red alarm audit path missing")
	}

	// 组件面：backup 组件不健康，错误原文可行动。
	herr := mgr.CheckHealth()
	if herr == nil {
		t.Fatal("CheckHealth must be unhealthy while last backup failed")
	}
	if !strings.Contains(herr.Error(), "NOT verified") {
		t.Fatalf("health error should mention NOT verified: %v", herr)
	}
}

// TestVerifyFailureRealCorruption 真实损坏路径（不经接缝）：快照文件截断
// → 真实回读校验必须失败（驱动真实 verifySnapshotFile，而非注入）。
func TestVerifyFailureRealCorruption(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	snap := filepath.Join(dir, "corrupt.db")
	if err := st.VacuumInto(context.Background(), snap); err != nil {
		t.Fatalf("VacuumInto: %v", err)
	}
	// 截断破坏（SQLite 文件头之后掏空——integrity_check 必报或打开即失败）。
	full, err := os.ReadFile(snap) //nolint:gosec // G304：快照路径为测试受控的 TempDir 产物
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if err := os.WriteFile(snap, full[:len(full)/2], 0o600); err != nil { //nolint:gosec // 测试受控路径
		t.Fatalf("truncate snapshot: %v", err)
	}
	if _, err := verifySnapshotFile(snap); err == nil {
		t.Fatal("verifySnapshotFile must fail on truncated snapshot")
	}
}

// TestRetentionPrunesOldest 保留策略：keep=2，第三份落账后最旧行删行 +
// 审计 backup.pruned + 目录清除（台账不留幽灵行）。
func TestRetentionPrunesOldest(t *testing.T) {
	mgr, st, _ := newTestManager(t, func(c *Config) { c.Keep = 2 })
	for i := 0; i < 3; i++ {
		if _, err := mgr.Trigger(context.Background(), state.BackupKindDaily); err != nil {
			t.Fatalf("Trigger %d: %v", i, err)
		}
	}
	rows := backupRows(t, st)
	if len(rows) != 2 {
		t.Fatalf("ledger rows = %d, want 2 (keep)", len(rows))
	}
	pruned := false
	for _, a := range auditActions(t, st) {
		if a.Action == "backup.pruned" {
			pruned = true
		}
	}
	if !pruned {
		t.Fatal("no backup.pruned audit entry")
	}
	// 备份根下只剩 keep 个备份目录。
	entries, err := os.ReadDir(mgr.Dir())
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("backup dirs = %d, want 2", len(entries))
	}
}

// TestKeySeparationGuard 密钥分离：key_path 落在备份目录内 → 构造期拒绝。
func TestKeySeparationGuard(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	backupDir := filepath.Join(dir, "backups")
	if _, err := NewManager(Config{}, backupDir, filepath.Join(backupDir, "fleetly.key"),
		"v", st, testLogger()); err == nil {
		t.Fatal("NewManager must reject key inside backup dir")
	}
	// 运行期守卫：构造时分离、执行时密钥被挪进备份目录 → 备份失败且台账
	// 红色（fail 的失败落账路径）。
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "fleetly.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	mgr, err := NewManager(Config{}, backupDir, box.Path(), "v", st, testLogger())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// 把密钥复制进备份根并改写 keyPath 指向（模拟配置漂移后的执行期形态）。
	mgr.keyPath = filepath.Join(backupDir, "moved.key")
	if _, err := mgr.Trigger(context.Background(), state.BackupKindDaily); err == nil {
		t.Fatal("Trigger must fail when key lives inside backup dir at run time")
	}
	if rows := backupRows(t, st); len(rows) != 1 || rows[0].VerifyStatus != state.BackupVerifyFailed {
		t.Fatalf("ledger after key-separation failure = %+v, want single failed row", rows)
	}
}

// TestUnknownKindRejected kind 词表收口。
func TestUnknownKindRejected(t *testing.T) {
	mgr, _, _ := newTestManager(t)
	if _, err := mgr.Trigger(context.Background(), "bogus"); err == nil {
		t.Fatal("unknown kind must be rejected")
	}
}

// TestLedgerFailedRowRequiresError 台账 fail 行必须有 error 原文。
func TestLedgerFailedRowRequiresError(t *testing.T) {
	mgr, st, _ := newTestManager(t)
	_, err := st.RecordStateBackup(context.Background(), state.BackupWrite{
		Kind: state.BackupKindManual, Path: "x", Verify: state.BackupVerifyFailed,
	})
	if err == nil {
		t.Fatal("failed row without error detail must be rejected")
	}
	_ = mgr
}
