package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// db_references / db_backups / app_secrets 的 CRUD 验收（managed-databases
// 设计 §2.4/§2.6/§2.7，E4 S1）。

// createTestDatabase 是库实例测试夹具（返回 ID；归属 = 独立夹具项目）。
func createTestDatabase(t *testing.T, st *Store, name string) string {
	t.Helper()
	proj := seedFixtureProject(t, st)
	row, err := st.CreateDatabaseInstance(context.Background(), DatabaseInstance{
		Name: name, Template: "postgres-16", ImageDigest: "postgres:16@sha256:aaa",
		CredentialCipher: "age-cipher",
		ProjectID:        proj.ID, TeamID: proj.TeamID,
	})
	if err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	return row.ID
}

// TestDatabaseReferencesLifecycle 引用倒排登记：upsert 幂等、全清 + 替换
// 语义（planner 每部署重建）、按 db 反查（删除守卫数据源）、按 app 正查、
// FK 完整性（不存在的库/app 拒绝）。
func TestDatabaseReferencesLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	dbID := createTestDatabase(t, st, "pg-ref")
	appID := createTestApp(t, st, "ref-app")

	ref := func(service, prefix string) DatabaseReference {
		return DatabaseReference{DatabaseID: dbID, AppID: appID, Service: service, EnvPrefix: prefix}
	}
	// 两次登记（同 service 幂等覆盖 env_prefix）。
	if err := st.UpsertDatabaseReference(ctx, ref("web", "FLEETLY_DB_PG_REF")); err != nil {
		t.Fatalf("upsert ref: %v", err)
	}
	if err := st.UpsertDatabaseReference(ctx, ref("web", "FLEETLY_DB_PG_REF")); err != nil {
		t.Fatalf("re-upsert ref: %v", err)
	}
	if err := st.UpsertDatabaseReference(ctx, ref("worker", "FLEETLY_DB_PG_REF")); err != nil {
		t.Fatalf("upsert worker ref: %v", err)
	}
	if err := st.UpsertDatabaseReference(ctx, DatabaseReference{DatabaseID: dbID, AppID: appID, Service: "web"}); err == nil {
		t.Fatal("ref without env_prefix accepted")
	}

	byDB, err := st.ListDatabaseReferencesByDB(ctx, dbID)
	if err != nil || len(byDB) != 2 {
		t.Fatalf("list by db = %+v err=%v, want 2 refs", byDB, err)
	}
	if byDB[0].Service != "web" || byDB[1].Service != "worker" || byDB[0].EnvPrefix != "FLEETLY_DB_PG_REF" {
		t.Fatalf("refs ordered/filled wrong: %+v", byDB)
	}
	if byDB[0].CreatedAt.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("created_at not stamped: %v", byDB[0].CreatedAt)
	}

	// 全清 + 替换（label 移除 = 行删除；planner 同事务先清后插）。
	n, err := st.DeleteDatabaseReferencesForApp(ctx, appID)
	if err != nil || n != 2 {
		t.Fatalf("delete for app = %d err=%v, want 2", n, err)
	}
	if byApp, err := st.ListDatabaseReferencesByApp(ctx, appID); err != nil || len(byApp) != 0 {
		t.Fatalf("list after clear = %+v err=%v, want empty", byApp, err)
	}
	// 幂等全清。
	if n, err := st.DeleteDatabaseReferencesForApp(ctx, appID); err != nil || n != 0 {
		t.Fatalf("idempotent clear = %d err=%v, want 0/nil", n, err)
	}

	// FK 完整性：不存在的库实例拒绝。
	err = st.UpsertDatabaseReference(ctx, DatabaseReference{DatabaseID: "01JMISSING", AppID: appID, Service: "web", EnvPrefix: "X"})
	if err == nil {
		t.Fatal("reference to missing database accepted (FK guard off?)")
	}
}

// TestDatabaseBackupsLedger 备份台账：落账缺省 unverified、按库倒序列表、
// verify 三态置位、词表外 kind/status 拒绝、行缺失显式化。
func TestDatabaseBackupsLedger(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	dbID := createTestDatabase(t, st, "pg-bk")

	first, err := st.InsertDatabaseBackup(ctx, DatabaseBackup{
		DatabaseID: dbID, Kind: DatabaseBackupDaily, ResticSnapshot: "snap-0001", SizeBytes: 1234,
	})
	if err != nil {
		t.Fatalf("insert backup: %v", err)
	}
	if first.VerifyStatus != DatabaseVerifyUnverified {
		t.Fatalf("initial verify_status = %s, want unverified", first.VerifyStatus)
	}
	second, err := st.InsertDatabaseBackup(ctx, DatabaseBackup{
		DatabaseID: dbID, Kind: DatabaseBackupPreUpgrade, ResticSnapshot: "snap-0002",
	})
	if err != nil {
		t.Fatalf("insert second backup: %v", err)
	}

	// 词表外 kind 拒绝。
	if _, err := st.InsertDatabaseBackup(ctx, DatabaseBackup{
		DatabaseID: dbID, Kind: "bogus", ResticSnapshot: "snap-0003",
	}); err == nil {
		t.Fatal("bogus backup kind accepted")
	}
	if _, err := st.InsertDatabaseBackup(ctx, DatabaseBackup{DatabaseID: dbID, Kind: DatabaseBackupManual}); err == nil {
		t.Fatal("backup without snapshot accepted")
	}

	rows, err := st.ListDatabaseBackups(ctx, dbID, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = %+v err=%v, want 2", rows, err)
	}
	// 倒序（最新在前）。
	if rows[0].ResticSnapshot != "snap-0002" || rows[1].ResticSnapshot != "snap-0001" {
		t.Fatalf("list order wrong: %+v", rows)
	}

	// verify 置位（成功清 error、失败带摘要）。
	if err := st.UpdateDatabaseBackupVerifyStatus(ctx, second.ID, DatabaseVerifyVerified, ""); err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	if err := st.UpdateDatabaseBackupVerifyStatus(ctx, first.ID, DatabaseVerifyFailed, "pg_restore --list failed"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	got, err := st.GetDatabaseBackup(ctx, first.ID)
	if err != nil || got.VerifyStatus != DatabaseVerifyFailed || got.Error != "pg_restore --list failed" {
		t.Fatalf("verify status = %+v err=%v", got, err)
	}
	if err := st.UpdateDatabaseBackupVerifyStatus(ctx, "01JMISSING", DatabaseVerifyVerified, ""); !errors.Is(err, ErrDatabaseBackupNotFound) {
		t.Fatalf("verify on missing row err = %v, want ErrDatabaseBackupNotFound", err)
	}
	if err := st.UpdateDatabaseBackupVerifyStatus(ctx, first.ID, "bogus", ""); err == nil {
		t.Fatal("bogus verify status accepted")
	}
}

// TestAppSecretsStore 平台密钥库：upsert 轮换（hash8 同拍重盖）、无值读回
// 面（GetAppSecretCipher 只回密文）、列表序、删除幂等显式化、写通道校验。
func TestAppSecretsStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	appID := createTestApp(t, st, "secret-app")

	sec, err := st.UpsertAppSecret(ctx, AppSecret{
		AppID: appID, Name: "API_KEY", ValueCipher: "age-cipher-v1", Hash8: "aaaa1111",
	})
	if err != nil {
		t.Fatalf("upsert secret: %v", err)
	}
	if sec.CreatedAt.IsZero() || sec.UpdatedAt.IsZero() {
		t.Fatal("timestamps not stamped")
	}

	// 覆盖即轮换（cipher + hash8 同拍重盖）。
	rotated, err := st.UpsertAppSecret(ctx, AppSecret{
		AppID: appID, Name: "API_KEY", ValueCipher: "age-cipher-v2", Hash8: "bbbb2222",
	})
	if err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	if rotated.ValueCipher != "age-cipher-v2" || rotated.Hash8 != "bbbb2222" {
		t.Fatalf("rotated secret = %+v, want new cipher + hash8", rotated)
	}
	// updated_at 不早于 created_at（同拍覆盖时相等——Windows 粗时钟下
	// 两次 nowNano 可能同值，只断言不倒退）。
	if rotated.UpdatedAt.Before(rotated.CreatedAt) {
		t.Fatalf("updated_at regressed: created=%v updated=%v", rotated.CreatedAt, rotated.UpdatedAt)
	}

	// 第二条 + 列表（name 字典序）。
	if _, err := st.UpsertAppSecret(ctx, AppSecret{AppID: appID, Name: "AAA_KEY", ValueCipher: "age-c", Hash8: "cccc3333"}); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	rows, err := st.ListAppSecrets(ctx, appID)
	if err != nil || len(rows) != 2 || rows[0].Name != "AAA_KEY" || rows[1].Name != "API_KEY" {
		t.Fatalf("list = %+v err=%v, want [AAA_KEY API_KEY]", rows, err)
	}

	// 密文装载面（无明文读回）。
	cipher, err := st.GetAppSecretCipher(ctx, appID, "API_KEY")
	if err != nil || cipher != "age-cipher-v2" {
		t.Fatalf("cipher = %q err=%v", cipher, err)
	}
	if _, err := st.GetAppSecretCipher(ctx, appID, "MISSING"); !errors.Is(err, ErrAppSecretNotFound) {
		t.Fatalf("missing cipher err = %v, want ErrAppSecretNotFound", err)
	}

	// 写通道校验（密文/hash8 必填）。
	for _, bad := range []AppSecret{
		{AppID: appID, Name: "X", ValueCipher: "", Hash8: "dddd4444"},
		{AppID: appID, Name: "X", ValueCipher: "age-c", Hash8: ""},
		{AppID: "", Name: "X", ValueCipher: "age-c", Hash8: "dddd4444"},
		{AppID: appID, Name: "", ValueCipher: "age-c", Hash8: "dddd4444"},
	} {
		if _, err := st.UpsertAppSecret(ctx, bad); err == nil {
			t.Errorf("bad secret accepted: %+v", bad)
		}
	}

	// 删除（幂等显式化）。
	if err := st.RemoveAppSecret(ctx, appID, "AAA_KEY"); err != nil {
		t.Fatalf("remove secret: %v", err)
	}
	if err := st.RemoveAppSecret(ctx, appID, "AAA_KEY"); !errors.Is(err, ErrAppSecretNotFound) {
		t.Fatalf("idempotent remove err = %v, want ErrAppSecretNotFound", err)
	}
}
