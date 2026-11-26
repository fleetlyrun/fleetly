// Package functions 是 Functions 应用模块（ADR-0077 一等实体；设计真源
// docs/design/2026-10-16-functions-api.md v3 §7；实施 ADR-0102 任务③B）：
// 收敛循环（functionLoop 单环，dataservices 同族形态）+ 聚合收口。行写动词
// （创建/换轨/容量/接入/删除落账）住 API 受理面（任务④）——本模块只消费
// 行事实收敛载体；行即期望态。
package functions

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"encoding/json"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Functions 是应用模块本体（复数普通名词 struct + 白盒测试，ADR-0075
// 决策 4）。
type Functions struct {
	// locks 是 per-function 互斥（P2-0 前置批，设计 §4.3 立法 2026-11-19 补
	// 落码：reconcile 的 Ensure 与 TeardownFunction 的 Remove 原先只共享
	// maintenanceMu 读锁——互不串行，环 mid-reconcile 可重建刚被收口拆除
	// 的载体（tombstone 后永久孤儿）。唤醒把 Ensure 频率放大一个量级后此
	// 竞态从理论变现实（Phase 2 设计 v2 §1.6a）。appLocks/task.locks 同款
	// 惰性建不退（E30 裁决）。
	locks sync.Map // functionID → *sync.Mutex

	runtime capability.Runtime
	repos   transact.Stores
	cipher  *material.Cipher
	log     *slog.Logger
	clock   state.Clock

	lockMaintenance     func() func()
	boundedStep         func(ctx context.Context) (context.Context, context.CancelFunc)
	ensure              contract.EnsureCache
	projectTeam         func(ctx context.Context, projectID string) (string, error)
	projectNetworkFacts func(ctx context.Context, projectID string) ([]string, map[string]bool)
	obs                 contract.Observations
	forgetObservation   func(functionID string)
	observationOf       func(functionID string) (capability.WorkloadEvent, bool)

	// intake 是构建摄取窄端口（P2-3，ADR-0102 收编条款：「铸 Build + inputs
	// 登记」经 contract.Intake 导出出口调用——模块间禁 import；nil = 构建
	// 源停用（手动测试装配），推进步对 pending 行诚实跳拍并告警）。
	intake contract.IntakeBuildFace
	// commit 是写事务端口（P2-3：兑现事务 SwapCurrent + 清位 + 事件一拍；
	// 组合根绑核 commitWrite）。
	commit contract.CommitWrite
	kick   contract.Kicker
	// registryEndpoint 是受管仓端点端口（P2-3 投影半边——digest 引用组合）。
	registryEndpoint func(ctx context.Context) (capability.RegistryEndpoint, error)
}

// Deps 是应用模块依赖与内核端口的注入面（与 contract.FunctionsPorts 逐
// 字段同形——组合根互转装配，漂移即编译红）。
type Deps struct {
	Runtime capability.Runtime
	Repos   transact.Stores
	Cipher  *material.Cipher
	Log     *slog.Logger
	Clock   state.Clock

	LockMaintenance     func() func()
	BoundedStep         func(ctx context.Context) (context.Context, context.CancelFunc)
	Ensure              contract.EnsureCache
	ProjectTeam         func(ctx context.Context, projectID string) (string, error)
	ProjectNetworkFacts func(ctx context.Context, projectID string) ([]string, map[string]bool)
	Observations        contract.Observations
	ForgetObservation   func(functionID string)
	ObservationOf       func(functionID string) (capability.WorkloadEvent, bool)
	Kick                contract.Kicker
	// Commit 是写事务端口（P2-3：兑现事务；组合根绑核 commitWrite）。
	Commit contract.CommitWrite
	// RegistryEndpoint 是受管仓端点端口（P2-3 投影半边）。
	RegistryEndpoint func(ctx context.Context) (capability.RegistryEndpoint, error)
	// IntakeBuildFace 是构建摄取窄端口（P2-3：inputs 登记导出出口；nil =
	// 构建源停用）。
	IntakeBuildFace contract.IntakeBuildFace
}

// New 构造应用模块（不启动——循环步由 engine 核驱动，ring "function"）。
func New(deps Deps) *Functions {
	return &Functions{
		runtime:             deps.Runtime,
		repos:               deps.Repos,
		cipher:              deps.Cipher,
		log:                 deps.Log,
		clock:               deps.Clock,
		lockMaintenance:     deps.LockMaintenance,
		boundedStep:         deps.BoundedStep,
		ensure:              deps.Ensure,
		projectTeam:         deps.ProjectTeam,
		projectNetworkFacts: deps.ProjectNetworkFacts,
		obs:                 deps.Observations,
		forgetObservation:   deps.ForgetObservation,
		observationOf:       deps.ObservationOf,
		intake:              deps.IntakeBuildFace,
		commit:              deps.Commit,
		kick:                deps.Kick,
		registryEndpoint:    deps.RegistryEndpoint,
	}
}

// stepFunction 是收敛循环单拍：pending 推进（构建就绪门——设计 v2 §3 机制
// 三件②）先于逐行 reconcile（新 revision 兑现后同拍即投影新载体）。
func (f *Functions) stepFunction(ctx context.Context) {
	if err := f.promotePending(ctx); err != nil {
		f.log.Error("function step: promote pending", "err", err)
	}
	rows, err := f.repos.Functions.List(ctx)
	if err != nil {
		f.log.Error("function step: list", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		f.reconcile(ctx, &rows[i])
	}
}

// promotePending 是构建就绪门的环内兑现步（P2-3，设计 v2 §3——无回调面）：
// pending 行 × Build 终态三分支——succeeded → 一事务 SwapCurrent + 清位 +
// revision_changed(cause=deploy)；failed/cancelled/expired → pending 保留
// + E_FUNCTION_NOT_READY 呈现（预留码在此兑现）；queued/building → D-4
// 重拾输入登记（intake 导出出口）后等构建推进。latest-wins：pending 被
// 新 deploy 覆写后旧行自然脱锚（Build 兑现时锚不等 = 放弃兑现）。
func (f *Functions) promotePending(ctx context.Context) error {
	rows, err := f.repos.Functions.List(ctx)
	if err != nil {
		return err
	}
	for i := range rows {
		if ctx.Err() != nil {
			return nil
		}
		row := &rows[i]
		if row.PendingRevisionID == "" {
			continue
		}
		// 悬空锚（保留窗竞态）= 清位跳拍（诚实：构建目标已不存在）。
		pending, perr := f.repos.FunctionRevisions.Get(ctx, row.PendingRevisionID)
		if perr != nil {
			f.log.Error("promote pending: current revision missing", "function", row.ID, "err", perr)
			if serr := f.repos.Functions.SetPending(ctx, row.ID, ""); serr != nil {
				return serr
			}
			continue
		}
		builds, berr := f.repos.Builds.ListByRevision(ctx, pending.ID)
		if berr != nil {
			return berr
		}
		var active *build.Build
		terminalFailed := false
		for j := range builds {
			b := builds[j]
			switch b.State {
			case build.StateSucceeded:
				// 兑现：锚已被他写（latest-wins）= 放弃（新 pending 接棒）。
				if row.PendingRevisionID != pending.ID {
					break
				}
				fnMu := f.lockFunction(row.ID)
				fnMu.Lock()
				err := f.commit(ctx, f.repos, contract.WriteFact{
					Write: func(ctx context.Context, st transact.Stores) error {
						if err := st.Functions.SwapCurrent(ctx, row.ID, pending.ID, pending.Digest); err != nil {
							return err
						}
						return st.Functions.SetPending(ctx, row.ID, "")
					},
					Facts: []func() writes.FactRow{f.revisionChangedFact(row, "deploy")},
				})
				fnMu.Unlock()
				if err != nil {
					return err
				}
				f.log.Info("function build landed", "function", row.ID, "revision", pending.ID)
				f.kick.Kick()
			case build.StateQueued, build.StateBuilding:
				active = &builds[j]
			default:
				terminalFailed = true // failed/cancelled/expired → pending 保留（E_FUNCTION_NOT_READY 呈现）
			}
		}
		if active != nil && f.intake != nil {
			rev, rerr := f.repos.FunctionRevisions.Get(ctx, active.RevisionID)
			if rerr == nil {
				if derr := f.intake.PrepareFunctionBuild(ctx, *active, *row, *rev); derr != nil {
					// D-4 重拾失败（upload 材料/凭证面）：告警下拍重试
					// ——构建循环的前置检会拦无登记行（buildPickable）。
					f.log.Error("promote pending: rebuild build input", "build", active.ID, "err", derr)
				}
			}
		}
		if terminalFailed {
			// 呈现面：pending 保留（诚实——构建目标未兑现，换轨不发生）；
			// CLI/Console 经 pending_revision_digest 见非空即 E_FUNCTION_NOT_READY
			// 语义（421 信封由 gRPC 信封面按 pending 锚渲染——不在此落错误行）。
			f.log.Warn("function build failed; pending revision stays anchored",
				"function", row.ID, "revision", pending.ID)
		}
	}
	return nil
}

// revisionChangedFact 铸 function.revision_changed 行事实（构建兑现来源
// ——payload 与 API 受理面同形：cause=deploy）。
func (f *Functions) revisionChangedFact(row *function.Function, cause string) func() writes.FactRow {
	return func() writes.FactRow {
		return writes.FactRow{
			Name: "function.revision_changed", Aggregate: "function", ID: row.ID,
			Payload: mustJSONPayload(map[string]string{"cause": cause}),
			Action:  "function." + cause, Resource: "function/" + row.ID,
			TeamID: f.teamOf(row.ProjectID),
		}
	}
}

// mustJSONPayload 铸事件载荷（标量 map——Marshal 不失败；失败即编程错误）。
func mustJSONPayload(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("functions: marshal fact payload: %v", err))
	}
	return b
}

// teamOf 解析事件的 Team 轴（ProjectTeam 端口；失败落空 = 平台级）。
func (f *Functions) teamOf(projectID string) string {
	if f.projectTeam == nil {
		return ""
	}
	team, err := f.projectTeam(context.Background(), projectID)
	if err != nil {
		return ""
	}
	return team
}

// reconcile 收敛单行（设计 v3 §7 单拍，reconcileDatabase 同族八段式的
// 函数裁剪：无 restore/settings 双车道/卷三段）：冻结 spec 读 → 维护锁 +
// 带界 → 投影 → 签名短路 → 材料解密 → 指纹 gen 口 → Ensure → 归属/期望/
// 状态推进。
// ParkedFunctionAliases 实现 contract.Functions（P2-1b 别名交接）：全平台
// parked（replicas=0 活跃）函数的 fn-<id> 别名集——唤醒组件 Addressing 的
// 注入源（fn 摘别名后由其接管 private 解析）。
func (f *Functions) ParkedFunctionAliases(ctx context.Context) ([]string, error) {
	rows, err := f.repos.Functions.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, 4)
	for i := range rows {
		if rows[i].Replicas == 0 {
			out = append(out, contract.FunctionDNSName(rows[i].ID))
		}
	}
	return out, nil
}

// ParkedFunctionPorts 实现 contract.Functions（P2-1b private 半边）：parked
// 函数声明口的并集——唤醒组件受管声明 ACTIVATOR_EXTRA_PORTS 的注入源。
// private 调用经 fn-<id> 别名命中唤醒组件任务，swarm VIP 按端口透传：
// 组件任务须在函数声明口同端口监听，唤醒窗的扣留面才可达。冻结体解析与
// functionEndpoint 同判据（恰一 http|h2c 口；tcp 不可路由跳过）。
func (f *Functions) ParkedFunctionPorts(ctx context.Context) ([]int32, error) {
	rows, err := f.repos.Functions.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]int32, 0, 4)
	seen := make(map[int32]bool, 4)
	for i := range rows {
		if rows[i].Replicas != 0 {
			continue
		}
		rev, err := f.repos.FunctionRevisions.Get(ctx, rows[i].CurrentRevisionID)
		if err != nil {
			// 悬空锚 = 终局窗竞态（reconcile 同款诚实跳过—— parked 行
			// 生命周期极短，漏一口的代价是 NXDOMAIN 快速失败语义）。
			f.log.Error("parked function ports: current revision missing", "function", rows[i].ID, "err", err)
			continue
		}
		port, ok := frozenPort(rev.Spec)
		if !ok || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	return out, nil
}

// frozenPort 从冻结体读声明口（恰一口；tcp 判不可路由）——解析判据与核
// functionEndpoint 同源（v1 受理位归一后不理论值）。
func frozenPort(specBody []byte) (int32, bool) {
	s := &specv1.FunctionSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(specBody, s); err != nil {
		return 0, false
	}
	ports := s.GetHandler().GetPorts()
	if len(ports) != 1 || ports[0].GetProtocol() == specv1.Protocol_PROTOCOL_TCP {
		return 0, false
	}
	return ports[0].GetPort(), ports[0].GetPort() > 0
}

// lockFunction 取函数级互斥（惰性建；收敛环/收口共享）。
func (f *Functions) lockFunction(functionID string) *sync.Mutex {
	mu, _ := f.locks.LoadOrStore(functionID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func (f *Functions) reconcile(ctx context.Context, row *function.Function) {
	// per-function 互斥（设计 §4.3；P2-0 落码）：与 TeardownFunction 全序
	// 串行——消灭「环 Ensure 重建刚拆载体」竞态窗。锁等待不占步预算。
	fnMu := f.lockFunction(row.ID)
	fnMu.Lock()
	defer fnMu.Unlock()

	// parked 行的观测缓存诚实化（P2-2 联动）：0 副本 = 载体不在场 =
	// stopped——0/0 服务无观测事件，缓存会停留 stale running，把决策环的
	// running 门骗过（唤醒收敛窗被 park 压回 0 的 wake↔park 振荡——e2e
	// 实证 2026-11-22）。显示层 parked 映射（replicas=0）不受影响。
	if row.Replicas == 0 && row.Status != "stopped" {
		if err := f.repos.Functions.AdvanceStatus(ctx, row.ID, "stopped"); err != nil {
			f.log.Error("function reconcile: park status", "function", row.ID, "err", err)
		}
		row.Status = "stopped"
	}

	if row.CurrentRevisionID == "" {
		// 创建即首部署的不变量下不可达（受理位换轨原子）；防御跳拍不
		// Ensure——载体保持现状（databases 模板下线同款诚实口径）。
		f.log.Error("function reconcile: no current revision", "function", row.ID)
		return
	}
	rev, err := f.repos.FunctionRevisions.Get(ctx, row.CurrentRevisionID)
	if err != nil {
		// 悬空锚 = 终局窗竞态（删行已清冻结体）——跳拍。
		f.log.Error("function reconcile: current revision missing", "function", row.ID, "err", err)
		return
	}
	spec := &specv1.FunctionSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(rev.Spec, spec); err != nil {
		f.log.Error("function reconcile: unmarshal frozen spec", "function", row.ID, "err", err)
		return
	}
	// 源码源的构建产物改写（P2-3 兑现的投影半边）：handler.FromBuild 形态
	// → 本 revision 的 succeeded Build digest 引用（App 侧 buildDigests 先
	// 例——Build.Repo 创建时定型 + LocalImageDigestRef 组合，地址是平台级
	// 配置不进冻结体）。投影单源保持 image 形假设（改写后的 spec 副本纯
	// image 形）；无 succeeded Build（构建进行中/失败）= 保持旧载体不投影
	// 新形态（诚实跳拍——兑现窗的载体过渡由旧 revision 载体继续服务）。
	if spec.GetHandler().GetFromBuild() != "" {
		img, berr := f.builtImageFor(ctx, rev)
		if berr != nil {
			f.log.Error("function reconcile: resolve built image", "function", row.ID, "revision", rev.ID, "err", berr)
			return
		}
		if img == "" {
			// 构建未成功（pending 窗或 failed）——跳拍保持现状。
			f.log.Info("function reconcile: no successful build yet; keeping previous carrier", "function", row.ID, "revision", rev.ID)
			return
		}
		// 投影单源双补：Source.Image（投影的源校验面）与 handler.ImageOrigin
		//（进程模板面）同一 digest 引用——改写后 spec 副本纯 image 形。
		spec.Source = &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: img}}}
		spec.Handler.ImageOrigin = &specv1.ProcessSpec_Image{Image: img}
		f.log.Info("function reconcile: built image resolved", "function", row.ID, "revision", rev.ID, "image", img)
	}
	// 维护互斥读半边（ADR-0046）+ 单步带界（staging docker API hang 教义）。
	unlockMaintenance := f.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := f.boundedStep(ctx)
	defer cancel()

	team, err := f.projectTeam(stepCtx, row.ProjectID)
	if err != nil {
		f.log.Error("function reconcile: resolve project", "function", row.ID, "err", err)
		return
	}
	networks, egress := f.projectNetworkFacts(stepCtx, row.ProjectID)
	w, ns, err := ProjectFunction(spec, row, team, networks, egress, rev.Digest)
	if err != nil {
		f.log.Error("function reconcile: project spec", "function", row.ID, "err", err)
		return
	}
	// 材料面：secret_refs 密文预读（不解密）——签名代理 + 下发段解密输入
	// 单源（databases 凭证同律：宁重下发不漏变更）。
	ciphertexts, err := f.secretCiphertexts(stepCtx, row, spec)
	if err != nil {
		f.log.Error("function reconcile: resolve secrets", "function", row.ID, "err", err)
		return
	}
	// 签名三段：投影集（含副本数——ADR-0064 纯伸缩走 scale-only）+ 寻址域
	// + 密文摘要。
	sig := contract.ManagedFingerprint([]capability.Workload{w}) + "\x00" + ns.String() + "\x00" +
		secretsFingerprint(ciphertexts)
	now := f.clock.Now()
	if memo, fresh := f.ensure.Fresh(row.ID, sig, now); fresh {
		f.advanceStatus(stepCtx, row, memo.Gen)
		return
	}
	materials, err := f.decryptMaterials(stepCtx, ciphertexts)
	if err != nil {
		f.log.Error("function reconcile: decrypt secrets", "function", row.ID, "err", err)
		return
	}
	ws := []capability.Workload{w}
	gen, err := f.repos.Functions.EnsureGeneration(stepCtx, row.ID, contract.ManagedFingerprint(ws))
	if err != nil {
		f.log.Error("function reconcile: generation", "function", row.ID, "err", err)
		return
	}
	report, err := f.runtime.Ensure(stepCtx, ns, ws, capability.Generation(gen), materials)
	if err != nil {
		f.log.Error("function reconcile: ensure", "function", row.ID, "generation", gen, "err", err)
		f.ensure.Forget(row.ID) // 失败清签名：下拍重试
		return
	}
	// 归属回写按回执 wire-gen（ADR-0064：内容等价 skip 保现存代次——
	// call-gen 登记会与载体错位发假 drift）。
	wireGen := report.WireGen(w, capability.Generation(gen))
	f.ensure.Remember(row.ID, contract.Memo{Sig: sig, Gen: uint64(wireGen), At: now})
	f.obs.RecordOwners(row.ID, uint64(wireGen), ws)
	// 合成路由期望集（P2-1a 第四刀补：resolveFunctionBackend 读
	// ensuredSpec——RecordOwners 不落 spec，本行是路由解析的唯一账本源）。
	f.obs.RecordProjected(row.ID, ws)
	// 看门狗期望：replicas>0 才登记——故意缩零不是事故（设计 §7；v1 恒
	// ≥1，防未来 P2 开 0 值域时的假告警面）。
	if row.Replicas > 0 {
		f.obs.RecordExpected(row.ID, uint64(wireGen))
	} else {
		f.obs.ForgetExpected(row.ID)
	}
	f.advanceStatus(stepCtx, row, uint64(wireGen))
}

// builtImageFor 解析本 revision 的构建产物镜像引用（from_build 投影半边
// ——Build.Repo 创建时定型 + 受管仓地址组合；digest 是冻结产物事实，
// LocalImageDigestRef 与 App 侧 buildDigests 同构）。registry 端点缺席 =
// 精确失败（诊断顺序：先平台缺件，再产物缺席）。
func (f *Functions) builtImageFor(ctx context.Context, rev *function.Revision) (string, error) {
	builds, err := f.repos.Builds.ListByRevision(ctx, rev.ID)
	if err != nil {
		return "", err
	}
	for i := range builds {
		b := builds[i]
		if b.State == build.StateSucceeded && b.Digest != "" {
			endpoint, err := f.registryEndpoint(ctx)
			if err != nil {
				return "", err
			}
			return contract.LocalImageDigestRef(endpoint.Addr, b.Repo, b.Digest), nil
		}
	}
	return "", nil
}

// secretCiphertexts 按名预读 Secret 行密文（不进 spec/日志）。
// secretCiphertexts 按名预读 Secret 行密文（不进 spec/日志）。
func (f *Functions) secretCiphertexts(ctx context.Context, row *function.Function, spec *specv1.FunctionSpec) (map[string][]byte, error) {
	refs := spec.GetHandler().GetSecretRefs()
	if len(refs) == 0 {
		return nil, nil
	}
	out := make(map[string][]byte, len(refs))
	for _, name := range refs {
		sec, err := f.repos.Secrets.GetByName(ctx, row.ProjectID, name)
		if err != nil {
			return nil, fmt.Errorf("lookup secret %q: %w", name, err)
		}
		out[name] = sec.Ciphertext
	}
	return out, nil
}

// decryptMaterials 把密文解为文件注入材料（ADR-0014：文件注入
// /run/secrets/<name>，值不进 env）。
func (f *Functions) decryptMaterials(ctx context.Context, ciphertexts map[string][]byte) (capability.Materials, error) {
	if len(ciphertexts) == 0 {
		return capability.Materials{}, nil
	}
	files := make(map[string][]byte, len(ciphertexts))
	for name, ct := range ciphertexts {
		plain, err := f.cipher.Open(ct)
		if err != nil {
			return capability.Materials{}, fmt.Errorf("decrypt secret %q: %w", name, err)
		}
		files[name] = plain
	}
	return capability.Materials{SecretFiles: files}, nil
}

// secretsFingerprint 是密文摘要（确定性：名排序拼接）。
func secretsFingerprint(ciphertexts map[string][]byte) string {
	if len(ciphertexts) == 0 {
		return "-"
	}
	names := make([]string, 0, len(ciphertexts))
	for name := range ciphertexts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+":"+fmt.Sprintf("%x", ciphertexts[name]))
	}
	return strings.Join(parts, "\x00")
}

// advanceStatus 推进观测缓存（wire-gen 匹配才采信——设计 §4.1 两层分立；
// 无观测/代次错位保持现值，观测缓存与 Ensure 无关）。
func (f *Functions) advanceStatus(ctx context.Context, row *function.Function, gen uint64) {
	ev, ok := f.observationOf(row.ID)
	if !ok || uint64(ev.Generation) != gen {
		return
	}
	status := statusFromEvent(ev)
	if status == "" || status == row.Status {
		return
	}
	if err := f.repos.Functions.AdvanceStatus(ctx, row.ID, status); err != nil {
		f.log.Error("function reconcile: advance status", "function", row.ID, "err", err)
		return
	}
	row.Status = status
}

// statusFromEvent 把载体观测折为 status 缓存值：State 词汇与 functions.status
// 缓存同族（pending/running/degraded/stopped）——pending 是缺省零值不回写，
// orphaned/completed/failed 是一次性/对账面词汇不进受管缓存（空串 = 保持
// 现值）。
func statusFromEvent(ev capability.WorkloadEvent) string {
	switch ev.State {
	case capability.WorkloadRunning, capability.WorkloadDegraded, capability.WorkloadStopped:
		return string(ev.State)
	default:
		return ""
	}
}
