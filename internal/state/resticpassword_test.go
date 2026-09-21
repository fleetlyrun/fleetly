package state

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// restic repo 口令内部键 + state_backups 上传列的存储面测试（E3-3）：
// 口令密文存取、内部键不泄进 s3.* typed 设置面（负面测试）、PUT 语义
// 不清口令；上传结论落账（词表校验/截断/回读）。

// TestResticPasswordRoundtrip 惰性生成路径的两步存取：未生成时
// found=false；保存后原样读回（state 层存密文不解释）。
func TestResticPasswordRoundtrip(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	_, found, err := st.LoadResticPasswordCiphertext(ctx)
	if err != nil {
		t.Fatalf("LoadResticPasswordCiphertext (fresh): %v", err)
	}
	if found {
		t.Fatal("fresh store must not report a stored restic password")
	}

	const ct = "age-ciphertext-marker-not-real"
	if err := st.SaveResticPasswordCiphertext(ctx, ct); err != nil {
		t.Fatalf("SaveResticPasswordCiphertext: %v", err)
	}
	got, found, err := st.LoadResticPasswordCiphertext(ctx)
	if err != nil || !found {
		t.Fatalf("LoadResticPasswordCiphertext: found=%v err=%v, want stored", found, err)
	}
	if got != ct {
		t.Fatalf("roundtrip = %q, want %q (state layer must not interpret ciphertext)", got, ct)
	}
	// 幂等 upsert（同键覆写）。
	if err := st.SaveResticPasswordCiphertext(ctx, ct+"-2"); err != nil {
		t.Fatalf("SaveResticPasswordCiphertext (overwrite): %v", err)
	}
	if got, _, _ := st.LoadResticPasswordCiphertext(ctx); got != ct+"-2" {
		t.Fatalf("overwrite result = %q", got)
	}
	// 空密文显式拒绝（防口径含糊的空条目）。
	if err := st.SaveResticPasswordCiphertext(ctx, ""); err == nil {
		t.Fatal("empty ciphertext must be rejected")
	}
}

// TestResticPasswordIsInternalKey 负面测试（§2.3 内部键）：口令键不被
// LoadS3Settings 的 typed 投影携带；SaveS3Settings 的 PUT 全量落库不触碰
// 口令条目（不残留、不覆写）。
func TestResticPasswordIsInternalKey(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	const ct = "age-ciphertext-marker-not-real"
	if err := st.SaveResticPasswordCiphertext(ctx, ct); err != nil {
		t.Fatalf("SaveResticPasswordCiphertext: %v", err)
	}
	// PUT 一轮合法 external 设置（含 secret）。
	in := S3Settings{
		Mode: S3ModeExternal, EndpointURL: "https://s3.example.com", Bucket: "fleetly",
		AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "enc-secret",
		PathStyle: true,
	}
	if err := st.SaveS3Settings(ctx, in, S3SaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("SaveS3Settings: %v", err)
	}
	got, err := st.LoadS3Settings(ctx)
	if err != nil {
		t.Fatalf("LoadS3Settings: %v", err)
	}
	// 八键投影与口令无关（typed 视图天然不含它——结构性保证由本断言钉住）。
	if got.EndpointURL != in.EndpointURL || got.SecretAccessKey != "enc-secret" {
		t.Fatalf("typed settings drifted: %+v", got)
	}
	pw, found, err := st.LoadResticPasswordCiphertext(ctx)
	if err != nil || !found || pw != ct {
		t.Fatalf("restic password after PUT save: found=%v value=%q err=%v, want untouched", found, pw, err)
	}
}

// TestBackupUploadColumns state_backups 上传三列（E3-3）：落账初始 none；
// ok/failed 结论落账（词表校验、failed 必带错误摘要、截断上界）；回读
// 投影（uploaded_at/upload_error）；GetStateBackup 单行读。
func TestBackupUploadColumns(t *testing.T) {
	st := newSettingsStore(t)
	ctx := context.Background()

	rec, err := st.RecordStateBackup(ctx, BackupWrite{
		Kind: BackupKindManual, Path: filepath.Join(t.TempDir(), "fleetly.db"),
		Verify: BackupVerifyVerified,
	})
	if err != nil {
		t.Fatalf("RecordStateBackup: %v", err)
	}
	if rec.UploadStatus != BackupUploadNone {
		t.Fatalf("initial upload_status = %s, want none", rec.UploadStatus)
	}

	// 词表收口：非法状态拒绝。
	if err := st.UpdateStateBackupUpload(ctx, rec.ID, "pending", ""); err == nil {
		t.Fatal("upload status must be ok|failed (pending rejected)")
	}
	// failed 无摘要拒绝。
	if err := st.UpdateStateBackupUpload(ctx, rec.ID, BackupUploadFailed, "  "); err == nil {
		t.Fatal("failed status requires error detail")
	}
	// 缺失行拒绝。
	if err := st.UpdateStateBackupUpload(ctx, "NOPE", BackupUploadOK, ""); err == nil {
		t.Fatal("missing row must be rejected")
	}

	// failed 落账 + 超长摘要截断。
	longErr := strings.Repeat("x", uploadErrorMaxBytes+100)
	if err := st.UpdateStateBackupUpload(ctx, rec.ID, BackupUploadFailed, longErr); err != nil {
		t.Fatalf("UpdateStateBackupUpload(failed): %v", err)
	}
	got, err := st.GetStateBackup(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetStateBackup: %v", err)
	}
	if got.UploadStatus != BackupUploadFailed || len(got.UploadError) != uploadErrorMaxBytes {
		t.Fatalf("failed row = %s/%d bytes, want failed/%d (truncated)",
			got.UploadStatus, len(got.UploadError), uploadErrorMaxBytes)
	}
	if got.UploadedAt.IsZero() {
		t.Error("uploaded_at should record the failed attempt time")
	}

	// ok 落账（摘要清空）。
	if err := st.UpdateStateBackupUpload(ctx, rec.ID, BackupUploadOK, ""); err != nil {
		t.Fatalf("UpdateStateBackupUpload(ok): %v", err)
	}
	got, err = st.GetStateBackup(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetStateBackup: %v", err)
	}
	if got.UploadStatus != BackupUploadOK || got.UploadError != "" || got.UploadedAt.IsZero() {
		t.Fatalf("ok row = %s/%q/%v, want ok/empty/set", got.UploadStatus, got.UploadError, got.UploadedAt)
	}

	// ListStateBackups 投影一致。
	rows, err := st.ListStateBackups(ctx, 0)
	if err != nil {
		t.Fatalf("ListStateBackups: %v", err)
	}
	if len(rows) != 1 || rows[0].UploadStatus != BackupUploadOK {
		t.Fatalf("list projection = %+v, want single ok row", rows)
	}

	// GetStateBackup 缺失 → 显式错误。
	if _, err := st.GetStateBackup(ctx, "NOPE"); err == nil {
		t.Fatal("GetStateBackup missing row must error")
	}
}
