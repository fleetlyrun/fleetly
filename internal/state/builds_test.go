package state

// builds 表读写通道测试（T2.8/T2.9）：状态机迁移谓词（终态不可逆、claim
// 竞争恰一胜出）、审计同事务（fail-closed——迁移失败审计不存在）、镜像
// 身份登记（digest→ref 反查，T2.9）。

import (
	"context"
	"errors"
	"testing"
)

func createBuildTestApp(t *testing.T, st *Store, name string) App {
	t.Helper()
	app, err := st.CreateApp(context.Background(), "", name)
	if err != nil {
		t.Fatalf("create app %s: %v", name, err)
	}
	return app
}

func createTestBuild(t *testing.T, st *Store, appID, service string) BuildRecord {
	t.Helper()
	rec, err := st.CreateBuild(context.Background(), BuildRecord{
		AppID:   appID,
		Service: service,
		Driver:  DriverRailpack,
		Request: `{"build_id":"x"}`,
	})
	if err != nil {
		t.Fatalf("create build: %v", err)
	}
	return rec
}

// TestBuildLifecycleQueuedToSucceeded 走通 queued→building→succeeded：
// claim 盖 started_at、成功终态落镜像身份（ref+digest+产物路径）。
func TestBuildLifecycleQueuedToSucceeded(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-lifecycle")
	rec := createTestBuild(t, st, app.ID, "web")

	got, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if got.Status != BuildQueued {
		t.Fatalf("initial status = %s, want queued", got.Status)
	}

	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get after claim: %v", err)
	}
	if got.Status != BuildBuilding || got.StartedAt.IsZero() {
		t.Fatalf("after claim status=%s started_at zero=%t, want building+stamped", got.Status, got.StartedAt.IsZero())
	}

	const (
		ref    = "fleetly-local/build-lifecycle:build-lifecycle-01"
		digest = "sha256:aa11"
	)
	if err := st.FinishBuildSucceeded(context.Background(), rec.ID, ref, digest, "plan.json", "build.log"); err != nil {
		t.Fatalf("finish succeeded: %v", err)
	}
	got, err = st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get after finish: %v", err)
	}
	if got.Status != BuildSucceeded || got.ImageRef != ref || got.ImageDigest != digest {
		t.Fatalf("succeeded row wrong: %+v", got)
	}
	if got.PlanPath != "plan.json" || got.LogPath != "build.log" || got.FinishedAt.IsZero() {
		t.Fatalf("succeeded artifacts wrong: %+v", got)
	}

	// 终态不可逆：claim/finish 均拒绝。
	if err := st.ClaimBuild(context.Background(), rec.ID); !errors.Is(err, ErrBuildStateTransition) {
		t.Fatalf("claim on terminal = %v, want ErrBuildStateTransition", err)
	}
	if err := st.FinishBuildFailed(context.Background(), rec.ID, "E_BUILD_FAILED"); !errors.Is(err, ErrBuildStateTransition) {
		t.Fatalf("finish on terminal = %v, want ErrBuildStateTransition", err)
	}
}

// TestBuildClaimRaceSingleWinner 多 worker 认领竞争：同一 queued 行重复
// claim 恰有一个胜出（行级谓词）。
func TestBuildClaimRaceSingleWinner(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-race")
	rec := createTestBuild(t, st, app.ID, "web")

	err1 := st.ClaimBuild(context.Background(), rec.ID)
	err2 := st.ClaimBuild(context.Background(), rec.ID)
	if err1 != nil {
		t.Fatalf("first claim must win: %v", err1)
	}
	if !errors.Is(err2, ErrBuildStateTransition) {
		t.Fatalf("second claim = %v, want ErrBuildStateTransition", err2)
	}
}

// TestBuildFailedRecordsErrorCode 失败终态落注册表错误码。
func TestBuildFailedRecordsErrorCode(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-failed")
	rec := createTestBuild(t, st, app.ID, "web")

	if err := st.ClaimBuild(context.Background(), rec.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.FinishBuildFailed(context.Background(), rec.ID, "E_BUILD_FAILED"); err != nil {
		t.Fatalf("finish failed: %v", err)
	}
	got, err := st.GetBuild(context.Background(), rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != BuildFailed || got.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("failed row wrong: %+v", got)
	}
	if got.ImageDigest != "" {
		t.Fatalf("failed build must not carry digest: %+v", got)
	}
}

// TestBuildAuditTrailFollowsTransitions 审计与状态迁移同事务：create/start/
// finish 各产生一条审计；终态拒绝路径不产生额外审计（事务回滚）。
func TestBuildAuditTrailFollowsTransitions(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-audit")
	rec := createTestBuild(t, st, app.ID, "web")

	_ = st.ClaimBuild(context.Background(), rec.ID)
	_ = st.FinishBuildSucceeded(context.Background(), rec.ID, "r", "sha256:d", "", "l")
	_ = st.ClaimBuild(context.Background(), rec.ID) // 拒绝（终态）——审计不得存在第四条

	audits, err := st.RecentAudits(context.Background(), 10)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	var actions []string
	for _, a := range audits {
		if a.Target == "build:"+rec.ID || a.Target == "app:"+app.ID {
			actions = append(actions, a.Action)
		}
	}
	want := []string{"build.finish", "build.start", "build.create"}
	if len(actions) != len(want) {
		t.Fatalf("audit actions = %v, want %v (fail-closed: rejected transition must not audit)", actions, want)
	}
	for i, w := range want {
		if actions[i] != w {
			t.Fatalf("audit action[%d] = %s, want %s（RecentAudits 倒序）", i, actions[i], w)
		}
	}
}

// TestBuildDigestLookup 按不可变 digest 反查登记（T2.9 digest→ref 映射）。
func TestBuildDigestLookup(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-digest")
	rec := createTestBuild(t, st, app.ID, "web")

	_ = st.ClaimBuild(context.Background(), rec.ID)
	if err := st.FinishBuildSucceeded(context.Background(), rec.ID,
		"fleetly-local/build-digest:b1", "sha256:dd11", "", ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	rows, err := st.FindBuildsByDigest(context.Background(), "sha256:dd11")
	if err != nil {
		t.Fatalf("find by digest: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != rec.ID || rows[0].ImageRef != "fleetly-local/build-digest:b1" {
		t.Fatalf("digest lookup wrong: %+v", rows)
	}
	if missing, err := st.FindBuildsByDigest(context.Background(), "sha256:gone"); err != nil || len(missing) != 0 {
		t.Fatalf("missing digest lookup = %v, %v; want empty, nil", missing, err)
	}
}

// TestNextQueuedBuildsFIFO 排队可见性：按入队序返回 queued 行。
func TestNextQueuedBuildsFIFO(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-fifo")
	first := createTestBuild(t, st, app.ID, "web")
	second := createTestBuild(t, st, app.ID, "worker")

	// first 已在执行 → 只剩 second 可被认领。
	if err := st.ClaimBuild(context.Background(), first.ID); err != nil {
		t.Fatalf("claim first: %v", err)
	}
	queued, err := st.NextQueuedBuilds(context.Background(), 10)
	if err != nil {
		t.Fatalf("next queued: %v", err)
	}
	if len(queued) != 1 || queued[0].ID != second.ID {
		t.Fatalf("queued = %+v, want only %s", queued, second.ID)
	}
}

// TestCreateBuildValidation 入队校验：非法 driver 拒绝。
func TestCreateBuildValidation(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-validate")

	if _, err := st.CreateBuild(context.Background(), BuildRecord{AppID: app.ID, Service: "web", Driver: "magical"}); err == nil {
		t.Fatal("invalid driver accepted")
	}
}
