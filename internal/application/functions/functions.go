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

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
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
	ensureFresh         contract.EnsureFresh
	ensureRemember      contract.EnsureRemember
	ensureForget        contract.EnsureForget
	projectTeam         func(ctx context.Context, projectID string) (string, error)
	projectNetworkFacts func(ctx context.Context, projectID string) ([]string, map[string]bool)
	recordOwners        func(functionID string, gen uint64, ws []capability.Workload)
	recordSpecs         func(functionID string, ws []capability.Workload)
	recordExpected      func(functionID string, gen uint64)
	forgetExpected      func(functionID string)
	forgetObservation   func(functionID string)
	observationOf       func(functionID string) (capability.WorkloadEvent, bool)
	ensure              map[string]contract.Memo
	// activatorMemo 是唤醒组件投影环的 ensure 缓存槽（组合根铸造注入——
	// ensure cache 表本体随域 D3 纪律）。
	activatorMemo map[string]contract.Memo
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
	EnsureFresh         contract.EnsureFresh
	EnsureRemember      contract.EnsureRemember
	EnsureForget        contract.EnsureForget
	ProjectTeam         func(ctx context.Context, projectID string) (string, error)
	ProjectNetworkFacts func(ctx context.Context, projectID string) ([]string, map[string]bool)
	RecordOwners        func(functionID string, gen uint64, ws []capability.Workload)
	RecordSpecs         func(functionID string, ws []capability.Workload)
	RecordExpected      func(functionID string, gen uint64)
	ForgetExpected      func(functionID string)
	ForgetObservation   func(functionID string)
	ObservationOf       func(functionID string) (capability.WorkloadEvent, bool)
	KickFunctions       func()
	EnsureMemo          map[string]contract.Memo
	EnsureActivatorMemo map[string]contract.Memo
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
		ensureFresh:         deps.EnsureFresh,
		ensureRemember:      deps.EnsureRemember,
		ensureForget:        deps.EnsureForget,
		projectTeam:         deps.ProjectTeam,
		projectNetworkFacts: deps.ProjectNetworkFacts,
		recordOwners:        deps.RecordOwners,
		recordSpecs:         deps.RecordSpecs,
		recordExpected:      deps.RecordExpected,
		forgetExpected:      deps.ForgetExpected,
		forgetObservation:   deps.ForgetObservation,
		observationOf:       deps.ObservationOf,
		ensure:              deps.EnsureMemo,
		activatorMemo:       deps.EnsureActivatorMemo,
	}
}

// stepFunction 是收敛循环单拍：列活跃行逐行 reconcile（ctx 取消即收拍）。
func (f *Functions) stepFunction(ctx context.Context) {
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

// reconcile 收敛单行（设计 v3 §7 单拍，reconcileDatabase 同族八段式的
// 函数裁剪：无 restore/settings 双车道/卷三段）：冻结 spec 读 → 维护锁 +
// 带界 → 投影 → 签名短路 → 材料解密 → 指纹 gen 口 → Ensure → 归属/期望/
// 状态推进。
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
	if memo, fresh := f.ensureFresh(f.ensure, row.ID, sig, now); fresh {
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
		f.ensureForget(f.ensure, row.ID) // 失败清签名：下拍重试
		return
	}
	// 归属回写按回执 wire-gen（ADR-0064：内容等价 skip 保现存代次——
	// call-gen 登记会与载体错位发假 drift）。
	wireGen := report.WireGen(w, capability.Generation(gen))
	f.ensureRemember(f.ensure, row.ID, contract.Memo{Sig: sig, Gen: uint64(wireGen), At: now})
	f.recordOwners(row.ID, uint64(wireGen), ws)
	f.recordSpecs(row.ID, ws) // 合成路由期望集（§6 后端解析）
	// 看门狗期望：replicas>0 才登记——故意缩零不是事故（设计 §7；v1 恒
	// ≥1，防未来 P2 开 0 值域时的假告警面）。
	if row.Replicas > 0 {
		f.recordExpected(row.ID, uint64(wireGen))
	} else {
		f.forgetExpected(row.ID)
	}
	f.advanceStatus(stepCtx, row, uint64(wireGen))
}

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
