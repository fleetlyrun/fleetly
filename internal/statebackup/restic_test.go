package statebackup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 上传轨测试（E3-3 六类路径，fake 执行器断言命令构造与结论落账）：
// 成功路径 / 失败路径 / 回读缺失 / unset 停摆 / 口令幂等 / 恢复绿，
// 外加 secret 不落台账/事件（state-model §2.9 负面测试）。

// fakeRestic 是 restic 执行器假件：记录每次调用规格（镜像/参数/env/挂载），
// 按子命令回放注入的输出与错误。
type fakeRestic struct {
	calls []ResticSpec

	backupOutput    string
	backupErr       error
	snapshotsOutput string
	snapshotsErr    error
	forgetErr       error
}

func (f *fakeRestic) RunRestic(_ context.Context, spec ResticSpec) (string, error) {
	f.calls = append(f.calls, spec)
	switch resticCommand(spec.Args) {
	case "backup":
		return f.backupOutput, f.backupErr
	case "snapshots":
		return f.snapshotsOutput, f.snapshotsErr
	case "forget":
		return "", f.forgetErr
	default:
		return "", fmt.Errorf("fakeRestic: unexpected command in args %v", spec.Args)
	}
}

// resticCommand 取 args 中的子命令（跳过 `-o`/`--flag` 全局选项；与
// substrate 侧 firstToken 同口径的测试内实现——不 import 适配器包）。
func resticCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			if args[i] == "-o" || args[i] == "--option" {
				i++
			}
			continue
		}
		return args[i]
	}
	return ""
}

// newUploadTestManager 构造带上传轨的 Manager（真实 Store + 真实密钥 +
// fake 执行器）。
func newUploadTestManager(t *testing.T, fr *fakeRestic) (*Manager, *state.Store) {
	t.Helper()
	mgr, st, _ := newTestManager(t)
	box, _, err := secrets.EnsureKey(mgr.keyPath)
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	mgr.WithUpload(fr, box)
	return mgr, st
}

// saveExternalSettings 落一份合法 external 设置（secret 密文入参——与
// 生产一致：state 层存密文不解释）。
func saveExternalSettings(t *testing.T, st *state.Store, box *secrets.Box, secretPlain string) {
	t.Helper()
	ct, err := box.Encrypt([]byte(secretPlain))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	in := state.S3Settings{
		Mode:            state.S3ModeExternal,
		EndpointURL:     "https://s3.example.com",
		Region:          "us-east-1",
		Bucket:          "fleetly-backup",
		AccessKeyID:     "AKIDTEST123",
		SecretAccessKey: string(ct),
		PathStyle:       true,
	}
	if err := st.SaveS3Settings(context.Background(), in, state.S3SaveOptions{Actor: "system"}); err != nil {
		t.Fatalf("SaveS3Settings: %v", err)
	}
}

// managerBox 取回 Manager 持有的 envelope（测试装配注入的同一把）。
func managerBox(mgr *Manager) *secrets.Box { return mgr.box }

const (
	fakeAKID  = "AKIDTEST123"
	fakeAKSK  = "SECRET-PLAIN-4f3e2d1c"
	fakeSnap  = "5a7c3e1d9b2f0001"
	snapLine  = `{"message_type":"summary","total_bytes_processed":1024,"snapshot_id":"` + fakeSnap + `"}`
	snapsJSON = `[{"id":"` + fakeSnap + `","hostname":"restic-test"}]`
)

// TestUploadHappyPath 成功路径：backup → snapshots 回读含该 id → 台账 ok
// → forget 被调（keep 对齐）；命令构造逐项断言（镜像钉版/repo URL/env
// 组装/只读挂载/keep 传递）；Trigger 同步响应携带上传结论。
func TestUploadHappyPath(t *testing.T) {
	fr := &fakeRestic{
		backupOutput:    "other line\n" + snapLine + "\n",
		snapshotsOutput: snapsJSON,
	}
	mgr, st := newUploadTestManager(t, fr)
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindManual)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.UploadStatus != state.BackupUploadOK {
		t.Fatalf("upload_status = %s, want ok (sync response must carry upload verdict)", rec.UploadStatus)
	}
	if rec.UploadedAt.IsZero() {
		t.Fatal("uploaded_at should be set on ok row")
	}
	if rec.VerifyStatus != state.BackupVerifyVerified {
		t.Fatalf("verify_status = %s, want verified (upload must not rewrite local verdict)", rec.VerifyStatus)
	}
	if err := mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth after ok upload: %v", err)
	}

	// 三段调用：backup → snapshots → forget。
	if len(fr.calls) != 3 {
		t.Fatalf("runner calls = %d, want 3 (backup, snapshots, forget)", len(fr.calls))
	}
	backup, snapshots, forget := fr.calls[0], fr.calls[1], fr.calls[2]

	// 镜像钉版（D-S3-3 双锚的常量锚）。
	for i, spec := range fr.calls {
		if spec.Image != DefaultResticImage {
			t.Errorf("call %d image = %s, want pinned DefaultResticImage", i, spec.Image)
		}
	}
	// 只读挂载指向本份备份目录（D-S3-3：备份核一字不动——挂的是这一份的
	// 目录，不是备份根）。
	if backup.HostMountDir == "" || backup.HostMountDir == mgr.Dir() {
		t.Fatalf("backup mount source = %q, want this backup's directory (not the backup root)", backup.HostMountDir)
	}
	if backup.ContainerMountDir != containerMountDir {
		t.Fatalf("backup mount target = %q, want %s", backup.ContainerMountDir, containerMountDir)
	}
	if !contains(backup.Args, containerMountDir) {
		t.Fatalf("backup args %v should reference the container mount", backup.Args)
	}
	// path-style（设置 true）→ 扩展选项在列。
	if resticCommand(backup.Args) != "backup" {
		t.Fatalf("backup command = %s", resticCommand(backup.Args))
	}
	foundLookup := false
	for i, a := range backup.Args {
		if a == "-o" && i+1 < len(backup.Args) && backup.Args[i+1] == "s3.bucket-lookup=path" {
			foundLookup = true
		}
	}
	if !foundLookup {
		t.Fatalf("backup args %v missing -o s3.bucket-lookup=path (path_style=true setting)", backup.Args)
	}
	// env 组装：repo 形态 + 解密后凭证 + 口令。
	wantRepo := "s3:https://s3.example.com/fleetly-backup/statebackups"
	if backup.Env["RESTIC_REPOSITORY"] != wantRepo {
		t.Errorf("RESTIC_REPOSITORY = %q, want %q", backup.Env["RESTIC_REPOSITORY"], wantRepo)
	}
	if got := backup.Env["AWS_ACCESS_KEY_ID"]; got != fakeAKID {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want decrypted-access-key", got)
	}
	if got := backup.Env["AWS_SECRET_ACCESS_KEY"]; got != fakeAKSK {
		t.Errorf("AWS_SECRET_ACCESS_KEY mismatch (want decrypted plaintext)")
	}
	if got := backup.Env["AWS_DEFAULT_REGION"]; got != "us-east-1" {
		t.Errorf("AWS_DEFAULT_REGION = %q, want us-east-1", got)
	}
	password := backup.Env["RESTIC_PASSWORD"]
	if len(password) != 2*resticPasswordBytes {
		t.Fatalf("RESTIC_PASSWORD length = %d, want %d hex chars (32B random)", len(password), 2*resticPasswordBytes)
	}

	// snapshots 回读与 forget 保留对齐。
	if resticCommand(snapshots.Args) != "snapshots" || !contains(snapshots.Args, fakeSnap) {
		t.Fatalf("snapshots call = %v, want readback of %s", snapshots.Args, fakeSnap)
	}
	if resticCommand(forget.Args) != "forget" ||
		!contains(forget.Args, "--keep-last") || !contains(forget.Args, "2") {
		t.Fatalf("forget call = %v, want --keep-last 2 (keep)", forget.Args)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestUploadFailurePath 失败路径：backup 执行失败 → 台账 failed + 事件
// backup.upload_failed + 组件红（degraded 口径）——本地 verified 结论
// 不回写、Trigger 不报错（上传失败不改写备份成功语义）。
func TestUploadFailurePath(t *testing.T) {
	fr := &fakeRestic{backupErr: errors.New("injected restic failure: connection refused")}
	mgr, st := newUploadTestManager(t, fr)
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger must not fail on upload failure (local snapshot unaffected): %v", err)
	}
	if rec.VerifyStatus != state.BackupVerifyVerified {
		t.Fatalf("verify_status = %s, want verified", rec.VerifyStatus)
	}
	if rec.UploadStatus != state.BackupUploadFailed {
		t.Fatalf("upload_status = %s, want failed", rec.UploadStatus)
	}
	if !strings.Contains(rec.UploadError, "injected restic failure") {
		t.Fatalf("upload_error = %q, want failure reason", rec.UploadError)
	}

	// 事件 backup.upload_failed（payload 带 id 与摘要）。
	events, err := st.EventsSince(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Name == "backup.upload_failed" {
			found = true
			if !strings.Contains(ev.Payload, rec.ID) {
				t.Errorf("event payload %q missing backup id", ev.Payload)
			}
			if !strings.Contains(ev.Payload, "injected restic failure") {
				t.Errorf("event payload %q missing error summary", ev.Payload)
			}
		}
	}
	if !found {
		t.Fatal("backup.upload_failed event missing")
	}

	// 组件红（degraded 口径，英文消息）。
	herr := mgr.CheckHealth()
	if herr == nil {
		t.Fatal("CheckHealth must be unhealthy when latest upload failed")
	}
	if !strings.Contains(herr.Error(), "local snapshot ok, remote upload failed") {
		t.Fatalf("health error %q missing degraded wording", herr.Error())
	}
}

// TestUploadReadbackMissing 回读缺失：backup 报告成功（有 snapshot id）但
// snapshots 列表不含该 id → failed——「绿色成功但实际没上传」结构性不存在。
func TestUploadReadbackMissing(t *testing.T) {
	fr := &fakeRestic{
		backupOutput:    snapLine,
		snapshotsOutput: `[{"id":"other000000000000"}]`,
	}
	mgr, st := newUploadTestManager(t, fr)
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindManual)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.UploadStatus != state.BackupUploadFailed {
		t.Fatalf("upload_status = %s, want failed (snapshot id absent from readback)", rec.UploadStatus)
	}
	if !strings.Contains(rec.UploadError, fakeSnap) || !strings.Contains(rec.UploadError, "not listed") {
		t.Fatalf("upload_error = %q, want missing-snapshot attribution", rec.UploadError)
	}
	events, err := st.EventsSince(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, ev := range events {
		if ev.Name == "backup.upload_recovered" {
			t.Fatal("recovered event must not fire on failed upload")
		}
	}
}

// TestUploadSkippedWhenUnset unset 停摆：s3.mode=unset → 执行器零调用、
// upload_status 保持 none、组件健康——外部端点未配置是合法态（负面测试
// 钉住「unset 不算失败不红」）。
func TestUploadSkippedWhenUnset(t *testing.T) {
	fr := &fakeRestic{}
	mgr, st := newUploadTestManager(t, fr)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0 (upload track off in unset mode)", len(fr.calls))
	}
	if rec.UploadStatus != state.BackupUploadNone {
		t.Fatalf("upload_status = %s, want none", rec.UploadStatus)
	}
	if err := mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth must stay healthy in unset mode: %v", err)
	}
	// unset 下不产生上传轨事件（backup.upload_failed/recovered 零出现）。
	events, err := st.EventsSince(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, ev := range events {
		if ev.Name == "backup.upload_failed" || ev.Name == "backup.upload_recovered" {
			t.Errorf("unset mode must not emit upload events, got %s", ev.Name)
		}
	}
}

// TestResticPasswordIdempotent 口令生成幂等（D-S3-5）：首次惰性生成落库，
// 第二次复用同一口令（两次 RESTIC_PASSWORD 一致；库中密文可解回同值）。
func TestResticPasswordIdempotent(t *testing.T) {
	fr := &fakeRestic{
		backupOutput:    snapLine,
		snapshotsOutput: snapsJSON,
	}
	mgr, st := newUploadTestManager(t, fr)
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)

	first, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger 1: %v", err)
	}
	second, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger 2: %v", err)
	}
	if first.UploadStatus != state.BackupUploadOK || second.UploadStatus != state.BackupUploadOK {
		t.Fatalf("upload status = %s/%s, want ok/ok", first.UploadStatus, second.UploadStatus)
	}
	// 备份目录互不相同：两次 RESTIC_PASSWORD 必须一致（生成一次，复用）。
	pw1 := fr.calls[0].Env["RESTIC_PASSWORD"]
	pw2 := fr.calls[3].Env["RESTIC_PASSWORD"]
	if pw1 == "" || pw1 != pw2 {
		t.Fatalf("RESTIC_PASSWORD drifted between runs (%d vs %d chars) — generation must be idempotent", len(pw1), len(pw2))
	}
	ct, found, err := st.LoadResticPasswordCiphertext(context.Background())
	if err != nil || !found {
		t.Fatalf("stored password found=%v err=%v, want stored once", found, err)
	}
	plain, err := managerBox(mgr).Decrypt([]byte(ct))
	if err != nil || string(plain) != pw1 {
		t.Fatalf("stored ciphertext roundtrip = %v (err %v), want env password", plain, err)
	}
}

// TestUploadRecoveredEvent 恢复绿：上一份 failed、本份 ok →
// backup.upload_recovered（配对 failed 形成红→绿闭环）。
func TestUploadRecoveredEvent(t *testing.T) {
	fr := &fakeRestic{backupErr: errors.New("first-run outage")}
	mgr, st := newUploadTestManager(t, fr)
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)

	if _, err := mgr.Trigger(context.Background(), state.BackupKindDaily); err != nil {
		t.Fatalf("Trigger 1 (upload fails): %v", err)
	}
	// 底座恢复：backup 成功 + 回读在列。
	fr.backupErr = nil
	fr.backupOutput = snapLine
	fr.snapshotsOutput = snapsJSON

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger 2: %v", err)
	}
	if rec.UploadStatus != state.BackupUploadOK {
		t.Fatalf("upload_status = %s, want ok", rec.UploadStatus)
	}
	if err := mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth must be green after recovered upload: %v", err)
	}
	events, err := st.EventsSince(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	failedCount, recovered := 0, false
	for _, ev := range events {
		switch ev.Name {
		case "backup.upload_failed":
			failedCount++
		case "backup.upload_recovered":
			recovered = true
			if !strings.Contains(ev.Payload, rec.ID) {
				t.Errorf("recovered payload %q missing current backup id", ev.Payload)
			}
		}
	}
	if failedCount != 1 {
		t.Errorf("backup.upload_failed count = %d, want 1", failedCount)
	}
	if !recovered {
		t.Error("backup.upload_recovered event missing (red→green closure)")
	}
}

// TestUploadSecretsNeverInLedgerOrEvents 负面测试（state-model §2.9）：
// 注入含全部 secret 材料的错误文本——台账 upload_error 与事件 payload
// 里的明文必须被擦除（scrubText 兜底），口令/AK/SK 零出现。
func TestUploadSecretsNeverInLedgerOrEvents(t *testing.T) {
	fr := &fakeRestic{}
	mgr, st := newUploadTestManager(t, fr)
	saveExternalSettings(t, st, managerBox(mgr), fakeAKSK)

	// 预生成口令（拿到明文注入错误文本——模拟「底座错误意外回显材料」）。
	password, err := mgr.resticPassword(context.Background())
	if err != nil {
		t.Fatalf("resticPassword: %v", err)
	}
	fr.backupErr = fmt.Errorf("boom: repo=%s pw=%s ak=%s sk=%s",
		"s3:https://s3.example.com/fleetly-backup/statebackups", password, fakeAKID, fakeAKSK)

	rec, err := mgr.Trigger(context.Background(), state.BackupKindDaily)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if rec.UploadStatus != state.BackupUploadFailed {
		t.Fatalf("upload_status = %s, want failed", rec.UploadStatus)
	}
	for _, secret := range []string{password, fakeAKID, fakeAKSK} {
		if strings.Contains(rec.UploadError, secret) {
			t.Fatalf("upload_error leaks secret material: %q", rec.UploadError)
		}
	}
	events, err := st.EventsSince(context.Background(), 0, 50)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, ev := range events {
		if ev.Name != "backup.upload_failed" {
			continue
		}
		for _, secret := range []string{password, fakeAKID, fakeAKSK} {
			if strings.Contains(ev.Payload, secret) {
				t.Fatalf("event payload leaks secret material: %q", ev.Payload)
			}
		}
	}
}

// TestResticTargetUnsetNotFailure resticTarget 的词表分派：unset →
// found=false（合法停摆，非错误）；external 缺端点 → found=true 且报错
//（已配置但残缺 = 诚实失败，不是停摆）。
func TestResticTargetUnsetNotFailure(t *testing.T) {
	_, _, _, found, err := resticTarget(state.S3Settings{Mode: state.S3ModeUnset})
	if err != nil || found {
		t.Fatalf("unset: found=%v err=%v, want false/nil (legal off state)", found, err)
	}
	_, _, _, found, err = resticTarget(state.S3Settings{Mode: state.S3ModeExternal, Bucket: "b"})
	if err == nil || !found {
		t.Fatalf("incomplete external: found=%v err=%v, want true/explicit error", found, err)
	}
	// rustfs：派生端点 + path-style（E3-5 前凭证缺位由 resticCredentials
	// 显式失败，target 本身成立）。
	repo, pathStyle, _, found, err := resticTarget(state.S3Settings{Mode: state.S3ModeRustfs})
	if err != nil || !found {
		t.Fatalf("rustfs: found=%v err=%v, want true/nil", found, err)
	}
	if repo != "s3:"+state.RustfsEndpointURL+"/"+state.RustfsBucketName+"/statebackups" || !pathStyle {
		t.Fatalf("rustfs repo = %q pathStyle=%v, want derived endpoint + path-style", repo, pathStyle)
	}
}

// TestParseResticOutput 解析器单元：summary 提取（含跳过创建时省略
// snapshot_id 的形态）与 snapshots 前缀匹配（short-id/long-id 形差）。
func TestParseResticOutput(t *testing.T) {
	if got := parseBackupSnapshotID("noise\n" + snapLine + "\n"); got != fakeSnap {
		t.Fatalf("parseBackupSnapshotID = %q, want %s", got, fakeSnap)
	}
	if got := parseBackupSnapshotID(`{"message_type":"summary"}`); got != "" {
		t.Fatalf("summary without snapshot_id = %q, want empty", got)
	}
	if got := parseBackupSnapshotID(""); got != "" {
		t.Fatalf("empty output = %q, want empty", got)
	}
	if !snapshotsContain(snapsJSON, fakeSnap) {
		t.Fatal("snapshotsContain should match exact id")
	}
	if !snapshotsContain(`[{"id":"5a7c3e1d"}]`, fakeSnap) {
		t.Fatal("snapshotsContain should match short-id prefix")
	}
	if snapshotsContain(`[{"id":"other000000000000"}]`, fakeSnap) {
		t.Fatal("snapshotsContain must not match unrelated id")
	}
	if snapshotsContain("not json", fakeSnap) {
		t.Fatal("unparseable readback must not count as contained (honest guard)")
	}
}
