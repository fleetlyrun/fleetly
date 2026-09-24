package api

// 备份/恢复/升级 RPC 的 API 面单测（E4 W4-S5）：受理语义（异步 accepted）、
// confirm 两段式、kind 收窄、编排哨兵的错误映射（E_S3_NOT_CONFIGURED /
// E_DB_NOT_FOUND / 升级无可升级目标）、台账只读投影、scope 登记面。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/database"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newOpsTestEnv 起带指定 ops 编排替身的 DatabaseService bufconn 环境（旋转
// 测试环境的 S5 变体——ops 由用例注入以驱动映射面）。
func newOpsTestEnv(t *testing.T, ops BackupOrchestrator) (*state.Store, *secrets.Box, serverv1.DatabaseServiceClient, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	auth := NewAuthenticator(st)
	srv := newAuthServer(auth)
	serverv1.RegisterDatabaseServiceServer(srv, NewDatabaseService(st, box, nil, &fakeRotator{}, ops))
	conn := serveBufconn(t, srv)
	token := seedTokenPlain(t, st, "admin")
	return st, box, serverv1.NewDatabaseServiceClient(conn), token
}

// seedReadyDB 落一个 ready 实例（API 同构造：凭据加密 + provisioning →
// ready 健康门推进；归属 = 确定性夹具项目）。
func seedReadyDB(t *testing.T, st *state.Store, box *secrets.Box, name string) state.DatabaseInstance {
	t.Helper()
	cipher, err := box.Encrypt([]byte("password-plaintext"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	proj := seedFixtureProject(t, st)
	inst, err := st.CreateDatabaseInstance(context.Background(), state.DatabaseInstance{
		Name:             name,
		Template:         "postgres-16",
		ImageDigest:      "postgres:16@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CredentialCipher: string(cipher),
		ProjectID:        proj.ID,
		TeamID:           proj.TeamID,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if err := st.EnterDbPhase(context.Background(), inst.ID, state.DatabaseProvisioning, state.DatabaseReady); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return inst
}

// TestTriggerBackupAcceptsAsync 受理语义：异步 accepted（kind=manual 回显），
// 编排端口恰好被调用一次。
func TestTriggerBackupAcceptsAsync(t *testing.T) {
	ops := &fakeOps{}
	st, box, cl, token := newOpsTestEnv(t, ops)
	seedReadyDB(t, st, box, "pg-api")
	ctx := authCtx(context.Background(), token)

	resp, err := cl.TriggerDatabaseBackup(ctx, &serverv1.TriggerDatabaseBackupRequest{Name: "pg-api"})
	if err != nil {
		t.Fatalf("TriggerDatabaseBackup: %v", err)
	}
	if resp.GetStatus() != "accepted" || resp.GetKind() != "manual" {
		t.Errorf("response = %+v, want accepted/manual (async acceptance)", resp)
	}
	if len(ops.calls) != 1 || ops.calls[0].op != "backup" || ops.calls[0].name != "pg-api" {
		t.Errorf("orchestrator calls = %v, want one backup call for pg-api", ops.calls)
	}
}

// TestTriggerBackupRejectsInternalKinds kind 收窄：daily/pre_upgrade 是平台
// 内部类别（调度/升级门），API 只受理 manual——受理面在编排之前拒绝。
func TestTriggerBackupRejectsInternalKinds(t *testing.T) {
	ops := &fakeOps{}
	st, box, cl, token := newOpsTestEnv(t, ops)
	seedReadyDB(t, st, box, "pg-kind")
	ctx := authCtx(context.Background(), token)

	if _, err := cl.TriggerDatabaseBackup(ctx, &serverv1.TriggerDatabaseBackupRequest{
		Name: "pg-kind", Kind: "daily",
	}); err == nil || !strings.Contains(err.Error(), "manual backups only") {
		t.Fatalf("kind=daily err = %v, want the manual-only refusal", err)
	}
	if len(ops.calls) != 0 {
		t.Errorf("orchestrator must not be invoked for rejected kinds, got %v", ops.calls)
	}
}

// TestTriggerBackupS3UnsetMapping S3 未配置的映射面：编排哨兵 →
// E_S3_NOT_CONFIGURED 信封（与引擎注入前哨同码同语义的诚实拒绝）。
func TestTriggerBackupS3UnsetMapping(t *testing.T) {
	ops := &fakeOps{err: database.ErrBackupS3NotConfigured}
	st, box, cl, token := newOpsTestEnv(t, ops)
	seedReadyDB(t, st, box, "pg-s3")
	ctx := authCtx(context.Background(), token)

	_, err := cl.TriggerDatabaseBackup(ctx, &serverv1.TriggerDatabaseBackupRequest{Name: "pg-s3"})
	envCode(t, err, "E_S3_NOT_CONFIGURED")
	if !strings.Contains(err.Error(), "object storage is not configured") {
		t.Errorf("err = %v, want the honest s3-unset reason", err)
	}
}

// TestRestoreConfirmAndOrchestration 恢复受理：confirm 两段式（缺失 400）+
// confirm 正确时编排放行 → accepted + 快照回显。
func TestRestoreConfirmAndOrchestration(t *testing.T) {
	ops := &fakeOps{}
	st, box, cl, token := newOpsTestEnv(t, ops)
	seedReadyDB(t, st, box, "pg-rest")
	ctx := authCtx(context.Background(), token)

	if _, err := cl.RestoreDatabaseBackup(ctx, &serverv1.RestoreDatabaseBackupRequest{
		Name: "pg-rest", Snapshot: "snap-x",
	}); err == nil || !strings.Contains(err.Error(), `confirm="pg-rest"`) {
		t.Fatalf("missing confirm err = %v, want the two-phase confirm guidance", err)
	}
	resp, err := cl.RestoreDatabaseBackup(ctx, &serverv1.RestoreDatabaseBackupRequest{
		Name: "pg-rest", Snapshot: "snap-x", Confirm: "pg-rest",
	})
	if err != nil {
		t.Fatalf("restore accept: %v", err)
	}
	if resp.GetStatus() != "accepted" || resp.GetSnapshot() != "snap-x" {
		t.Errorf("response = %+v, want accepted + snapshot echo", resp)
	}
	if len(ops.calls) != 1 || ops.calls[0].op != "restore" {
		t.Errorf("orchestrator calls = %v, want one restore call", ops.calls)
	}
}

// TestUpgradeNotFoundMapping 目标不存在 → E_DB_NOT_FOUND（404 族）。
func TestUpgradeNotFoundMapping(t *testing.T) {
	ops := &fakeOps{}
	_, _, cl, token := newOpsTestEnv(t, ops)
	ctx := authCtx(context.Background(), token)
	_, err := cl.UpgradeDatabase(ctx, &serverv1.UpgradeDatabaseRequest{Name: "ghost", Confirm: "ghost"})
	envCode(t, err, "E_DB_NOT_FOUND")
}

// TestUpgradeConfirmRequired 升级 confirm 两段式（与 delete/restore 同型的
// 数据安全面）。
func TestUpgradeConfirmRequired(t *testing.T) {
	ops := &fakeOps{}
	st, box, cl, token := newOpsTestEnv(t, ops)
	seedReadyDB(t, st, box, "pg-upc")
	ctx := authCtx(context.Background(), token)
	if _, err := cl.UpgradeDatabase(ctx, &serverv1.UpgradeDatabaseRequest{Name: "pg-upc"}); err == nil ||
		!strings.Contains(err.Error(), `confirm="pg-upc"`) {
		t.Fatalf("missing confirm err = %v, want the two-phase confirm guidance", err)
	}
	if len(ops.calls) != 0 {
		t.Errorf("orchestrator must not run without confirm, got %v", ops.calls)
	}
}

// TestUpgradeNotAvailableMapping 无可升级目标 → E_STATE_VERSION_CONFLICT 族
// 信封（up_to_date 是升级的非法前置事实，零新增码）。
func TestUpgradeNotAvailableMapping(t *testing.T) {
	ops := &fakeOps{err: database.ErrUpgradeNotAvailable{Instance: "pg-x", Current: "cur", Template: "cur"}}
	st, box, cl, token := newOpsTestEnv(t, ops)
	seedReadyDB(t, st, box, "pg-x")
	ctx := authCtx(context.Background(), token)
	_, err := cl.UpgradeDatabase(ctx, &serverv1.UpgradeDatabaseRequest{Name: "pg-x", Confirm: "pg-x"})
	envCode(t, err, "E_STATE_VERSION_CONFLICT")
}

// TestListDatabaseBackupsProjection 台账只读投影（全量事实面）。
func TestListDatabaseBackupsProjection(t *testing.T) {
	ops := &fakeOps{}
	st, box, cl, token := newOpsTestEnv(t, ops)
	inst := seedReadyDB(t, st, box, "pg-list")
	ctx := authCtx(context.Background(), token)
	if _, err := st.InsertDatabaseBackup(ctx, state.DatabaseBackup{
		DatabaseID:     inst.ID,
		Kind:           state.DatabaseBackupDaily,
		ResticSnapshot: "snap-list-1",
		SizeBytes:      4096,
		VerifyStatus:   state.DatabaseVerifyVerified,
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	resp, err := cl.ListDatabaseBackups(ctx, &serverv1.ListDatabaseBackupsRequest{Name: "pg-list"})
	if err != nil {
		t.Fatalf("ListDatabaseBackups: %v", err)
	}
	if len(resp.GetBackups()) != 1 {
		t.Fatalf("rows = %d, want 1", len(resp.GetBackups()))
	}
	b := resp.GetBackups()[0]
	if b.GetKind() != "daily" || b.GetSnapshot() != "snap-list-1" || b.GetSizeBytes() != 4096 || b.GetVerifyStatus() != "verified" {
		t.Errorf("row = %+v, want the full fact projection", b)
	}
	if b.GetCreatedAt() == nil {
		t.Error("created_at missing (ledger row projection)")
	}
}

// TestBackupRPCScopeRegistrations scope 登记面（验收：备份列表 = read；触
// 发/恢复/升级 = admin——破坏性/写面语义与 delete 同级）。
func TestBackupRPCScopeRegistrations(t *testing.T) {
	for method, want := range map[string]string{
		"/fleetly.server.v1.DatabaseService/TriggerDatabaseBackup": ScopeAdmin,
		"/fleetly.server.v1.DatabaseService/ListDatabaseBackups":   ScopeRead,
		"/fleetly.server.v1.DatabaseService/RestoreDatabaseBackup": ScopeAdmin,
		"/fleetly.server.v1.DatabaseService/UpgradeDatabase":       ScopeAdmin,
	} {
		if got, ok := RequiredScope(method); !ok || got != want {
			t.Errorf("scope(%s) = (%q,%v), want (%q,true)", method, got, ok, want)
		}
	}
}
