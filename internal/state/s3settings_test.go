package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

// platform_settings + s3.* 设置面测试（E3-2）：往返、互斥校验四负路径、
// 公网开关门禁、审计/事件脱敏（secret 材料零出现）、迁移 up/down。

// newSettingsStore 起一个独立临时库（全迁移已应用）。
func newSettingsStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestS3SettingsRoundtrip 保存 → 读取逐字段一致；空库缺省态 = unset。
func TestS3SettingsRoundtrip(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	got, err := st.LoadS3Settings(ctx)
	if err != nil {
		t.Fatalf("LoadS3Settings (empty): %v", err)
	}
	if got.Mode != S3ModeUnset || got.EndpointURL != "" || got.PublicExposed {
		t.Fatalf("empty-db settings = %+v, want unset defaults", got)
	}

	// storedForm 是测试用「存储形态」标记值——state 层存密文不解释，这里
	// 只验证原样存取；非真实凭证（envelope 加解密边界测试在 api 面）。
	storedForm := "ciphertext-not-plaintext"
	in := S3Settings{
		Mode:            S3ModeExternal,
		EndpointURL:     "https://s3.example.com",
		Region:          "us-east-1",
		Bucket:          "fleetly",
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: storedForm,
		PathStyle:       true,
	}
	if err := st.SaveS3Settings(ctx, in, S3SaveOptions{Actor: "human", ActorTokenID: "tok_1"}); err != nil {
		t.Fatalf("SaveS3Settings: %v", err)
	}
	got, err = st.LoadS3Settings(ctx)
	if err != nil {
		t.Fatalf("LoadS3Settings: %v", err)
	}
	in.UpdatedAt = got.UpdatedAt // 时间列是保存侧分配的，不参与字段比对
	if got != in {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, in)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should be set after save")
	}
}

// TestS3SettingsValidation 互斥校验（设计 §2.2）四负路径 + 门禁正路径。
func TestS3SettingsValidation(t *testing.T) {
	external := S3Settings{
		Mode: S3ModeExternal, EndpointURL: "https://s3.example.com", Bucket: "fleetly",
		AccessKeyID: "AKID", SecretAccessKey: "enc",
	}
	cases := []struct {
		name       string
		in         S3Settings
		baseDomain string
		wantCode   string
	}{
		{
			name:     "external missing secret",
			in:       S3Settings{Mode: S3ModeExternal, EndpointURL: "https://s3.example.com", Bucket: "b", AccessKeyID: "a"},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "external missing endpoint",
			in:       S3Settings{Mode: S3ModeExternal, Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s"},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "rustfs with external fields set",
			in:       S3Settings{Mode: S3ModeRustfs, EndpointURL: "https://s3.example.com"},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "public_exposed on external mode",
			in:       S3Settings{Mode: S3ModeExternal, EndpointURL: "https://s3.example.com", Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s", PublicExposed: true},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "public_exposed rustfs without base_domain",
			in:       S3Settings{Mode: S3ModeRustfs, PublicExposed: true},
			wantCode: "E_S3_PUBLIC_REQUIRES_BASE_DOMAIN",
		},
		{
			name:     "invalid mode value",
			in:       S3Settings{Mode: "minio"},
			wantCode: "", // 非 E_ 码：plain error（proto 面另有 buf.validate 词表约束）
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateS3Settings(tc.in, tc.baseDomain)
			if err == nil {
				t.Fatalf("ValidateS3Settings(%+v) should fail", tc.in)
			}
			if tc.wantCode == "" {
				return
			}
			if !strings.Contains(err.Error(), tc.wantCode) {
				t.Fatalf("error = %q, want code %s", err.Error(), tc.wantCode)
			}
		})
	}

	// 正路径：rustfs + public_exposed + base_domain 齐 → 通过；rustfs 四项
	// 全空 → 通过。
	if err := ValidateS3Settings(S3Settings{Mode: S3ModeRustfs, PublicExposed: true}, "example.com"); err != nil {
		t.Fatalf("rustfs+public+base_domain should pass: %v", err)
	}
	if err := ValidateS3Settings(S3Settings{Mode: S3ModeRustfs}, ""); err != nil {
		t.Fatalf("bare rustfs should pass: %v", err)
	}
	if err := ValidateS3Settings(external, ""); err != nil {
		t.Fatalf("complete external should pass: %v", err)
	}
}

// TestS3SettingsSaveRejectsInvalid 保存路径复跑同一校验（fail-fast）：
// 非法设置不落库、不写审计/事件。
func TestS3SettingsSaveRejectsInvalid(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	err := st.SaveS3Settings(ctx, S3Settings{Mode: S3ModeExternal, Bucket: "b"}, S3SaveOptions{Actor: "human"})
	if err == nil || !strings.Contains(err.Error(), "E_S3_CONFIG_CONFLICT") {
		t.Fatalf("SaveS3Settings invalid should fail with E_S3_CONFIG_CONFLICT, got: %v", err)
	}
	got, err := st.LoadS3Settings(ctx)
	if err != nil {
		t.Fatalf("LoadS3Settings: %v", err)
	}
	if got.Mode != S3ModeUnset {
		t.Fatalf("invalid save must not persist, got mode %s", got.Mode)
	}
}

// TestS3SettingsAuditAndEventRedacted 审计 + 事件同事务落库（fail-closed
// 面）且脱敏：diff 摘要与事件 payload 只含 mode/public_exposed，secret
// 材料零出现（state-model §2.9，负面测试钉死）。
func TestS3SettingsAuditAndEventRedacted(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()
	const plaintextMarker = "PLAINTEXT-SECRET-9f8e7d6c"
	in := S3Settings{
		Mode: S3ModeExternal, EndpointURL: "https://s3.example.com", Bucket: "fleetly",
		AccessKeyID: "AKIDEXAMPLE",
		// 存储形态 = 密文；用明文标记当输入，验证它不出现在审计/事件。
		SecretAccessKey: plaintextMarker,
	}
	if err := st.SaveS3Settings(ctx, in, S3SaveOptions{Actor: "human", ActorTokenID: "tok_e"}); err != nil {
		t.Fatalf("SaveS3Settings: %v", err)
	}

	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var auditAction string
	for _, a := range audits {
		if a.Action == "s3.updated" {
			auditAction = a.Action
			if strings.Contains(a.DiffSummary, plaintextMarker) || strings.Contains(a.DiffSummary, in.AccessKeyID) {
				t.Fatalf("audit diff leaks secret material: %s", a.DiffSummary)
			}
			if !strings.Contains(a.DiffSummary, S3ModeExternal) {
				t.Fatalf("audit diff should carry mode: %s", a.DiffSummary)
			}
		}
	}
	if auditAction != "s3.updated" {
		t.Fatal("audit action s3.updated missing")
	}

	events, err := st.EventsSince(ctx, 0, 10)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	var found bool
	for _, ev := range events {
		if ev.Name != "s3.updated" {
			continue
		}
		found = true
		if strings.Contains(ev.Payload, plaintextMarker) || strings.Contains(ev.Payload, in.AccessKeyID) {
			t.Fatalf("event payload leaks secret material: %s", ev.Payload)
		}
		if !strings.Contains(ev.Payload, S3ModeExternal) {
			t.Fatalf("event payload should carry mode: %s", ev.Payload)
		}
	}
	if !found {
		t.Fatal("event s3.updated missing")
	}
}

// TestPlatformSettingsMigrationUpDown 迁移 00011 的 Up/Down 往返（goose
// 演练；生产回滚 = 恢复快照——Down 仅证明回滚 SQL 可执行）。
func TestPlatformSettingsMigrationUpDown(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fsys, err := migrationFiles()
	if err != nil {
		t.Fatalf("migrationFiles: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, fsys)
	if err != nil {
		t.Fatalf("goose provider: %v", err)
	}
	ctx := context.Background()
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !tableExists(t, db, "platform_settings") {
		t.Fatal("platform_settings should exist after Up")
	}
	// 行为冒烟：PK 冲突即更新。
	if _, err := db.Exec(`INSERT INTO platform_settings (key, value, updated_at) VALUES ('s3.mode', 'external', 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO platform_settings (key, value, updated_at) VALUES ('s3.mode', 'rustfs', 2)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM platform_settings WHERE key = 's3.mode'`).Scan(&v); err != nil || v != "rustfs" {
		t.Fatalf("upsert result = %s (err %v), want rustfs", v, err)
	}

	// Down 到 00010：表删除；再 Up：恢复。
	if _, err := provider.DownTo(ctx, 10); err != nil {
		t.Fatalf("DownTo 10: %v", err)
	}
	if tableExists(t, db, "platform_settings") {
		t.Fatal("platform_settings should be gone after DownTo 10")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("Up (again): %v", err)
	}
	if !tableExists(t, db, "platform_settings") {
		t.Fatal("platform_settings should exist after re-Up")
	}
}

// tableExists 探测表存在性。
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var one string
	err := db.QueryRow(`SELECT '1' FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&one)
	return err == nil
}
