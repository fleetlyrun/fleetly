package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// localImageDomain 是本地构建镜像的命名域（N0 单节点形态：产物导入本机
// daemon；多节点分发随 zot Registry 批次 N1 替换为真仓库地址）。
const localImageDomain = "fleetly.local"

// LocalImageRef 组装本地镜像引用（app + Revision 序号可读形态）。
func LocalImageRef(appID string, revSeq int64) string {
	return fmt.Sprintf("%s/%s:r%d", localImageDomain, strings.ToLower(appID), revSeq)
}

// driveBuilding 是 Deployment 的 building 态驱动：Revision 级构建一次、
// digest 复用；无构建则直通 releasing（领域模型 §4：building 可跳过）。
func (e *Engine) driveBuilding(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	if e.builder == nil {
		return e.failDeployment(ctx, d, "no builder provider wired; image-source deployments are supported in this batch")
	}
	spec, err := e.loadSpec(d.ToRevision)
	if err != nil {
		return e.failDeployment(ctx, d, "load revision spec: "+err.Error())
	}
	if spec.GetBuild() == nil {
		// 无构建声明（如回放路径）直通下发。
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateBuilding}, deployment.StateReleasing, nil)
	}

	revSeq := int64(0)
	if rev, err := e.revisions.Get(ctx, e.db.Runner(), d.ToRevision); err == nil {
		revSeq = rev.Seq
	}
	existing, err := e.builds.ListByRevision(ctx, e.db.Runner(), d.ToRevision)
	if err != nil {
		return nil, err
	}
	for _, b := range existing {
		switch b.State {
		case build.StateSucceeded:
			return e.transitAndReload(ctx, d,
				[]deployment.State{deployment.StateBuilding}, deployment.StateReleasing, nil)
		case build.StateQueued, build.StateBuilding:
			return nil, nil // 在途：tick 再进
		default:
			// 终态失败（failed/cancelled/expired）→ 部署失败。
			return e.failDeployment(ctx, d, fmt.Sprintf("build %s: %s (%s)", b.ID, b.State, b.Error))
		}
	}

	// 无 Build 行：准备输入并受理（事件 + 审计同事务）。
	input, err := e.prepareBuildInput(ctx, d, spec, revSeq)
	if err != nil {
		return e.failDeployment(ctx, d, "prepare build input: "+err.Error())
	}
	buildID := ulid.Make().String()
	input.BuildID = buildID
	e.buildInputMu.Lock()
	e.buildInputs[buildID] = input
	e.buildInputMu.Unlock()

	err = e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.builds.Create(ctx, tx, &build.Build{
			ID: buildID, AppID: d.AppID, RevisionID: d.ToRevision, State: build.StateQueued,
		}); err != nil {
			return err
		}
		if _, err := e.outbox.Append(ctx, tx, eventBuildState(build.StateQueued), "build", buildID,
			buildEventPayloadJSON(&build.Build{
				ID: buildID, AppID: d.AppID, RevisionID: d.ToRevision, State: build.StateQueued,
			})); err != nil {
			return err
		}
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Source: audit.SourceSystem,
			Action: "build.create", Resource: "build/" + buildID,
		})
	})
	if err != nil {
		return nil, err
	}
	e.buildLoop.Kick()
	return nil, nil // 等构建完成（tick 再进）
}

// prepareBuildInput 组装构建输入：git 源浅检出到数据根（幂等：已存在跳过
// ——Revision 冻结体的检出是纯函数）；Target 是本地镜像命名域引用。
func (e *Engine) prepareBuildInput(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, revSeq int64) (capability.BuildRequest, error) {
	git := spec.GetSource().GetGit()
	if git == nil {
		return capability.BuildRequest{}, fmt.Errorf("build requires a git source (upload lands with the automation batch)")
	}
	if e.opts.DataRoot == "" {
		return capability.BuildRequest{}, fmt.Errorf("data root is not configured for source checkout")
	}
	dir := filepath.Join(e.opts.DataRoot, "contexts", d.ToRevision)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := e.cloneSource(ctx, git.GetRepo(), git.GetRef(), dir); err != nil {
			return capability.BuildRequest{}, err
		}
	}
	dockerfile := spec.GetBuild().GetDockerfile()
	return capability.BuildRequest{
		ContextDir: dir,
		Dockerfile: dockerfile,
		Target:     LocalImageRef(d.AppID, revSeq),
	}, nil
}

// cloneSource 浅检出（ref 为分支/tag；commit 精确检出用 checkout 二段式）。
// Token 材料只进命令参数不进日志（ADR-0009：Token 不进日志路径段）。
func (e *Engine) cloneSource(ctx context.Context, repo, ref, dir string) error {
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	// ref 可为 commit sha：先浅克隆默认分支再 checkout 会失败（浅历史不含
	// 目标 commit）——对 sha 形态退化为全量 clone + checkout（小仓可接受；
	// blobless 优化随 Git 集成批次）。
	isSHA := isHexSHA(ref)
	args := []string{"clone", "--quiet"}
	if !isSHA {
		args = append(args, "--depth", "1", "--branch", ref)
	}
	args = append(args, repo, dir)
	cmd := exec.CommandContext(cloneCtx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// 输出可能含 URL（带 token）——剥离 repo 串后再入错误文本。
		return fmt.Errorf("git clone: %v: %s", err, stripSecret(out, repo))
	}
	if isSHA {
		// ref 取自 Revision 冻结体（已冻结的不可变输入，非调用方实时注入）。
		co := exec.CommandContext(cloneCtx, "git", "-C", dir, "checkout", "--quiet", ref) //nolint:gosec // ref 来自冻结体
		if out, err := co.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s: %v: %s", ref, err, stripSecret(out, repo))
		}
	}
	return nil
}

// isHexSHA 报告 ref 是否 40 位 hex（commit 形态；短 sha 7 位同样处理）。
func isHexSHA(ref string) bool {
	if len(ref) != 40 && len(ref) != 7 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// stripSecret 从命令输出中剥离仓库 URL（含 token 形态）。
func stripSecret(out []byte, secret string) string {
	s := string(out)
	if secret == "" {
		return s
	}
	// 只保留错误骨架：URL 行替换为占位。
	if i := strings.Index(s, "https://"); i >= 0 {
		if j := strings.IndexAny(s[i:], " \n\r\t"); j > 0 {
			s = s[:i] + "<repo-url>" + s[i+j:]
		}
	}
	return s
}
