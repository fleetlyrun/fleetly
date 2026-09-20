package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/gitserver"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DeployFromGit 分支过滤映射契约（H2 修复 / MG-C1）：端口返回
// gitserver.ErrBranchNotTracked → 非 gRPC 错误的 skipped 回执
// （deployment_id 留空——钩子脚本把响应 JSON 打到 pusher stderr）+
// git.ignored_branch 处置审计；正常入队回执不受影响。分支比对的权威
// 谓词在 gitserver 侧（deploy_test.go 锁定），此处只锁 API 面映射。

// stubGitTriggers 是 GitDeployTriggers 的桩（固定返回哨兵或部署记录）。
type stubGitTriggers struct {
	rec state.DeployRecord
	err error
}

func (g *stubGitTriggers) DeployFromGitPush(ctx context.Context, app, sha, ref, actorTokenID string) (state.DeployRecord, []compose.Warning, error) {
	return g.rec, nil, g.err
}

func TestDeployFromGitBranchNotTrackedSkips(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	const sha = "0123456789abcdef0123456789abcdef01234567"

	// 哨兵 → skipped 回执（不是 gRPC 错误；App 回显、deployment_id 留空）。
	svc := NewDeploymentsService(st, &stubGitTriggers{err: gitserver.ErrBranchNotTracked})
	resp, err := svc.DeployFromGit(ctx, &serverv1.DeployFromGitRequest{
		App: "my-api", Sha: sha, Ref: "refs/heads/dev",
	})
	if err != nil {
		t.Fatalf("DeployFromGit returned error %v, want skipped receipt", err)
	}
	if resp.Status != "skipped" || resp.DeploymentId != "" || resp.App != "my-api" {
		t.Fatalf("skipped receipt = %+v", resp)
	}

	// 处置审计：git.ignored_branch（机器动作 actor=system；app 行不存在 →
	// target 退化为名形态；diff 携带被忽略的 ref）。
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "git.ignored_branch" && a.Result == "ok" && a.Actor == "system" &&
			a.Target == "app:my-api" && strings.Contains(a.DiffSummary, "refs/heads/dev") {
			found = true
		}
	}
	if !found {
		t.Fatal("git.ignored_branch audit missing")
	}

	// 对照：正常入队回执（queued + deployment id）不受过滤分支影响。
	ok := NewDeploymentsService(st, &stubGitTriggers{rec: state.DeployRecord{
		ID: "dep-1", AppName: "my-api", Status: state.DeployQueued,
	}})
	resp2, err := ok.DeployFromGit(ctx, &serverv1.DeployFromGitRequest{
		App: "my-api", Sha: sha, Ref: "refs/heads/main",
	})
	if err != nil {
		t.Fatalf("tracked DeployFromGit: %v", err)
	}
	if resp2.Status != string(state.DeployQueued) || resp2.DeploymentId != "dep-1" || resp2.App != "my-api" {
		t.Fatalf("tracked receipt = %+v", resp2)
	}
}

// ── 破坏性变更门控（MG-C3，架构 §2.4 plan/apply 语义）─────────────────────

// gateBaseCompose 基线：web + worker 两服务。
const gateBaseCompose = `
name: gateapp
services:
  web:
    image: nginx:1.27
  worker:
    image: my/worker
`

// gateRemovedCompose 删 worker（destructive）。
const gateRemovedCompose = `
name: gateapp
services:
  web:
    image: nginx:1.27
`

// gateChangedCompose 仅改 web 镜像（非 destructive）。
const gateChangedCompose = `
name: gateapp
services:
  web:
    image: nginx:1.28
  worker:
    image: my/worker
`

// seedGateRevision 落一条成功版本快照作为门控基线（ComposeNormalized =
// canonical JSON，与引擎 succeedDeployment 同形态），返回 app 行。
func seedGateRevision(t *testing.T, st *state.Store, yamlText string) state.App {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	spec, _, err := compose.Load(context.Background(), path)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	app, err := st.CreateApp(context.Background(), "", spec.Name)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	raw, err := spec.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if err := st.InTx(context.Background(), func(tx *state.Tx) error {
		_, err := tx.CreateRevision(context.Background(), state.RevisionWrite{
			AppID: app.ID, ComposeNormalized: string(raw), DesiredHash: spec.SpecHash,
		})
		return err
	}); err != nil {
		t.Fatalf("create revision: %v", err)
	}
	return app
}

// deploymentRowCount 数该 app 的部署行（「不入队」断言的读点）。
func deploymentRowCount(t *testing.T, st *state.Store, appName string) int {
	t.Helper()
	app, err := st.GetAppByName(context.Background(), appName)
	if err != nil {
		t.Fatalf("GetAppByName %s: %v", appName, err)
	}
	rows, err := st.ListAppDeployments(context.Background(), app.ID, 100)
	if err != nil {
		t.Fatalf("ListAppDeployments: %v", err)
	}
	return len(rows)
}

// TestDeployConfirmDestructiveGate MG-C3 机制测试：有历史 revision 且新
// compose 删除服务 → 无 confirm 被拒（E_DEPLOY_CONFIRM_REQUIRED 信封 +
// 修复建议文案，无部署行）；带 confirm → 入队成功；无服务删除（仅修改）
// → 不需要 confirm 即入队；首发（无 revision）恒放行。
func TestDeployConfirmDestructiveGate(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	deploys := serverv1.NewDeploymentsServiceClient(env.conn)

	seedGateRevision(t, env.st, gateBaseCompose)

	// 删除服务、未确认：拒绝（信封 code、409 映射、建议文案含
	// --confirm-destructive），且不入队（部署行仍为 0）。
	_, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "gateapp",
		Compose: []byte(gateRemovedCompose),
	})
	if err == nil {
		t.Fatal("service-removing deploy without confirm must be rejected")
	}
	e, ok := apperr.FromError(err)
	if !ok {
		t.Fatalf("rejection error carries no envelope: %v", err)
	}
	if e.Code() != "E_DEPLOY_CONFIRM_REQUIRED" {
		t.Fatalf("envelope code = %q, want E_DEPLOY_CONFIRM_REQUIRED", e.Code())
	}
	if e.HTTPStatus() != 409 {
		t.Errorf("HTTP mapping = %d, want 409", e.HTTPStatus())
	}
	if sug := e.Envelope().GetSuggestion(); !strings.Contains(sug, "--confirm-destructive") {
		t.Errorf("suggestion text missing the --confirm-destructive retry hint: %q", sug)
	}
	if n := deploymentRowCount(t, env.st, "gateapp"); n != 0 {
		t.Fatalf("rejected deploy must not enqueue: deployment rows = %d", n)
	}

	// 同一 compose、带 confirm：入队成功（queued + 部署行 1）。
	dr, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:                "gateapp",
		Compose:            []byte(gateRemovedCompose),
		ConfirmDestructive: true,
	})
	if err != nil {
		t.Fatalf("confirmed deploy: %v", err)
	}
	if dr.GetStatus() != "queued" || dr.GetDeploymentId() == "" {
		t.Fatalf("confirmed receipt = %+v", dr)
	}
	if n := deploymentRowCount(t, env.st, "gateapp"); n != 1 {
		t.Fatalf("after confirm it should enqueue: deployment rows = %d, want 1", n)
	}

	// 仅修改（无服务删除）：不需要 confirm 即入队（部署行 2）。
	dr2, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "gateapp",
		Compose: []byte(gateChangedCompose),
	})
	if err != nil {
		t.Fatalf("modify-only deploy should not require confirm: %v", err)
	}
	if dr2.GetStatus() != "queued" {
		t.Fatalf("modify-only receipt = %+v", dr2)
	}
	if n := deploymentRowCount(t, env.st, "gateapp"); n != 2 {
		t.Fatalf("no service removal should enqueue: deployment rows = %d, want 2", n)
	}

	// 首发（无历史 revision）：无基线可比，恒放行。
	dr3, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "freshapp",
		Compose: []byte("name: freshapp\nservices:\n  web:\n    image: nginx:alpine\n"),
	})
	if err != nil {
		t.Fatalf("first deploy must pass the gate: %v", err)
	}
	if dr3.GetStatus() != "queued" {
		t.Fatalf("first deploy receipt = %+v", dr3)
	}
}

// TestDeployTempDirCleanedUp MG-6 回归：Deploy 的解析中转临时目录
// （os.MkdirTemp("fleetly-compose-")）随请求回收（defer os.RemoveAll）——
// 成功与被拒两条路径都不留孤儿 tmp（持久化副本在 <数据根>/deployments/
// <id>/compose.yaml，tmp 不是契约面）。
func TestDeployTempDirCleanedUp(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	deploys := serverv1.NewDeploymentsServiceClient(env.conn)

	countTmp := func() int {
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

	before := countTmp()
	// 被拒路径（compose 名与请求 app 错位——在 ensureApp 之前返回）。
	if _, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "nomatch",
		Compose: []byte("name: otherapp\nservices:\n  web:\n    image: nginx:alpine\n"),
	}); err == nil {
		t.Fatal("app-name mismatch deploy must be rejected")
	}
	// 成功路径（入队）。
	if _, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "tmpclean",
		Compose: []byte("name: tmpclean\nservices:\n  web:\n    image: nginx:alpine\n"),
	}); err != nil {
		t.Fatalf("normal deploy: %v", err)
	}
	if after := countTmp(); after != before {
		t.Fatalf("parse-time staging temp dirs not reclaimed: fleetly-compose-* dir count %d → %d", before, after)
	}
}

// TestDeployAppNameMismatchRejected A1（S18）：compose 应用名与请求 app 不
// 一致 → E_COMPOSE_UNSUPPORTED（信封携带 expected/actual 上下文），且不
// 误建 app、不入队（拒绝发生在 ensureApp 之前）；一致 → 正常入队。
func TestDeployAppNameMismatchRejected(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	deploys := serverv1.NewDeploymentsServiceClient(env.conn)

	// 不一致：拒绝（REST 路径 {app}=urlapp、compose name=composeapp 的
	// 静默错位形态）。
	_, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "urlapp",
		Compose: []byte("name: composeapp\nservices:\n  web:\n    image: nginx:alpine\n"),
	})
	if err == nil {
		t.Fatal("app-name mismatch must be rejected")
	}
	e, ok := apperr.FromError(err)
	if !ok {
		t.Fatalf("rejection error carries no envelope: %v", err)
	}
	if e.Code() != "E_COMPOSE_UNSUPPORTED" {
		t.Fatalf("envelope code = %q, want E_COMPOSE_UNSUPPORTED", e.Code())
	}
	if e.Context()["expected"] != "urlapp" || e.Context()["actual"] != "composeapp" {
		t.Fatalf("envelope context = %v, want expected=urlapp actual=composeapp", e.Context())
	}

	// 不误建：两个名字的 app 行都不存在。
	for _, name := range []string{"urlapp", "composeapp"} {
		if _, gerr := env.st.GetAppByName(ctx, name); !errors.Is(gerr, state.ErrAppNotFound) {
			t.Fatalf("rejected request must not create app %s: %v", name, gerr)
		}
	}
	// 不入队：无任何在途部署行。
	if rows, lerr := env.st.ListNonTerminalDeployments(ctx); lerr != nil || len(rows) != 0 {
		t.Fatalf("rejected request must not enqueue: rows=%d err=%v", len(rows), lerr)
	}

	// 一致：正常入队（queued + 部署行 1）。
	dr, err := deploys.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{
		App:     "composeapp",
		Compose: []byte("name: composeapp\nservices:\n  web:\n    image: nginx:alpine\n"),
	})
	if err != nil {
		t.Fatalf("matching-names Deploy: %v", err)
	}
	if dr.GetStatus() != "queued" || dr.GetDeploymentId() == "" {
		t.Fatalf("matching receipt = %+v", dr)
	}
	if n := deploymentRowCount(t, env.st, "composeapp"); n != 1 {
		t.Fatalf("matching names should enqueue: deployment rows = %d, want 1", n)
	}
}
