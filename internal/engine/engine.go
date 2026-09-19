package engine

// 发布引擎主链路（T2.10/T2.11）：队列拾取（同 app 互斥）→ preparing
// （compose 重载/放置/env/secret）→ building（构建直通核对）→ releasing
// （Swarm 对账 + 健康门 + L2 看门狗）→ observing（L3 观察窗）→ succeeded，
// 失败分流/取消/重启恢复见 recovery.go 与 observing.go。单 goroutine tick
// 推进（行谓词 CAS 保证与 CLI 的并发安全）；状态全部落库——控制面随时
// 重启可续跑。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/envlayer"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Engine 是发布引擎。依赖里的 *secrets.Box 承担两件事：平台 env 密文解密
// （合并输入）与期望态快照加密（desired_spec——归位/重放的执行依据，明文
// 纪律与 env_vars 相同：只以密文落库）。
type Engine struct {
	cfg      Config
	store    *state.Store
	sub      Substrate
	images   ImageChecker
	resolver PlacementResolver
	box      *secrets.Box
	clock    Clock
	log      *slog.Logger
	// routes 是入口路由发布端口（T2.15；nil = 未接入口面——发布挂点
	// 整体跳过。由 WithRoutePublisher 注入，fleetlyd 装配 ingress.Manager）。
	routes RoutePublisher
	// postDeploy 是部署成功后的备份挂钩（T2.22：每次部署成功后触发一次
	// 热备快照，异步不阻塞部署主链；nil = 未接备份面。由 WithPostDeployHook
	// 注入，fleetlyd 装配 statebackup.Manager）。
	postDeploy PostDeployHook
	// waterMarks 是副本水位不足判定的进程内计时（观察窗辅助信号；引擎
	// 重启后重摆——窗口本身持久化，重启代价可接受）。
	waterMarks map[string]time.Time
	// driftSeen 是漂移上报的进程内迁移记忆（no-drift → drift 只报一次；
	// 重启清零 = 漂移存续时重报一次，漏报劣于重复）。
	driftSeen map[string]bool
}

// PlacementResolver 是引擎对放置层的消费端口（internal/placement.Resolver
// 隐式实现；接口在本包定义以便单测注入）。
type PlacementResolver interface {
	Resolve(ctx context.Context, in placement.Input) (placement.Decision, error)
	Apply(ctx context.Context, in placement.Input) (placement.Decision, error)
	Preflight(ctx context.Context, appID string) error
}

// NewEngine 构造发布引擎（时钟缺省真实时钟；水位表初始化）。
func NewEngine(cfg Config, store *state.Store, sub Substrate, images ImageChecker,
	resolver PlacementResolver, box *secrets.Box, log *slog.Logger) *Engine {
	return &Engine{
		cfg:        cfg.Normalize(),
		store:      store,
		sub:        sub,
		images:     images,
		resolver:   resolver,
		box:        box,
		clock:      realClock{},
		log:        log,
		waterMarks: map[string]time.Time{},
		driftSeen:  map[string]bool{},
	}
}

// WithClock 注入时钟（单测）。
func (e *Engine) WithClock(c Clock) *Engine { e.clock = c; return e }

// PostDeployHook 是部署成功后的备份挂钩签名（rec 为成功部署记录的副本）。
type PostDeployHook func(rec state.DeployRecord)

// WithPostDeployHook 注入部署成功挂钩（T2.22 备份触发面：每次部署成功后
// 一次热备快照。引擎侧异步触发——go 例程 + 独立预算在实现方；挂钩失败
// 只落备份台账与告警，绝不回滚/阻塞已成功的部署）。
func (e *Engine) WithPostDeployHook(fn PostDeployHook) *Engine { e.postDeploy = fn; return e }

// Run 启动引擎主循环：启动扫描（控制面重启分类恢复）→ 周期 tick + 漂移
// 扫描。ctx 取消返回 nil（lynx actor 契约由服务壳负责阻塞语义）。
func (e *Engine) Run(ctx context.Context) error {
	e.recoverInterrupted(ctx)
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	driftTicker := time.NewTicker(e.cfg.DriftInterval)
	defer driftTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			e.tick(ctx)
		case <-driftTicker.C:
			e.driftScan(ctx)
		}
	}
}

// Tick 单步推进（测试与诊断入口；生产由 Run 驱动）。
func (e *Engine) Tick(ctx context.Context) { e.tick(ctx) }

// tick 是一个推进周期：队列拾取 → 在途推进 → 窗后巡检。
func (e *Engine) tick(ctx context.Context) {
	e.pickQueued(ctx)
	e.advanceActive(ctx)
	e.watchPostWindow(ctx)
}

// pickQueued 拾取可启动的 queued 部署：同 app 互斥——仅当该 app 无其他
// 在途部署时启动最早一条（第二个入队 queued 等待，release-semantics §2.5
// 并发控制行）。
func (e *Engine) pickQueued(ctx context.Context) {
	queued, err := e.store.NextQueuedDeployments(ctx, 20)
	if err != nil {
		e.log.Warn("engine: scan queued deployments", "error", err)
		return
	}
	startedApps := map[string]bool{}
	for _, q := range queued {
		if startedApps[q.AppID] {
			continue
		}
		// queued 即可取消（未触底座）。
		if q.CancelRequested {
			if err := e.cancelTerminal(ctx, q); err != nil {
				e.log.Warn("engine: cancel queued deployment", "deployment", q.ID, "error", err)
			}
			continue
		}
		has, err := e.store.AppHasNonTerminalDeployment(ctx, q.AppID)
		if err != nil {
			e.log.Warn("engine: mutex check", "app", q.AppID, "error", err)
			continue
		}
		// queued 行自身即在途——仅当该 app 的在途行全部是 queued（无
		// preparing/building/releasing/observing 竞争者）才可启动，且同
		// app 只启动最早一条。
		if has && !e.onlyQueuedForApp(ctx, q.AppID) {
			continue
		}
		if err := e.startQueued(ctx, q); err != nil {
			e.log.Warn("engine: start queued deployment", "deployment", q.ID, "error", err)
		}
		startedApps[q.AppID] = true
	}
}

// onlyQueuedForApp 报告该 app 的在途部署是否全部处于 queued（无执行中
// 竞争者）。
func (e *Engine) onlyQueuedForApp(ctx context.Context, appID string) bool {
	rows, err := e.store.ListNonTerminalDeployments(ctx)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if r.AppID == appID && r.Status != state.DeployQueued {
			return false
		}
	}
	return true
}

// advanceActive 推进全部在途部署一个周期。
func (e *Engine) advanceActive(ctx context.Context) {
	active, err := e.store.ListNonTerminalDeployments(ctx)
	if err != nil {
		e.log.Warn("engine: scan active deployments", "error", err)
		return
	}
	for _, d := range active {
		var err error
		switch d.Status {
		case state.DeployQueued:
			// 等待互斥窗口（pickQueued 负责）。
		case state.DeployPreparing:
			err = e.runPreparing(ctx, d)
		case state.DeployBuilding:
			err = e.runBuilding(ctx, d)
		case state.DeployReleasing:
			err = e.evaluateReleasing(ctx, d)
		case state.DeployObserving:
			err = e.evaluateObserving(ctx, d)
		}
		if err != nil {
			e.log.Warn("engine: advance deployment", "deployment", d.ID, "status", d.Status, "error", err)
		}
	}
}

// startQueued 启动一条 queued 部署：queued → preparing（CAS 谓词防多实例
// 竞争）并同 tick 执行准备。
func (e *Engine) startQueued(ctx context.Context, rec state.DeployRecord) error {
	to := state.DeployPreparing
	from := state.DeployQueued
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{
		Status:     &to,
		PrevStatus: &from,
	}); err != nil {
		return fmt.Errorf("claim queued deployment: %w", err)
	}
	if err := e.store.InTx(ctx, func(tx *state.Tx) error {
		return deploymentEvent(ctx, tx, "deployment.release_started", rec.ID)
	}); err != nil {
		return err
	}
	rec.Status = state.DeployPreparing
	return e.runPreparing(ctx, rec)
}

// prepareResult 是 preparing 阶段的中间产物（reload/放置/env；building 与
// preparing 共用——明文 env 不落库，building 重算，spec_hash 复核漂移）。
type prepareResult struct {
	spec        *compose.Spec
	fileEnv     map[string]map[string]string
	composeEnv  map[string]map[string]string
	platformEnv []envlayer.PlatformVar
	decision    placement.Decision
}

// runPreparing 执行准备阶段：底座就绪 → compose 重载 → 放置解析/绑定/前哨
// → env 提取 → secret 检查 → 分路（build-mode → building；纯镜像 → 直接
// 规划进 releasing）。kind=rollback 分路见 rollback.go（期望态已随入队
// 固化，preflight 后直接对账）。
func (e *Engine) runPreparing(ctx context.Context, rec state.DeployRecord) error {
	if rec.Kind == kindRollback {
		return e.runRollbackPreparing(ctx, rec)
	}
	// cancel（preparing 未触底座：直接落 cancelled）。
	if rec.CancelRequested {
		return e.cancelTerminal(ctx, rec)
	}
	// 准备预算：底座不可用等暂态错误重试到 created+releaseTimeout 为止。
	if !rec.CreatedAt.IsZero() && e.now().Sub(rec.CreatedAt) > e.cfg.ReleaseTimeout {
		return e.failTransition(ctx, rec, "E_RUNTIME_UNAVAILABLE",
			"准备阶段超过发布看门狗预算（底座不可用或环境异常）")
	}
	if err := e.requireSwarm(ctx, rec); err != nil {
		return err // 暂态：下一 tick 重试（预算由上守）
	}

	pre, err := e.prepareInputs(ctx, rec)
	if err != nil {
		return e.failTransitionErr(ctx, rec, err)
	}

	// 分路：任一 build-mode 服务 → building（构建产物核对）。
	hasBuild := false
	for i := range pre.spec.Services {
		if pre.spec.Services[i].Build != nil {
			hasBuild = true
			break
		}
	}
	if hasBuild {
		to := state.DeployBuilding
		from := state.DeployPreparing
		if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{
			Status:     &to,
			PrevStatus: &from,
		}); err != nil {
			return err
		}
		rec.Status = state.DeployBuilding
		return e.runBuilding(ctx, rec)
	}
	return e.planAndRelease(ctx, rec, pre)
}

// runBuilding 执行构建直通核对（release-semantics 场景 2）：build-mode 服务
// 必须已有匹配当前 compose（spec_hash）的成功构建；镜像 digest 取自 builds
// 行（D9）。全部可得 → 规划 → releasing。
func (e *Engine) runBuilding(ctx context.Context, rec state.DeployRecord) error {
	if rec.CancelRequested {
		return e.cancelTerminal(ctx, rec)
	}
	if !rec.CreatedAt.IsZero() && e.now().Sub(rec.CreatedAt) > e.cfg.ReleaseTimeout {
		return e.failTransition(ctx, rec, "E_RUNTIME_UNAVAILABLE",
			"构建核对阶段超过发布看门狗预算")
	}
	if err := e.requireSwarm(ctx, rec); err != nil {
		return err
	}
	pre, err := e.prepareInputs(ctx, rec)
	if err != nil {
		return e.failTransitionErr(ctx, rec, err)
	}
	return e.planAndRelease(ctx, rec, pre)
}

// prepareInputs 重载 compose、解析放置、提取 env 与平台层明文（preparing/
// building 共用；幂等——重启后重入）。
func (e *Engine) prepareInputs(ctx context.Context, rec state.DeployRecord) (*prepareResult, error) {
	spec, _, err := compose.Load(ctx, rec.ComposePath)
	if err != nil {
		return nil, err // compose.Load 已携带 E_COMPOSE_* 信封（场景 1）
	}
	if spec.SpecHash != rec.SpecHash {
		return nil, errorf("E_COMPOSE_UNSUPPORTED",
			"compose 文件自入队后被改写（spec_hash %s → %s）：请取消后重新部署",
			rec.SpecHash, spec.SpecHash)
	}

	// 放置：意图解析 + 绑定落库 + 卷登记（幂等），随后部署前哨快速失败
	//（E_PLACEMENT_NODE_UNAVAILABLE / GONE / E_VOLUME_NODE_MISMATCH）。
	decision, err := e.resolver.Apply(ctx, placement.Input{
		AppID:    rec.AppID,
		AppName:  rec.AppName,
		Volumes:  composeVolumes(spec),
		LabelRef: composePlacementLabel(spec),
	})
	if err != nil {
		return nil, err
	}
	if err := e.resolver.Preflight(ctx, rec.AppID); err != nil {
		return nil, err
	}

	fileEnvs, err := extractServiceEnvs(rec.ComposePath)
	if err != nil {
		return nil, err
	}
	fileEnv := map[string]map[string]string{}
	composeEnv := map[string]map[string]string{}
	for i := range spec.Services {
		svc := &spec.Services[i]
		se := fileEnvs[svc.Name]
		fileEnv[svc.Name] = se.File
		composeEnv[svc.Name] = se.Compose
		// 键集交叉核对：提取器与归一化形态必须同源（文件被换写的防御）。
		if !envKeySetsMatch(svc.Environment, se.File, se.Compose) {
			return nil, errorf("E_COMPOSE_UNSUPPORTED",
				"服务 %s 的 env 键集与归一化形态不一致（compose 文件可能被并发改写）", svc.Name)
		}
	}

	platform, err := e.platformEnvForMerge(ctx, rec.AppID)
	if err != nil {
		return nil, err
	}
	return &prepareResult{
		spec:        spec,
		fileEnv:     fileEnv,
		composeEnv:  composeEnv,
		platformEnv: platform,
		decision:    decision,
	}, nil
}

// planAndRelease 镜像 digest 核对（场景 3）→ 规划 → 快照落库 → releasing。
func (e *Engine) planAndRelease(ctx context.Context, rec state.DeployRecord, pre *prepareResult) error {
	images := map[string]string{}
	for i := range pre.spec.Services {
		svc := &pre.spec.Services[i]
		ref, err := e.resolveImage(ctx, rec, svc)
		if err != nil {
			return e.failTransitionErr(ctx, rec, err)
		}
		images[svc.Name] = ref
	}
	volumes, err := e.store.ListAppVolumes(ctx, rec.AppID)
	if err != nil {
		return e.failTransitionErr(ctx, rec, errorf("E_RUNTIME_UNAVAILABLE", "读取卷注册表失败: %v", err))
	}
	plan, err := BuildPlan(PlanInput{
		AppID:        rec.AppID,
		AppName:      rec.AppName,
		DeploymentID: rec.ID,
		Spec:         pre.spec,
		FileEnv:      pre.fileEnv,
		ComposeEnv:   pre.composeEnv,
		PlatformEnv:  pre.platformEnv,
		Images:       images,
		Decision:     pre.decision,
		Volumes:      volumes,
	})
	if err != nil {
		return e.failTransitionErr(ctx, rec, err)
	}

	snapshot, err := e.box.Encrypt(plan.DesiredSpecJSON)
	if err != nil {
		return e.failTransitionErr(ctx, rec, errorf("E_RUNTIME_UNAVAILABLE", "期望态快照加密失败: %v", err))
	}

	// 规划警告（W_ENV_PLATFORM_OVERRIDE 等）以 deployment.warning 事件披露。
	for _, w := range plan.Warnings {
		if w.Code == "" {
			continue
		}
		if err := e.store.InTx(ctx, func(tx *state.Tx) error {
			return deploymentEvent(ctx, tx, "deployment.warning", rec.ID, "code", w.Code, "service", w.Service)
		}); err != nil {
			return err
		}
	}

	releaseAt := e.now()
	deadline := releaseAt.Add(e.cfg.ReleaseTimeout)
	to := state.DeployReleasing
	from := rec.Status
	specHash := pre.spec.SpecHash
	envHash := plan.EnvSnapshotHash
	desiredHash := plan.DesiredHash
	desiredSpec := string(snapshot)
	if err := e.store.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{
		Status:             &to,
		PrevStatus:         &from,
		ReleaseStartedAt:   &releaseAt,
		WatchdogDeadlineAt: &deadline,
		SpecHash:           &specHash,
		EnvSnapshotHash:    &envHash,
		DesiredHash:        &desiredHash,
		DesiredSpec:        &desiredSpec,
	}); err != nil {
		return err
	}
	rec.Status = state.DeployReleasing
	rec.DesiredSpec = desiredSpec
	rec.ReleaseStartedAt = releaseAt
	rec.WatchdogDeadlineAt = deadline

	// 对账执行（新增/更新/删除）。
	if err := e.applyDesired(ctx, rec, plan.Services, false); err != nil {
		return e.failTransitionErr(ctx, rec, err)
	}
	return nil
}

// resolveImage 裁决单个服务的镜像引用（digest 钉定，D9）：
//   - image 模式：compose 值直通 + 本机 digest 钉定（缺失 →
//     E_IMAGE_PULL_FAILED，场景 3）；
//   - build 模式：builds 表最近一次匹配当前 spec_hash 的成功构建
//     （缺失 → E_BUILD_FAILED，场景 2；`fleetly build` 先行的契约）。
func (e *Engine) resolveImage(ctx context.Context, rec state.DeployRecord, svc *compose.Service) (string, error) {
	if svc.Build == nil {
		ref := svc.Image
		digest, err := e.images.ImageDigest(ctx, ref)
		if err != nil {
			if errors.Is(err, ErrImageMissing) {
				return "", errorf("E_IMAGE_PULL_FAILED",
					"服务 %s 的镜像 %s 本机不可得（v0.1 单节点以本机镜像部署；请先 pull 或 build）", svc.Name, ref)
			}
			return "", errorf("E_RUNTIME_UNAVAILABLE", "镜像检查失败 %s: %v", ref, err)
		}
		return pinDigest(ref, digest), nil
	}

	builds, err := e.store.ListAppBuilds(ctx, rec.AppID, 50)
	if err != nil {
		return "", errorf("E_RUNTIME_UNAVAILABLE", "读取构建历史失败: %v", err)
	}
	for _, b := range builds {
		if b.Service != svc.Name || b.Status != state.BuildSucceeded || b.ImageDigest == "" {
			continue
		}
		req, err := build.DecodeRequest(b.Request)
		if err != nil {
			continue // 历史行损坏：跳过（更早的成功构建仍可命中）
		}
		if req.SpecHash != "" && req.SpecHash != rec.SpecHash {
			continue
		}
		// digest 钉定经镜像端口复核（清单摘要优先；本机构建镜像无清单摘要
		// 时按 tag 引用直用——免 registry 形态，镜像不被平台自动清理）。
		digest, err := e.images.ImageDigest(ctx, b.ImageRef)
		if err != nil {
			if errors.Is(err, ErrImageMissing) {
				continue // 构建产物已被清理：尝试更早的成功构建
			}
			return "", errorf("E_RUNTIME_UNAVAILABLE", "镜像检查失败 %s: %v", b.ImageRef, err)
		}
		return pinDigest(b.ImageRef, digest), nil
	}
	return "", errorf("E_BUILD_FAILED",
		"服务 %s 无匹配当前 compose（spec_hash %.12s）的可用构建：先执行 fleetly build 再部署",
		svc.Name, rec.SpecHash)
}

// pinDigest 把镜像引用钉定为不可变 digest 形态（已是 digest 引用则原样）。
func pinDigest(ref, digest string) string {
	if digest == "" || strings.Contains(ref, "@") {
		return ref
	}
	return ref + "@" + digest
}

// requireSwarm 确认底座就绪；未就绪不是部署失败（暂态），由准备预算守门。
func (e *Engine) requireSwarm(ctx context.Context, rec state.DeployRecord) error {
	if err := e.sub.SwarmReady(ctx); err != nil {
		if errors.Is(err, ErrNotSwarmReady) {
			e.log.Warn("engine: swarm not ready, retrying next tick", "deployment", rec.ID)
			return nil
		}
		return e.failTransitionErr(ctx, rec, errorf("E_RUNTIME_UNAVAILABLE", "底座检查失败: %v", err))
	}
	return nil
}

// platformEnvForMerge 读取并解密平台 env（三层合并第三层）。pending 与
// effective 都参与合并——部署是 pending 的消费点（随本次部署注入，成功后
// MarkAppEnvEffective 提升；失败不提升、下次部署重试，architecture §2.4
// 变量合并行 + 发布引擎消费契约）。解密失败 = 密钥/密文损坏：显式失败不
// 静默降级。
func (e *Engine) platformEnvForMerge(ctx context.Context, appID string) ([]envlayer.PlatformVar, error) {
	rows, err := e.store.ListAppEnv(ctx, appID)
	if err != nil {
		return nil, errorf("E_RUNTIME_UNAVAILABLE", "读取平台 env 失败: %v", err)
	}
	out := make([]envlayer.PlatformVar, 0, len(rows))
	for _, row := range rows {
		plain, err := e.box.Decrypt([]byte(row.Value))
		if err != nil {
			return nil, errorf("E_RUNTIME_UNAVAILABLE",
				"平台 env %s 解密失败（主密钥不匹配或密文损坏）", row.Key)
		}
		out = append(out, envlayer.PlatformVar{Key: row.Key, Value: string(plain), Source: row.Source})
	}
	return out, nil
}

// applyDesired 是 stack 对账核心（architecture §2.4 省略=删除）：期望服务
// 集 vs 实际（fleetly- 前缀 + managed label）——缺失创建、内容/归属更新、
// 多余删除。幂等；服务 label 以当前发布归属重写（任务零替换，Spike B2）。
//
// force 语义：false（发布对账）以 desired-hash label 做跳过捷径；true
// （归位/回滚/漂移收敛的重放路径）**恒下发 ServiceUpdate**——label 是上次
// 平台写的存根，外部改动不清理它：期望与 label 相等不代表实况未被篡改，
// 重放必须以 swarm 侧 spec 重申为准（同内容 ServiceUpdate 不触发任务重建，
// Spike B2 零替换语义不受影响）。
func (e *Engine) applyDesired(ctx context.Context, rec state.DeployRecord, desired []ServiceSpec, force bool) error {
	if err := e.sub.SwarmReady(ctx); err != nil {
		return appErrOf(err, rec.ID)
	}
	if len(desired) == 0 {
		return nil
	}
	// per-app 网络先行（服务创建的前置对象）。
	netName, err := networkNameOf(rec.AppName)
	if err != nil {
		return appErrOf(err, rec.ID)
	}
	if err := e.sub.NetworkEnsure(ctx, netName); err != nil {
		return appErrOf(err, rec.ID)
	}

	existing, err := e.sub.ServiceList(ctx, map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     rec.AppName,
	})
	if err != nil {
		return appErrOf(err, rec.ID)
	}
	byName := map[string]ServiceState{}
	for _, s := range existing {
		byName[s.Name] = s
	}
	desiredNames := map[string]bool{}

	for i := range desired {
		spec := desired[i]
		desiredNames[spec.Name] = true
		// 快照/规划产出的服务 label 不含哈希（哈希后附加）——对账时以当前
		// 发布归属补齐，保证 label 与哈希一致。
		if spec.ServiceLabels == nil {
			spec.ServiceLabels = map[string]string{}
		}
		spec.ServiceLabels[state.LabelDeployment] = rec.ID
		spec.ServiceLabels[state.LabelDesiredHash] = spec.DesiredHash()

		cur, ok := byName[spec.Name]
		switch {
		case !ok:
			if err := e.sub.ServiceCreate(ctx, spec); err != nil {
				return appErrOf(err, rec.ID)
			}
		case force ||
			cur.DesiredHash != spec.DesiredHash() ||
			cur.Labels[state.LabelDeployment] != rec.ID:
			if err := e.sub.ServiceUpdate(ctx, spec.Name, spec); err != nil {
				return appErrOf(err, rec.ID)
			}
		}
	}
	// 省略=删除（compose 移除服务 → 删 Swarm service；卷数据不删）。
	for _, s := range existing {
		if !desiredNames[s.Name] {
			if err := e.sub.ServiceRemove(ctx, s.Name); err != nil {
				return appErrOf(err, rec.ID)
			}
		}
	}
	return nil
}

// scaleToZero 首发失败/取消的保留现场原语（D-REL-5）：期望服务副本清零、
// service/revision 与日志保留。服务不存在（发布未触底座）则跳过。
func (e *Engine) scaleToZero(ctx context.Context, rec state.DeployRecord, specs []ServiceSpec) error {
	for i := range specs {
		spec := specs[i]
		if _, err := e.sub.ServiceInspect(ctx, spec.Name); err != nil {
			if errors.Is(err, ErrServiceNotFound) {
				continue
			}
			return appErrOf(err, rec.ID)
		}
		spec.Replicas = 0
		if err := e.sub.ServiceUpdate(ctx, spec.Name, spec); err != nil {
			return appErrOf(err, rec.ID)
		}
	}
	return nil
}

// ── 快照编解码（desired_spec 密文 ↔ []ServiceSpec）─────────────────────────

func (e *Engine) decodeSpecs(rec state.DeployRecord) ([]ServiceSpec, error) {
	if rec.DesiredSpec == "" {
		return nil, fmt.Errorf("engine: deployment %s has no desired-spec snapshot", rec.ID)
	}
	plain, err := e.box.Decrypt([]byte(rec.DesiredSpec))
	if err != nil {
		return nil, fmt.Errorf("engine: decrypt desired-spec %s: %w", rec.ID, err)
	}
	var specs []ServiceSpec
	if err := json.Unmarshal(plain, &specs); err != nil {
		return nil, fmt.Errorf("engine: decode desired-spec %s: %w", rec.ID, err)
	}
	return specs, nil
}

// now 是时钟出口（单测注入）。
func (e *Engine) now() time.Time { return e.clock.Now() }
