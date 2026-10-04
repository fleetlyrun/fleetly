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
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/upload"
)

// buildRepo 组装构建产物所在的仓名（不含 registry 地址；<projectID>/<appID>
// 双段小写——docker 引用要求仓库名小写，ULID 大写形态无损小写化）。本文件
// 是本地镜像命名的唯一真源（勿散落副本）。前纲布局是 per-Project 凭证域
// 隔离的地基（ADR-0036 N2 兑现节 2）；存量扁平行的回退见 buildDigests。
func buildRepo(projectID, appID string) string {
	return strings.ToLower(projectID) + "/" + strings.ToLower(appID)
}

// LocalImageRef 组装构建推送目标（tag 形态：受管仓库地址 + Project 前纲 +
// app + Revision 序号可读 tag；仅作推送目标与本机缓存命名——Revision 冻结
// 的是 digest，ADR-0019 附录 B.4）。
func LocalImageRef(registryAddr, projectID, appID string, revSeq int64) string {
	return fmt.Sprintf("%s/%s:r%d", registryAddr, buildRepo(projectID, appID), revSeq)
}

// LocalImageDigestRef 组装 from_build 的下发引用（digest 形态：绕开 tag
// 语义的全部 docker29 真机坑——digest-pull 不落 tag、save/load 丢 tag）。
// repo 是产物所在仓名：新构建 = buildRepo 前纲；存量行（repo 空）= 扁平
// 回退 lower(appID)——由调用方 buildDigests 判别。
func LocalImageDigestRef(registryAddr, repo, digest string) string {
	return fmt.Sprintf("%s/%s@%s", registryAddr, repo, digest)
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

	// 无 Build 行：准备输入并受理（事件 + 审计同事务）。projectID 解析自
	// App 行（推送目标前纲与 Build 行 repo 同源一次定型）。
	a, err := e.apps.Get(ctx, e.db.Runner(), d.AppID)
	if err != nil {
		return e.failDeployment(ctx, d, "resolve app for build: "+err.Error())
	}
	input, err := e.prepareBuildInput(ctx, d, spec, revSeq, a.ProjectID)
	if err != nil {
		return e.failDeployment(ctx, d, "prepare build input: "+err.Error())
	}
	buildID := ulid.Make().String()
	input.BuildID = buildID
	e.build.inputsMu.Lock()
	e.build.inputs[buildID] = input
	e.build.inputsMu.Unlock()

	err = e.db.Tx(ctx, func(tx *sql.Tx) error {
		queued := &build.Build{
			ID: buildID, AppID: d.AppID, RevisionID: d.ToRevision, State: build.StateQueued,
			Repo: buildRepo(a.ProjectID, d.AppID),
		}
		return e.commitWrite(ctx, tx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error { return e.builds.Create(ctx, tx, queued) },
			events: []func() eventFact{func() eventFact {
				return eventFact{name: eventBuildState(build.StateQueued), aggregate: "build", id: buildID,
					payload: buildEventPayloadJSON(queued)}
			}},
			audits: []auditFact{{action: "build.create", resource: "build/" + buildID}},
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
// 存量在途行（repo 空，前纲切换升级窗口）按新公式重放输入——产物将落
// 前纲仓，repo 补章与之一致（StampRepo 幂等一次性）。
func (e *Engine) ensureBuildInput(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, revSeq int64, b *build.Build) error {
	e.build.inputsMu.Lock()
	_, ok := e.build.inputs[b.ID]
	e.build.inputsMu.Unlock()
	if ok {
		return nil // 本进程已登记（正常路径）
	}
	a, err := e.apps.Get(ctx, e.db.Runner(), d.AppID)
	if err != nil {
		return fmt.Errorf("resolve app for build input rebuild: %w", err)
	}
	input, err := e.prepareBuildInput(ctx, d, spec, revSeq, a.ProjectID)
	if err != nil {
		return err
	}
	input.BuildID = b.ID
	e.build.inputsMu.Lock()
	e.build.inputs[b.ID] = input
	e.build.inputsMu.Unlock()
	if b.Repo == "" {
		if err := e.builds.StampRepo(ctx, e.db.Runner(), b.ID, buildRepo(a.ProjectID, d.AppID)); err != nil {
			return fmt.Errorf("stamp legacy build repo: %w", err)
		}
	}
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
// 是受管仓库推送目标（Project 前纲 tag 形态）+ 推送凭证（per-Project 铸造
// 分发，ADR-0036 N2 兑现节 2；面缺席回退平台凭证——升级零扰动序）。
func (e *Engine) prepareBuildInput(ctx context.Context, d *deployment.Deployment, spec *specv1.AppSpec, revSeq int64, projectID string) (capability.BuildRequest, error) {
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
	if projectCred, err := e.projectPushCred(ctx, projectID, endpoint); err != nil {
		return capability.BuildRequest{}, err
	} else if projectCred != nil {
		pushCred = projectCred
	}
	if pushCred.Username == "" && pushCred.Secret == "" {
		pushCred = nil // 匿名仓库合法形态（受管 zot 恒有凭证；缺省留 nil）
	}
	input := capability.BuildRequest{
		// builderName 按 strategy oneof 消歧（受理面已执法 builder 名与形态
		// 配对；strategy 缺席 = 存量行，防御性按 dockerfile 路由——存量全部
		// 是 dockerfile strategy，见 ADR-0032 决策 8）。
		Builder:    builderName(spec.GetBuild()),
		ContextDir: contextDir,
		Target:     LocalImageRef(endpoint.Addr, projectID, d.AppID, revSeq),
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

// registryProjectEndpoints 返回 Registry 的 per-Project 端点面（nil = 面
// 未提供，调用方回退平台凭证——升级零扰动序）。
func (e *Engine) registryProjectEndpoints() capability.ProjectEndpoints {
	if e.registry == nil {
		return nil
	}
	return capability.FacesOf(e.registry).ProjectEndpoints
}

// projectPushCred 解析该 Project 的推送凭证（zot per-Project 用户推自家
// 前纲仓，ADR-0036 N2 兑现节 2）。面缺席返回 nil（调用方用平台凭证）；
// 面在场但铸造失败/端点地址不一致 → 精确失败（部署 fail loud，不静默降
// 级平台凭证——那会把域隔离静默升级成全域写）。
func (e *Engine) projectPushCred(ctx context.Context, projectID string, platform capability.RegistryEndpoint) (*capability.RegistryCredential, error) {
	pe := e.registryProjectEndpoints()
	if pe == nil {
		return nil, nil
	}
	ep, err := pe.EndpointForProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("resolve project registry credential for %s: %w", projectID, err)
	}
	if ep.Addr != platform.Addr {
		return nil, fmt.Errorf("project registry endpoint %q does not match the managed address %q", ep.Addr, platform.Addr)
	}
	if ep.Cred.Username == "" || ep.Cred.Secret == "" {
		return nil, fmt.Errorf("project registry credential for %s is empty", projectID)
	}
	return &ep.Cred, nil
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
	args := gitCloneArgs(repo, ref, dir)
	// nolint:gosec // G204 变量子进程的信任域在受理面：repo 经 hooks.go 的
	// https:// 白名单+控制字符拒绝，argv 构造带 -- 分隔与 protocol.ext/file
	// .allow=never 双防线（58f901d）——此处非注入面。
	cmd := exec.CommandContext(cloneCtx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// 输出可能含 URL（带 token）——剥离 repo 串后再入错误文本。
		return fmt.Errorf("git clone: %v: %s", err, stripSecret(out, repo))
	}
	if isHexSHA(ref) {
		// ref 取自 Revision 冻结体（已冻结的不可变输入，非调用方实时注入）。
		co := exec.CommandContext(cloneCtx, "git", "-C", dir, "checkout", "--quiet", ref) //nolint:gosec // ref 来自冻结体
		if out, err := co.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s: %v: %s", ref, err, stripSecret(out, repo))
		}
	}
	return nil
}

// gitCloneArgs 组装 `git` 的 clone argv（单测钉死的执行面防线；两个 ref
// 形态——sha 全量 / 分支浅克隆——都过同一构造）：
//   - `-c protocol.ext.allow=never -c protocol.file.allow=never`（必须在
//     clone 子命令之前）：禁 ext/file 传输——`ext::sh -c ...` 等价任意命令
//     执行、本地路径 clone 可被用于越权读文件。这是纵深防线的存量行兜底：
//     受理面校验只拦新写入，已冻结进 Revision GitSource 的 repo 仍会走到
//     这里（P0）。
//   - `--` 终结选项解析：repo/dir 即使以 `-` 开头（如 `--upload-pack=`
//     形态）也不再被吞作旗标执行。
func gitCloneArgs(repo, ref, dir string) []string {
	args := []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"clone", "--quiet",
	}
	if !isHexSHA(ref) {
		// ref 可为 commit sha：先浅克隆默认分支再 checkout 会失败（浅历史
		// 不含目标 commit）——对 sha 形态退化为全量 clone + checkout（小仓
		// 可接受；blobless 优化随 Git 集成批次）。
		args = append(args, "--depth", "1", "--branch", ref)
	}
	return append(args, "--", repo, dir)
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
