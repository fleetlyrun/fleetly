package gitserver

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DeployFromCommit 测试（幂等口径绑定断言面）：每次 git push 都建部署记录
// （不去重）；来源字段落库；compose 名与仓库名不一致拒绝。

func TestDeployFromGitPush(t *testing.T) {
	requireGit(t)
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	sourceDir, sha := newSourceRepo(t, composeFixture)
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sourceDir, "push", "--quiet", src.repoPath("my-api"), "main")

	// MG-6 回归前置采样：解析中转临时目录（os.MkdirTemp("fleetly-compose-")）
	// 必须随请求回收——结束时对照，不留孤儿 tmp。
	tmpBefore := countComposeTempDirs(t)

	// 首次 push → queued 部署 + 来源字段。
	rec, warnings, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/main", "")
	if err != nil {
		t.Fatalf("DeployFromGitPush: %v", err)
	}
	if rec.Status != state.DeployQueued || rec.SourceGitSHA != sha || rec.SourceGitRef != "refs/heads/main" {
		t.Fatalf("record = %+v (warnings=%v)", rec, warnings)
	}

	// 审计与事件同事务落库（git.push_deploy；事件复用 deployment.queued）。
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil || len(audits) == 0 {
		t.Fatalf("audits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "git.push_deploy" && a.Result == "ok" && strings.Contains(a.DiffSummary, sha) {
			found = true
		}
	}
	if !found {
		t.Fatal("git.push_deploy audit missing")
	}

	// 同内容二次 push → 部署记录新建（显式用户动作不去重）。
	rec2, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/main", "")
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if rec2.ID == rec.ID {
		t.Fatal("second push reused deployment id (dedupe semantics leaked into the push path)")
	}

	// MG-6 回归：两次 push 的解析中转目录均已回收（defer os.RemoveAll——
	// 持久化副本在 <数据根>/deployments/<id>/compose.yaml，tmp 不是契约面）。
	if after := countComposeTempDirs(t); after != tmpBefore {
		t.Fatalf("parse scratch temp dirs not reclaimed: fleetly-compose-* dir count %d → %d", tmpBefore, after)
	}
}

// countComposeTempDirs 数系统 temp 里 fleetly-compose- 前缀目录数（MG-6
// 临时目录回收断言的采样点；只数前缀，不触碰其他测试的临时物）。
func countComposeTempDirs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "fleetly-compose-") {
			n++
		}
	}
	return n
}

func TestDeployFromCommitRejections(t *testing.T) {
	requireGit(t)
	src, _, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	// compose name ≠ 仓库名 → 拒绝（E_COMPOSE_UNSUPPORTED——errcode 零新增）。
	mismatch := strings.Replace(composeFixture, "my-api", "other-name", 1)
	sourceDir, sha := newSourceRepo(t, mismatch)
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sourceDir, "push", "--quiet", src.repoPath("my-api"), "main")
	_, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/main", "")
	var appErr *apperr.Error
	if err == nil || !errors.As(err, &appErr) || appErr.Code() != "E_COMPOSE_UNSUPPORTED" {
		t.Fatalf("name mismatch err = %v, want E_COMPOSE_UNSUPPORTED", err)
	}

	// 非法 sha / 非法 app 名 → 输入防御拒绝。
	if _, _, err := src.DeployFromGitPush(ctx, "my-api", "zz", "refs/heads/main", ""); err == nil {
		t.Fatal("invalid sha accepted")
	}
	if _, _, err := src.DeployFromGitPush(ctx, "../evil", sha, "refs/heads/main", ""); err == nil {
		t.Fatal("invalid app name accepted")
	}
}

// 分支过滤契约（H2 修复 / MG-C1）：push 非配置分支不建部署（哨兵
// ErrBranchNotTracked），配置分支（缺省回落 main + 显式配置分支）正常
// 入队——daemon 侧权威过滤，SSH push 路径由此与 webhook 语义对齐。
func TestDeployFromCommitBranchFilter(t *testing.T) {
	requireGit(t)
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	sourceDir, sha := newSourceRepo(t, composeFixture)
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sourceDir, "push", "--quiet", src.repoPath("my-api"), "main")

	// 首次形态（app 行不存在）：按缺省分支 main 比对——dev 哨兵拒止且
	// 零副作用（不建 app 行、不建部署行）。
	if _, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/dev", ""); !errors.Is(err, ErrBranchNotTracked) {
		t.Fatalf("untracked branch (first push) err = %v, want ErrBranchNotTracked", err)
	}
	if _, err := st.GetAppByName(ctx, "my-api"); !errors.Is(err, state.ErrAppNotFound) {
		t.Fatalf("filtered push created app row (err = %v)", err)
	}

	// 配置分支（缺省 main）正常入队；app 行随首次部署创建。
	rec, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/main", "")
	if err != nil {
		t.Fatalf("tracked branch push: %v", err)
	}
	appRow, err := st.GetAppByName(ctx, "my-api")
	if err != nil {
		t.Fatal(err)
	}

	// app 行在、分支缺省 main：dev 仍拒止，(app, sha) 部署计数不变。
	if _, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/dev", ""); !errors.Is(err, ErrBranchNotTracked) {
		t.Fatalf("untracked branch (app exists) err = %v, want ErrBranchNotTracked", err)
	}
	n, err := st.CountGitDeploymentsForSHA(ctx, appRow.ID, sha)
	if err != nil || n != 1 {
		t.Fatalf("deployments after filtered push = %d (err = %v), want 1", n, err)
	}

	// 显式配置分支 release：main 反被拒止；release 放行（新部署行）。
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "release")
	if _, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/main", ""); !errors.Is(err, ErrBranchNotTracked) {
		t.Fatalf("main after reconfig err = %v, want ErrBranchNotTracked", err)
	}
	rec2, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "refs/heads/release", "")
	if err != nil {
		t.Fatalf("reconfigured branch push: %v", err)
	}
	if rec2.ID == rec.ID {
		t.Fatal("release push reused deployment id")
	}

	// ref 为空（端口调用方未携带引用形态）不做比对——进程内直连夹具兼容面。
	if _, _, err := src.DeployFromGitPush(ctx, "my-api", sha, "", ""); err != nil {
		t.Fatalf("empty ref push: %v", err)
	}
}

// TestDeployFromCommitFailClosedAtomic（H13 修复，state-model §2.9）：
// 入队单事务机制测试。故障注入沿用 state 测试基建同款（数据库级约束
// 违例，audit_test.go 口径）：DeployInput.AuditAction 传空串 → audit_log
// CHECK length(action) > 0 违例，事务后半（审计写，位于部署行 INSERT 与
// deployment.queued 事件之后）失败。断言：部署行不存在（已插的行随事务
// 回滚）、事件不留痕——两事务时代的「部署行已存在、引擎照常执行但事件
// 与审计缺失」审计黑洞在结构上不可达；对照组合（合法 action）行 + 事件
// 齐全。
func TestDeployFromCommitFailClosedAtomic(t *testing.T) {
	requireGit(t)
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	sourceDir, sha := newSourceRepo(t, composeFixture)
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sourceDir, "push", "--quiet", src.repoPath("my-api"), "main")

	_, _, err := src.DeployFromCommit(ctx, DeployInput{
		App:         "my-api",
		SHA:         sha,
		Ref:         "refs/heads/main",
		AuditAction: "", // 故障注入：空 action → audit_log CHECK 违例
	})
	if err == nil {
		t.Fatal("enqueue with failing audit write must fail (fail-closed)")
	}

	// 部署行随事务回滚：app 行（事务外前置写）保留，但无在途部署、
	// (app, sha) 部署计数为 0。
	app, aerr := st.GetAppByName(ctx, "my-api")
	if aerr != nil {
		t.Fatalf("app row (pre-tx write) should survive: %v", aerr)
	}
	if has, herr := st.AppHasNonTerminalDeployment(ctx, app.ID); herr != nil || has {
		t.Fatalf("AppHasNonTerminalDeployment = %v (%v), want false", has, herr)
	}
	if n, qerr := st.CountGitDeploymentsForSHA(ctx, app.ID, sha); qerr != nil || n != 0 {
		t.Fatalf("git deployments for sha = %d (%v), want 0", n, qerr)
	}
	// deployment.queued 事件先于审计写、一并撤销——事件表干净。
	events, eerr := st.EventsSince(ctx, 0, 10)
	if eerr != nil || len(events) != 0 {
		t.Fatalf("events after failed enqueue = %+v (%v), want none", events, eerr)
	}

	// 对照：合法 action 的正常入队后，部署行与事件齐全。
	if _, _, err := src.DeployFromCommit(ctx, DeployInput{
		App: "my-api", SHA: sha, Ref: "refs/heads/main", AuditAction: "git.push_deploy",
	}); err != nil {
		t.Fatalf("healthy enqueue: %v", err)
	}
	if n, qerr := st.CountGitDeploymentsForSHA(ctx, app.ID, sha); qerr != nil || n != 1 {
		t.Fatalf("git deployments after healthy enqueue = %d (%v), want 1", n, qerr)
	}
	if events, eerr := st.EventsSince(ctx, 0, 10); eerr != nil || len(events) != 1 {
		t.Fatalf("events after healthy enqueue = %d (%v), want 1", len(events), eerr)
	}
}

// TestDeployFromCommitDedupeSHA M3-4 回归：DedupeSHA 置位（webhook 入口）
// 时入队事务内做 (app, sha) 幂等复查——首创建行、重投返回
// ErrDuplicateGitDeployment 且不建新行；不置位（SSH push 路径）恒建新行
// （幂等口径绑定，行为不变）。并发双投的竞态闭合在 state 层测试
// （TestGitSHADedupConcurrentSingleRow）钉死。
func TestDeployFromCommitDedupeSHA(t *testing.T) {
	requireGit(t)
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	sourceDir, sha := newSourceRepo(t, composeFixture)
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sourceDir, "push", "--quiet", src.repoPath("my-api"), "main")
	dedupeInput := DeployInput{
		App: "my-api", SHA: sha, Ref: "refs/heads/main",
		AuditAction: "git.webhook_deploy", DedupeSHA: true,
	}
	// 首发：建行（app 行随之自动创建）。
	if _, _, err := src.DeployFromCommit(ctx, dedupeInput); err != nil {
		t.Fatalf("first dedupe enqueue: %v", err)
	}
	app, err := st.GetAppByName(ctx, "my-api")
	if err != nil {
		t.Fatalf("GetAppByName: %v", err)
	}
	// 重投（判重命中）：ErrDuplicateGitDeployment、不建新行。
	if _, _, err := src.DeployFromCommit(ctx, dedupeInput); !errors.Is(err, state.ErrDuplicateGitDeployment) {
		t.Fatalf("second dedupe enqueue err = %v, want ErrDuplicateGitDeployment", err)
	}
	if n, qerr := st.CountGitDeploymentsForSHA(ctx, app.ID, sha); qerr != nil || n != 1 {
		t.Fatalf("git deployments for sha = %d (%v), want 1", n, qerr)
	}
	// 不置位（SSH push 语义）：显式用户动作恒建新行。
	pushInput := DeployInput{
		App: "my-api", SHA: sha, Ref: "refs/heads/main", AuditAction: "git.push_deploy",
	}
	if _, _, err := src.DeployFromCommit(ctx, pushInput); err != nil {
		t.Fatalf("push-path enqueue (no dedupe): %v", err)
	}
	if n, qerr := st.CountGitDeploymentsForSHA(ctx, app.ID, sha); qerr != nil || n != 2 {
		t.Fatalf("git deployments after push-path = %d (%v), want 2", n, qerr)
	}
}
