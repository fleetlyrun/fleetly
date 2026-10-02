package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/upload"
)

// LocalImageRef 组装构建推送目标（tag 形态：受管仓库地址 + app + Revision
// 序号可读 tag；仅作推送目标与本机缓存命名——Revision 冻结的是 digest，
// ADR-0019 附录 B.4）。本文件是本地镜像命名的唯一真源（勿散落副本）。
func LocalImageRef(registryAddr, appID string, revSeq int64) string {
	return fmt.Sprintf("%s/%s:r%d", registryAddr, strings.ToLower(appID), revSeq)
}

// LocalImageDigestRef 组装 from_build 的下发引用（digest 形态：绕开 tag
// 语义的全部 docker29 真机坑——digest-pull 不落 tag、save/load 丢 tag）。
func LocalImageDigestRef(registryAddr, appID, digest string) string {
	return fmt.Sprintf("%s/%s@%s", registryAddr, strings.ToLower(appID), digest)
}

// driveBuilding 是 Deployment 的 building 态驱动：Revision 级构建一次、
// digest 复用；无构建则直通 releasing（领域模型 §4：building 可跳过）。
func (e *Engine) driveBuilding(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	if len(e.builders) == 0 {
		return e.failDeployment(ctx, d, "no builder provider wired; image-source deployments are supported in this batch")
	}
	// revSeq 是构建产物命名锚（LocalImageRef）：读取失败必须硬失败——静默
	// 用 0 会撞 r0 tag（审计 Q-9；同驱动步内 revision 必在）。
	rev, err := e.revisions.Get(ctx, e.db.Runner(), d.ToRevision)
	if err != nil {
		return e.failDeployment(ctx, d, "resolve revision for build: "+err.Error())
	}
	spec, err := e.loadSpec(ctx, d.ToRevision)
	if err != nil {
		return e.failDeployment(ctx, d, "load revision spec: "+err.Error())
	}
	if spec.GetBuild() == nil {
		// 无构建声明（如回放路径）直通下发。
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateBuilding}, deployment.StateReleasing, nil)
	}

	revSeq := rev.Seq
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
			// 在途：确认进程内输入登记仍在（D-4：重启丢失时在此幂等重建，
			// 否则构建循环拾取后永陷无输入路径），然后等构建推进。
			if err := e.ensureBuildInput(ctx, d, spec, revSeq, &b); err != nil {
				return e.failDeployment(ctx, d, "rebuild build input: "+err.Error())
			}
			return nil, nil // 等构建完成（tick 再进）
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

// ensureBuildInput 保证在途 Build 行有进程内输入登记（D-4 根修）：
// buildInputs 是内存表，重启即丢——既有 queued/building 行在此幂等重建
// （prepareBuildInput 本身幂等：检出目录已存在即跳过 clone），重建失败
// 走 failDeployment（调用方收口）。building 行且无登记 = 本进程没有执行
// goroutine（崩溃遗留且未经 Start 重置）→ 回 queued 交构建循环重拾。
func (e *Engine) ensureBuildInput(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, revSeq int64, b *build.Build) error {
	e.buildInputMu.Lock()
	_, ok := e.buildInputs[b.ID]
	e.buildInputMu.Unlock()
	if ok {
		return nil // 本进程已登记（正常路径）
	}
	input, err := e.prepareBuildInput(ctx, d, spec, revSeq)
	if err != nil {
		return err
	}
	input.BuildID = b.ID
	e.buildInputMu.Lock()
	e.buildInputs[b.ID] = input
	e.buildInputMu.Unlock()
	if b.State == build.StateBuilding {
		// CAS 冲突 = 并发写者已迁移该行（如竞态回 queued）：容忍，下拍复查。
		if _, err := e.transitBuild(ctx, b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); err != nil && !errors.Is(err, state.ErrConflict) {
			return err
		}
	}
	e.buildLoop.Kick() // 登记即拾取（不等下个兜底拍）
	return nil
}

// prepareBuildInput 组装构建输入：git 源浅检出 / 上传产物解包到数据根
// （幂等：已存在跳过——Revision 冻结体 + 内容寻址 blob = 纯函数）；Target
// 是受管仓库推送目标（tag 形态）+ 平台推送凭证（附录 B.3 分发面②）。
func (e *Engine) prepareBuildInput(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, revSeq int64) (capability.BuildRequest, error) {
	endpoint, err := e.registryEndpoint(ctx)
	if err != nil {
		return capability.BuildRequest{}, err
	}
	var contextDir string
	switch origin := spec.GetSource().GetKind().(type) {
	case *specv1.Source_Git:
		if e.opts.DataRoot == "" {
			return capability.BuildRequest{}, fmt.Errorf("data root is not configured for source checkout")
		}
		dir := filepath.Join(e.opts.DataRoot, "contexts", d.ToRevision)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := e.cloneSource(ctx, origin.Git.GetRepo(), origin.Git.GetRef(), dir); err != nil {
				return capability.BuildRequest{}, err
			}
		}
		contextDir = dir
	case *specv1.Source_Upload:
		dir, err := e.extractUploadContext(ctx, origin.Upload.GetId(), d.ToRevision)
		if err != nil {
			return capability.BuildRequest{}, err
		}
		contextDir = dir
	default:
		return capability.BuildRequest{}, fmt.Errorf("build requires a git or upload source")
	}
	pushCred := &endpoint.Cred
	if endpoint.Cred.Username == "" && endpoint.Cred.Secret == "" {
		pushCred = nil // 匿名仓库合法形态（受管 zot 恒有凭证；缺省留 nil）
	}
	input := capability.BuildRequest{
		// builderName 按 strategy oneof 消歧（受理面已执法 builder 名与形态
		// 配对；strategy 缺席 = 存量行，防御性按 dockerfile 路由——存量全部
		// 是 dockerfile strategy，见 ADR-0032 决策 8）。
		Builder:    builderName(spec.GetBuild()),
		ContextDir: contextDir,
		Target:     LocalImageRef(endpoint.Addr, d.AppID, revSeq),
		PushCred:   pushCred,
	}
	switch strategy := spec.GetBuild().GetStrategy().(type) {
	case *specv1.BuildSpec_Railpack:
		input.Railpack = &capability.RailpackInput{PinnedVersion: strategy.Railpack.GetPinnedVersion()}
	case *specv1.BuildSpec_Static:
		outputDir := strategy.Static.GetOutputDir()
		if outputDir == "" {
			outputDir = "." // 归一化面恒填充；防御性兜底
		}
		input.Static = &capability.StaticInput{OutputDir: outputDir}
	default:
		input.Dockerfile = spec.GetBuild().GetDockerfile()
	}
	return input, nil
}

// builderName 按 strategy oneof 返回路由名（strategy 缺席 = dockerfile，
// 存量行防御轨；受理面 ValidateBuild 保证新行名与形态配对）。
func builderName(b *specv1.BuildSpec) string {
	switch b.GetStrategy().(type) {
	case *specv1.BuildSpec_Railpack:
		return specir.BuilderRailpack
	case *specv1.BuildSpec_Static:
		return specir.BuilderStatic
	default:
		return specir.BuilderDockerfile
	}
}

// registryEndpoint 解析受管仓库端点（推送目标/下发引用/平台凭证的共同
// 真源）。无 Registry Provider = build 源部署的精确失败（附录 B.5①——
// 不做本机导入退化形态：裸 image ID 引用真机 swarm 不可拉取）。
func (e *Engine) registryEndpoint(ctx context.Context) (capability.RegistryEndpoint, error) {
	if e.registry == nil {
		return capability.RegistryEndpoint{}, fmt.Errorf("no registry provider wired; build-source deployments require the managed registry (set FLEETLY_REGISTRY_ADDR on the control plane)")
	}
	endpoint, err := e.registry.Endpoint(ctx)
	if err != nil {
		return capability.RegistryEndpoint{}, fmt.Errorf("resolve managed registry endpoint: %w", err)
	}
	if endpoint.Addr == "" {
		return capability.RegistryEndpoint{}, fmt.Errorf("managed registry endpoint returned an empty address")
	}
	return endpoint, nil
}

// extractUploadContext 把上传产物 blob 解包为构建上下文（ADR-0019 附录
// A.4）：行回行 digest → blob → tar 安全解包到 tmp 目录后原子 rename 到
// contexts/<revision>（幂等：目标已存在跳过；解包失败 tmp 全弃，不留半
// 截上下文被后续拍误判完整）。
func (e *Engine) extractUploadContext(ctx context.Context, uploadID, revisionID string) (string, error) {
	if e.opts.DataRoot == "" {
		return "", fmt.Errorf("data root is not configured for source extraction")
	}
	row, err := e.uploads.Get(ctx, e.db.Runner(), uploadID)
	if err != nil {
		return "", fmt.Errorf("resolve upload source %s: %w", uploadID, err)
	}
	dir := filepath.Join(e.opts.DataRoot, "contexts", revisionID)
	if _, serr := os.Stat(dir); serr == nil {
		return dir, nil // 幂等：Revision 冻结体 + 内容寻址 blob，已解包即纯函数结果
	}
	blob := upload.BlobPath(e.opts.DataRoot, row.Digest)
	f, err := os.Open(blob) //nolint:gosec // 数据根私有目录
	if err != nil {
		return "", fmt.Errorf("open upload blob for digest %s (upload %s): %w", row.Digest, uploadID, err)
	}
	tmp := dir + ".extracting"
	_ = os.RemoveAll(tmp) // 上次失败遗留（正常路径 rename 已带走）
	if err := upload.ExtractTar(tmp, f, row.SizeBytes); err != nil {
		_ = f.Close()
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("extract upload %s: %w", uploadID, err)
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("close upload blob: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		// 并发解包（同 Revision 双驱动）先行落位：容忍并清理本侧 tmp。
		if _, serr := os.Stat(dir); serr == nil {
			_ = os.RemoveAll(tmp)
			return dir, nil
		}
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("publish extracted context: %w", err)
	}
	return dir, nil
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
