package gitserver

import (
	"context"
	"errors"
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
		t.Fatal("second push reused deployment id (去重语义泄漏到 push 路径)")
	}
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
