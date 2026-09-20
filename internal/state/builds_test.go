package state

// builds 表读写通道测试（T2.8/T2.9）：状态机迁移谓词（终态不可逆、claim
// 竞争恰一胜出）、审计同事务（fail-closed——迁移失败审计不存在）、镜像
// 身份登记（digest→ref 反查，T2.9）。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

// TestResetInterruptedBuilds 启动复位（MG-A3 crashpoint：重启恢复）：
// building 行 → failed（错误码 + finished_at + 审计 reason）；queued 与终态
// 行不动；无 building 行时返回 0；FailStrandedBuild 对非 building 行按
// CAS 谓词拒绝。
func TestResetInterruptedBuilds(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-reset")
	interrupted := createTestBuild(t, st, app.ID, "web")
	queued := createTestBuild(t, st, app.ID, "worker")
	terminal := createTestBuild(t, st, app.ID, "api")
	if err := st.ClaimBuild(context.Background(), interrupted.ID); err != nil {
		t.Fatalf("claim interrupted: %v", err)
	}
	if err := st.ClaimBuild(context.Background(), terminal.ID); err != nil {
		t.Fatalf("claim terminal: %v", err)
	}
	if err := st.FinishBuildSucceeded(context.Background(), terminal.ID, "r", "sha256:t", "", ""); err != nil {
		t.Fatalf("finish terminal: %v", err)
	}

	const reason = "构建被中断（daemon 重启/关停）"
	n, err := st.ResetInterruptedBuilds(context.Background(), "E_BUILD_FAILED", reason)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n != 1 {
		t.Fatalf("reset count = %d, want 1（只复位 building 行）", n)
	}

	row, err := st.GetBuild(context.Background(), interrupted.ID)
	if err != nil {
		t.Fatalf("get interrupted: %v", err)
	}
	if row.Status != BuildFailed || row.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("interrupted row = %s/%s, want failed/E_BUILD_FAILED", row.Status, row.ErrorCode)
	}
	if row.FinishedAt.IsZero() {
		t.Fatal("reset row must stamp finished_at")
	}
	if qrow, err := st.GetBuild(context.Background(), queued.ID); err != nil || qrow.Status != BuildQueued {
		t.Fatalf("queued row = %s (%v), want queued untouched", qrow.Status, err)
	}
	if trow, err := st.GetBuild(context.Background(), terminal.ID); err != nil || trow.Status != BuildSucceeded {
		t.Fatalf("terminal row = %s (%v), want succeeded untouched（终态不可逆）", trow.Status, err)
	}

	// 复位归因进审计 diff（builds 表只存错误码，归因落点在审计）。
	audits, err := st.RecentAudits(context.Background(), 20)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Target == "build:"+interrupted.ID && a.Action == "build.finish" && a.Result == "error" {
			if !strings.Contains(a.DiffSummary, reason) {
				t.Fatalf("audit diff = %s, want contains %q", a.DiffSummary, reason)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("reset must audit build.finish(error) per row")
	}

	// 幂等：无 building 行时再复位返回 0。
	if n, err := st.ResetInterruptedBuilds(context.Background(), "E_BUILD_FAILED", reason); err != nil || n != 0 {
		t.Fatalf("second reset = %d, %v; want 0, nil", n, err)
	}

	// FailStrandedBuild 的 CAS 谓词：非 building 行拒绝（queued 行不得被
	// 兜底路径误伤）。
	if err := st.FailStrandedBuild(context.Background(), queued.ID, "E_BUILD_FAILED", "x"); !errors.Is(err, ErrBuildStateTransition) {
		t.Fatalf("fail stranded on queued = %v, want ErrBuildStateTransition", err)
	}
}

// TestSetBuildLogPath M2-5：building 行的 log_path 回填——产物目录就位即写
// （失败终态也携带日志路径），行级谓词限定 building（queued/终态行静默
// 跳过、不覆盖成功终态的权威值），失败终态保留回填值。
func TestSetBuildLogPath(t *testing.T) {
	st := newTestStore(t)
	app := createBuildTestApp(t, st, "build-logpath")
	queued := createTestBuild(t, st, app.ID, "web")
	claimed := createTestBuild(t, st, app.ID, "api")
	if err := st.ClaimBuild(context.Background(), claimed.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// queued 行：谓词未命中，静默跳过（best-effort 回填不报错）。
	if err := st.SetBuildLogPath(context.Background(), queued.ID, "/q.log"); err != nil {
		t.Fatalf("set on queued: %v", err)
	}
	if row, err := st.GetBuild(context.Background(), queued.ID); err != nil || row.LogPath != "" {
		t.Fatalf("queued row log_path = %q (%v), want empty", row.LogPath, err)
	}

	// building 行：回填生效。
	const want = "/artifacts/app/01BUILD/build.log"
	if err := st.SetBuildLogPath(context.Background(), claimed.ID, want); err != nil {
		t.Fatalf("set on building: %v", err)
	}
	if row, err := st.GetBuild(context.Background(), claimed.ID); err != nil || row.LogPath != want {
		t.Fatalf("building row log_path = %q (%v), want %q", row.LogPath, err, want)
	}

	// 失败终态保留回填值（M2-5 的失败取证通道）。
	if err := st.FinishBuildFailed(context.Background(), claimed.ID, "E_BUILD_FAILED"); err != nil {
		t.Fatalf("finish failed: %v", err)
	}
	if row, err := st.GetBuild(context.Background(), claimed.ID); err != nil || row.LogPath != want {
		t.Fatalf("failed row log_path = %q (%v), want %q（终态不得清除回填）", row.LogPath, err, want)
	}

	// 终态后回填：谓词未命中，不覆盖。
	if err := st.SetBuildLogPath(context.Background(), claimed.ID, "/other.log"); err != nil {
		t.Fatalf("set on terminal: %v", err)
	}
	if row, err := st.GetBuild(context.Background(), claimed.ID); err != nil || row.LogPath != want {
		t.Fatalf("terminal row log_path = %q (%v), want %q（终态行不得被覆盖）", row.LogPath, err, want)
	}
}

// TestCreateBuildAsAuditAttribution M4-8 回归：CreateBuildAs 的调用方归因
// 注入——audit 非 nil 时 build.create 审计行带调用方 token/actor（H14
// 敏感写面的行为人记录）；nil 时回落 system 归因（与旧 CreateBuild 行为
// 逐字一致）。
func TestCreateBuildAsAuditAttribution(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := createBuildTestApp(t, st, "build-attrib")

	// 调用方归因：actor=human + ActorTokenID=调用方 token（与 api 层
	// TriggerBuild 同形态：ID 在入队侧分配，request 与审计共用）。
	const attributedID = "01ATTRIBBUILD0000000000000"
	attributed, err := st.CreateBuildAs(ctx, BuildRecord{
		ID: attributedID, AppID: app.ID, Service: "web", Driver: DriverRailpack, Request: `{"build_id":"a"}`,
	}, &AuditEntry{
		Actor:        "human",
		ActorTokenID: "01CALLER000000000000000000",
		Action:       "build.create",
		Target:       "app:" + app.ID,
		Result:       "ok",
		DiffSummary:  DiffSummary("build", attributedID, "service", "web"),
	})
	if err != nil {
		t.Fatalf("CreateBuildAs: %v", err)
	}
	// 缺省归因：actor=system（内部路径）。
	plain, err := st.CreateBuildAs(ctx, BuildRecord{
		AppID: app.ID, Service: "api", Driver: DriverRailpack, Request: `{"build_id":"b"}`,
	}, nil)
	if err != nil {
		t.Fatalf("CreateBuildAs default: %v", err)
	}

	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	// actor_token_id 不在只读投影（AuditRecord）内——经行内查询取回
	//（归因断言的核心字段）。
	tokenIDOf := func(a AuditRecord) string {
		var actorTokenID sql.NullString
		if err := st.db.QueryRowContext(ctx,
			`SELECT actor_token_id FROM audit_log WHERE id = ?`, a.ID).Scan(&actorTokenID); err != nil {
			t.Fatalf("scan actor_token_id of audit %s: %v", a.ID, err)
		}
		return actorTokenID.String
	}
	var gotAttributed, gotPlain bool
	for _, a := range audits {
		if a.Action != "build.create" {
			continue
		}
		if strings.Contains(a.DiffSummary, attributed.ID) {
			gotAttributed = true
			if a.Actor != "human" || tokenIDOf(a) != "01CALLER000000000000000000" {
				t.Fatalf("attributed audit actor=%q actor_token_id=%q, want human/01CALLER…", a.Actor, tokenIDOf(a))
			}
		}
		if strings.Contains(a.DiffSummary, plain.ID) {
			gotPlain = true
			if a.Actor != "system" || tokenIDOf(a) != "" {
				t.Fatalf("default audit actor=%q actor_token_id=%q, want system/空", a.Actor, tokenIDOf(a))
			}
		}
	}
	if !gotAttributed || !gotPlain {
		t.Fatalf("build.create audits missing: attributed=%v plain=%v", gotAttributed, gotPlain)
	}
}
