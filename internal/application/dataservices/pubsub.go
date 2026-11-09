package dataservices

// pubsub 家族收敛步（B4b，ADR-0096 决策 1/2/4/5；design §4.3）：database 族
// 分支的同构复制——活跃行 → 投影（Workload：Image/Command/Probe/卷 pub 名
// 公式/Ports 4222/Addressing pub-<id>/项目全部活跃网 + EgressSubset）→
// 材料（凭证 Secret 解密回读 → 模板渲染 nats-server.conf）→ gen 口 →
// Ensure → 观测登记与状态推进；Teardown + 级联（项目删除逐 pubsub 收口）。
// 本族首批旋钮全 static（nats）：无 dynamic 重放门、Update 单车道（落行 +
// Kick）。凭证轮换 = 声明式（nats RotateSpec Argv 空——Secret 重写 + 载体
// 重下发，redis 同款）。
//
// 族状态面分持（三环并发调 Step 的并发安全约定）：ensurePub memo 表随
// 域（组合根铸造注入）；rotateMu 跨族共用（轮换是罕见运维动作，全域串行
// 是最小诚实防线——锁已在 database 族存在，复用不新增写面）。
//
// 卷实例标记门（B4c 实体中性化接入）：ensurePubSubVolumeOrigin 与
// CheckVolumeOriginForCreate(FamilyPubSub) 在 origin.go 与 database 族共享
// 实体中性内核（ADR-0097 决策 4——pubsub 首启在卷内落 family:pubsub 标记，
// 跨族/异名陈旧卷 fail-closed，红队 B-击三闭环）。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// IsPubSubCredentialSecret 报告 Secret 名是否落在 pubsub 凭证保留前缀
// （API PutSecret 受理守卫消费：用户覆写会破坏连接材料单真源）。
func IsPubSubCredentialSecret(name string) bool {
	return strings.HasPrefix(name, contract.PubCredentialSecretPrefix)
}

// PubSubConnMaterial 铸连接材料方言串（API 创建面消费的单源公式：模板
// 查询 + 铸名 + 模板 ConnMaterial → RenderConnMaterial——Secret 值 = 家族
// 方言串，nats URL 形 / kafka k=v 串，B6a 起双方言单源）。host = pub-<id>
// （只在项目网内可解析）。
func PubSubConnMaterial(engineName, version, pubsubID, password string) (string, error) {
	tpl, err := pubsubtemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return pubsubtemplate.RenderConnMaterial(tpl.ConnMaterial(contract.PubSubDNSName(pubsubID), password)), nil
}

// stepPubSub 是 pubsub 族收敛环的单次推进（单写者；逐行收敛，错误逐行记
// 日志不阻断）。经分族步 Step(ctx, FamilyPubSub) 调入。
func (d *DataServices) stepPubSub(ctx context.Context) {
	rows, err := d.repos.PubSubs.List(ctx)
	if err != nil {
		d.log.Error("pubsub step: list", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		d.reconcilePubSub(ctx, &rows[i])
		// 子资源尾段（B8，design §4.7）：Topic/Stream 行集对账——reconcile
		// 的 maintenance 读锁已释放，段内工具容器链自持锁。
		d.reconcilePubSubSubResources(ctx, &rows[i])
	}
}

// reconcilePubSub 收敛单行（database 族 reconcileDatabase 同构：投影 →
// 材料 → 签名短路 → 卷补建/钉住/adoption 守卫 → gen 口 → Ensure → 归属
// 登记与状态推进）。恢复挂起中的行：nats 逐流恢复是 stream 形态（目标在
// 服后注入）——不跳过 Ensure（preseed 形态在本族不存在）。
func (d *DataServices) reconcilePubSub(ctx context.Context, row *pubsub.PubSub) {
	// 重放门（B6a kafka dynamic 面）：dynamic 子集失配重放在 memo 短路与
	// maintenance 读锁**之前**执行（replayDynamicSettings 同序同因——锁序
	// utilityMu → maintenance，锁内调用即读锁递归死锁）。
	d.replayPubSubDynamic(ctx, row)
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := d.boundedStep(ctx)
	defer cancel()

	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		d.log.Error("pubsub reconcile: engine left the template registry", "pubsub", row.ID, "engine", row.Engine, "err", err)
		return
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		d.log.Error("pubsub reconcile: resolve project", "pubsub", row.ID, "err", err)
		return
	}
	staticOnly, _ := pubsubtemplate.SplitSettings(tpl.Knobs(), row.Settings)
	networks, egress := d.projectNetworkFacts(stepCtx, row.ProjectID)
	volRow, err := d.ensurePubSubVolume(stepCtx, row)
	if err != nil {
		d.log.Error("pubsub reconcile: ensure volume", "pubsub", row.ID, "err", err)
		return
	}
	w, ns, err := ProjectPubSub(row, team, volRow.ID, networks, egress, tpl, pubsubResources(row), staticOnly)
	if err != nil {
		d.log.Error("pubsub reconcile: project", "pubsub", row.ID, "err", err)
		return
	}
	sec, err := d.repos.Secrets.GetByName(stepCtx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		d.log.Error("pubsub reconcile: resolve credentials", "pubsub", row.ID,
			"err", fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err))
		return
	}
	// 签名四段（database 族同款复合代理：投影集 + 寻址域 + 凭证密文摘要 +
	// static 子集摘要）。
	sig := contract.ManagedFingerprint([]capability.Workload{w}) + "\x00" + ns.String() + "\x00" +
		credentialFingerprint(sec.Ciphertext) + "\x00" + settingsDigest(staticOnly)
	now := d.clock.Now()
	if memo, fresh := d.ensureFresh(d.ensurePub, row.ID, sig, now); fresh {
		d.advancePubSubStatus(stepCtx, row, memo.Gen)
		return
	}
	materials, err := d.pubsubMaterials(row, tpl, sec, staticOnly)
	if err != nil {
		d.log.Error("pubsub reconcile: resolve credentials", "pubsub", row.ID, "err", err)
		return
	}
	ws := []capability.Workload{w}
	if err := d.pinVolumes(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("pubsub reconcile: pin volumes", "pubsub", row.ID, "err", err)
		return
	}
	if err := d.applyVolumePinning(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("pubsub reconcile: volume pinning merge", "pubsub", row.ID, "err", err)
		return
	}
	if err := d.guardVolumeCarriers(stepCtx, ns, ws, row.ProjectID); err != nil {
		d.log.Error("pubsub reconcile: volume carrier adoption guard", "pubsub", row.ID, "err", err)
		return
	}
	// pubsub 卷实例标记门（B4c，ADR-0097 决策 4 实体中性化）：Ensure 前读/铸
	// fleetly-instance.json——异源标记（跨族或异名陈旧卷）= 拒绝确保
	//（fail-closed）；同源/缺席即过。在 gen 口之前——拒绝不推进代次。
	if err := d.ensurePubSubVolumeOrigin(stepCtx, row, tpl, volRow); err != nil {
		d.log.Error("pubsub reconcile: volume origin gate", "pubsub", row.ID, "err", err)
		return
	}
	gen, err := d.repos.PubSubs.EnsureGeneration(stepCtx, row.ID, contract.ManagedFingerprint(ws))
	if err != nil {
		d.log.Error("pubsub reconcile: generation", "pubsub", row.ID, "err", err)
		return
	}
	report, err := d.runtime.Ensure(stepCtx, ns, ws, capability.Generation(gen), materials)
	if err != nil {
		d.log.Error("pubsub reconcile: ensure", "pubsub", row.ID, "generation", gen, "err", err)
		d.ensureForget(d.ensurePub, row.ID)
		return
	}
	wireGen := report.WireGen(w, capability.Generation(gen))
	d.ensureRemember(d.ensurePub, row.ID, contract.Memo{Sig: sig, Gen: uint64(wireGen), At: now})
	d.recordPubSubOwners(row.ID, uint64(wireGen), []capability.Workload{w})
	d.recordPubSubExpected(row.ID, uint64(wireGen))
	d.advancePubSubStatus(stepCtx, row, uint64(wireGen))
}

// pubsubResources 把行资源面折为载体上限（nil = 无限制）。
func pubsubResources(row *pubsub.PubSub) *capability.Resources {
	if row.CPUMillis == nil && row.MemoryMB == nil {
		return nil
	}
	res := &capability.Resources{}
	if row.CPUMillis != nil {
		res.CPUMillis = int64(*row.CPUMillis)
	}
	if row.MemoryMB != nil {
		res.MemoryMB = int64(*row.MemoryMB)
	}
	return res
}

// advancePubSubStatus 从观测缓存推进行状态（gen 匹配才采信；观测缓存键
// = 投影 Workload ID = 行 ID）。
func (d *DataServices) advancePubSubStatus(ctx context.Context, row *pubsub.PubSub, gen uint64) {
	ev, seen := d.observationOf(row.ID)
	if status, ok := dbStatusFromObservation(ev, gen); ok && seen && status != row.Status {
		if err := d.repos.PubSubs.SetStatus(ctx, row.ID, status); err != nil {
			d.log.Error("pubsub reconcile: set status", "pubsub", row.ID, "err", err)
		}
	}
}

// pubsubMaterials 装配 Workload 的凭证材料：解密凭证 Secret → 连接材料
// 方言串 → 密码回读 → 模板渲染（nats-server.conf；redpanda.yaml 含
// rpk.memory——内存上限随行值进 Materials 面，B8-fix）。
func (d *DataServices) pubsubMaterials(row *pubsub.PubSub, tpl pubsubtemplate.Template, sec *secret.Secret, staticOnly map[string]string) (capability.Materials, error) {
	if d.cipher == nil {
		return capability.Materials{}, fmt.Errorf("pubsub credentials require the master key (data root keys/ missing)")
	}
	plain, err := d.cipher.Open(sec.Ciphertext)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := pubsubtemplate.PasswordFromURL(string(plain))
	if err != nil {
		return capability.Materials{}, fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	var memoryMB int64
	if row.MemoryMB != nil {
		memoryMB = int64(*row.MemoryMB)
	}
	files, err := tpl.Materials(password, memoryMB, staticOnly)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("render materials for engine %q: %w", row.Engine, err)
	}
	return capability.Materials{SecretFiles: files}, nil
}

// ProjectPubSub 把行投影为用户域受管 Workload（database 族 ProjectDatabase
// 同构：NamespaceRef.PubSub 轴；镜像/端口/卷目标来自模板；Workload ID =
// 行 ID；Addressing = pub-<id>；挂全部活跃项目网 + EgressSubset）。host
// 与内存上限随 Workload 渲染面下发（B6a：kafka advertised.listeners 需要
// 网内名、heap 联动需要资源面——nats 忽略）。
func ProjectPubSub(row *pubsub.PubSub, team, volumeID string, networks []string, egress map[string]bool, tpl pubsubtemplate.Template, limits *capability.Resources, staticSettings map[string]string) (capability.Workload, capability.NamespaceRef, error) {
	if row.ID == "" || row.ProjectID == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("pubsub row has no identity")
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	var memoryMB int64
	if limits != nil {
		memoryMB = limits.MemoryMB
	}
	env, command, err := tpl.Workload(contract.PubSubDNSName(row.ID), memoryMB, staticSettings)
	if err != nil {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("render workload for engine %q: %w", row.Engine, err)
	}
	w := capability.Workload{
		ID:             row.ID,
		Process:        tpl.Engine(),
		Image:          tpl.Image(),
		Command:        command,
		Env:            env,
		Replicas:       1,
		Networks:       networks,
		EgressNetworks: contract.EgressSubset(networks, egress),
		// JetStream 干净关停（文件存储 fsync 收口）：与数据库族同口径 60s。
		StopGrace: 60 * time.Second,
		Ports: []capability.WorkloadPort{
			{Port: tpl.Meta().Port, Protocol: capability.ProtocolTCP},
		},
		Healthcheck: &capability.Healthcheck{
			Exec:        tpl.Probe(),
			Interval:    dbProbeConnectTimeout,
			Timeout:     5 * time.Second,
			StartPeriod: 60 * time.Second,
			Retries:     3,
		},
		Volumes: []capability.VolumeMount{
			{VolumeID: volumeID, Target: tpl.DataTarget()},
		},
		Addressing: []capability.Address{{Name: contract.PubSubDNSName(row.ID)}},
		Resources:  limits,
	}
	return w, ns, nil
}

// TeardownPubSub 收口拆除（Runtime.Remove 幂等拆域内全部载体 + 清归属/
// 期望/观测缓存；tombstone 与事件由 API 受理位在后续事务落——先变更后
// 留痕）。单实例删除与项目删除级联共用本口。
func (d *DataServices) TeardownPubSub(ctx context.Context, id string) error {
	row, err := d.repos.PubSubs.Get(ctx, id)
	if err != nil {
		return err
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	rctx, rcancel := d.boundedStep(ctx)
	removeErr := d.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("pubsub teardown: %w", removeErr)
	}
	d.forgetObservation(row.ID)
	d.forgetPubSubExpected(row.ID)
	d.ensureForget(d.ensurePub, row.ID)
	return nil
}

// RotatePubSubPassword 轮换凭证（ADR-0096 决策 4：nats 声明式——RotateSpec
// Argv 空，Secret 重写 + 收敛环重下发即生效；kafka 数据面——B6a live 定案
// Argv 非空：SCRAM user alter（以 current 凭证执行）+ Secret 重写，PG 同序
// ——材料面随收敛环重下发对齐）。rotateMu 跨族共用（全域单飞）。
func (d *DataServices) RotatePubSubPassword(ctx context.Context, pubsubID string) error {
	d.rotateMu.Lock()
	defer d.rotateMu.Unlock()

	row, err := d.repos.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return err
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return fmt.Errorf("engine %q left the template registry: %w", row.Engine, err)
	}
	current, err := d.pubsubPassword(ctx, row)
	if err != nil {
		return err
	}
	next := pubsubtemplate.MintPassword()
	spec, err := tpl.RotatePassword(contract.PubSubDNSName(row.ID), current, next)
	if err != nil {
		return err
	}
	if len(spec.Argv) > 0 {
		req := capability.UtilityRequest{
			ID:          "rotate-" + row.ID,
			Namespace:   capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name},
			Image:       tpl.Image(),
			User:        tpl.ToolUser(), // B8-fix：kafka/redpanda 声明 root——0600 材料（--command-config/profile）可读
			Argv:        spec.Argv,
			Env:         spec.Env,
			Networks:    d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
			SecretFiles: spec.SecretFiles,
		}
		if err := d.runUtilityContainer(ctx, req); err != nil {
			return fmt.Errorf("%w: %s", ErrRotateRejected, err)
		}
	}
	newURL := pubsubtemplate.RenderConnMaterial(tpl.ConnMaterial(contract.PubSubDNSName(row.ID), next))
	sealed, err := d.cipher.Produce([]byte(newURL))
	if err != nil {
		return fmt.Errorf("seal rotated credential: %w", err)
	}
	sec := &secret.Secret{
		ProjectID: row.ProjectID, Name: row.CredentialsRef,
		Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
	}
	if err := d.repos.Secrets.Upsert(ctx, sec); err != nil {
		return fmt.Errorf("rewrite credential secret %q: %w", row.CredentialsRef, err)
	}
	d.kickPubSubs()
	return nil
}

// PubSubUpdateRequest 是 pubsub Update 的域输入（UpdateRequest 同形；Admit
// 钩子按 pubsub 行签名——锁内行 = 执行真源）。
type PubSubUpdateRequest struct {
	Settings  map[string]string
	Resources *ResourceUpdate
	Admit     func(row *pubsub.PubSub) (redeem func(), err error)
}

// UpdatePubSub 执行期望态变更（ADR-0095 决策 5 双车道——B6a kafka dynamic
// 面接入后本族双车道齐形，UpdateDatabase 同构）：utilityMu per-entity 全程
// 持锁（Admit→apply→memo→行→烧票原子化）→ dynamic 子集同步热应用
// （kafka-configs.sh——未变重放 = 摘要命中即跳过；移除键复位缺省）→
// static 子集 + 资源面落行 + Kick。nats 全 static：dynamic 子集恒空，
// 行为与 B4b 单车道逐位一致。
func (d *DataServices) UpdatePubSub(ctx context.Context, pubsubID string, req PubSubUpdateRequest) (UpdateResult, error) {
	unlock := d.utilityMu.acquire(pubsubID)
	defer unlock()

	row, err := d.repos.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return UpdateResult{}, err
	}
	var redeem func()
	if req.Admit != nil {
		if redeem, err = req.Admit(row); err != nil {
			return UpdateResult{}, err
		}
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return UpdateResult{}, fmt.Errorf("engine %q left the template registry: %w", row.Engine, err)
	}
	nextSettings := row.Settings
	if req.Settings != nil {
		nextSettings = req.Settings
	}
	nextCPU, nextMemory := row.CPUMillis, row.MemoryMB
	if req.Resources != nil {
		nextCPU, nextMemory = req.Resources.CPUMillis, req.Resources.MemoryMB
	}
	_, oldDynamic := pubsubtemplate.SplitSettings(tpl.Knobs(), row.Settings)
	_, dynamicOnly := pubsubtemplate.SplitSettings(tpl.Knobs(), nextSettings)
	resets, err := dynamicResets(tpl.Knobs(), oldDynamic, dynamicOnly)
	if err != nil {
		return UpdateResult{}, err
	}
	// dynamic 车道（UpdateDatabase 同款协议：未变重放 = 摘要命中跳过、
	// 移除键复位、失败强制 Forget——半应用黑洞修）。
	res := UpdateResult{}
	wantDigest := settingsDigest(dynamicOnly)
	haveDigest, have := d.appliedDigestOf(d.applied, row.ID)
	switch {
	case len(resets) > 0 || (len(dynamicOnly) > 0 && (!have || haveDigest != wantDigest)):
		applySet := make(map[string]string, len(dynamicOnly)+len(resets))
		for k, v := range dynamicOnly {
			applySet[k] = v
		}
		for k, v := range resets {
			applySet[k] = v
		}
		if err := d.applyPubSubDynamicLocked(ctx, row, tpl, applySet, wantDigest); err != nil {
			return UpdateResult{}, err
		}
		res.DynamicApplied = sortedKeys(applySet)
	case len(dynamicOnly) == 0 && have:
		d.appliedForget(d.applied, row.ID)
	}
	if err := d.repos.PubSubs.UpdateDesired(ctx, row.ID, nextSettings, nextCPU, nextMemory); err != nil {
		return UpdateResult{}, err
	}
	res.StaticRolled = changedPubSubStaticKeys(tpl, row.Settings, nextSettings)
	if redeem != nil {
		redeem()
	}
	d.kickPubSubs()
	return res, nil
}

// applyPubSubDynamicLocked 执行一次 pubsub 族 dynamic 热应用（utilityMu 已
// 持有——applyDynamicLocked 同构）：pubsub 凭证解密 → 模板渲染
// （ApplyDynamicSettings——kafka kafka-configs.sh；nats 不可达：ValidateSettings
// 已把白名单收敛、空集被调用方跳过）→ 工具容器执行（PubSub 命名空间轴）→
// 成功记 memo / 失败强制 Forget（半应用黑洞修，ADR-0095 决策 6）。
func (d *DataServices) applyPubSubDynamicLocked(ctx context.Context, row *pubsub.PubSub, tpl pubsubtemplate.Template, applySet map[string]string, wantDigest string) error {
	password, err := d.pubsubPassword(ctx, row)
	if err != nil {
		return err
	}
	spec, err := tpl.ApplyDynamicSettings(contract.PubSubDNSName(row.ID), password, applySet)
	if err != nil {
		return err
	}
	req := capability.UtilityRequest{
		ID:          "pubsettings-" + row.ID,
		Namespace:   capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name},
		Image:       tpl.Image(),
		User:        tpl.ToolUser(), // B8-fix：kafka/redpanda 声明 root——热应用工具面可读 0600 材料
		Argv:        spec.Argv,
		Env:         spec.Env,
		Networks:    d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
		SecretFiles: spec.SecretFiles,
	}
	if err := d.runUtilityContainer(ctx, req); err != nil {
		d.appliedForget(d.applied, row.ID)
		return fmt.Errorf("%w: %s", ErrSettingsApplyRejected, err)
	}
	if wantDigest == "" {
		d.appliedForget(d.applied, row.ID)
	} else {
		d.appliedRemember(d.applied, row.ID, wantDigest)
	}
	return nil
}

// replayPubSubDynamic 是 pubsub 族环侧重放门（replayDynamicSettings 同构，
// B6a kafka dynamic 面）：失配触发幂等重放；仅 running 且非恢复挂起；失败
// 退避；重放检查在 memo 短路判定之前（reconcilePubSub 取 maintenance 读锁
// 之前——锁序同 Update：utilityMu → maintenance）。锁内重读行 = 期望态
// 单真源。
func (d *DataServices) replayPubSubDynamic(ctx context.Context, row *pubsub.PubSub) {
	if row.Status != pubsub.StatusRunning || row.RestoreFromBackup != "" {
		return
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return // 模板离册的存量行：reconcile 主路径同判记日志
	}
	_, dynamicOnly := pubsubtemplate.SplitSettings(tpl.Knobs(), row.Settings)
	if len(dynamicOnly) == 0 {
		return
	}
	if have, ok := d.appliedDigestOf(d.applied, row.ID); ok && have == settingsDigest(dynamicOnly) {
		return
	}
	if d.replayInBackoff(row.ID) {
		return
	}
	unlock := d.utilityMu.acquire(row.ID)
	defer unlock()
	fresh, err := d.repos.PubSubs.Get(ctx, row.ID)
	if err != nil || fresh.Status != pubsub.StatusRunning || fresh.RestoreFromBackup != "" {
		return
	}
	_, dynamicOnly = pubsubtemplate.SplitSettings(tpl.Knobs(), fresh.Settings)
	wantDigest := settingsDigest(dynamicOnly)
	if have, ok := d.appliedDigestOf(d.applied, fresh.ID); len(dynamicOnly) == 0 || (ok && have == wantDigest) {
		return
	}
	if err := d.applyPubSubDynamicLocked(ctx, fresh, tpl, dynamicOnly, wantDigest); err != nil {
		d.markReplayFailure(fresh.ID)
		d.log.Error("pubsub replay: dynamic settings", "pubsub", fresh.ID, "err", err)
		return
	}
	d.clearReplayFailure(fresh.ID)
	d.log.Info("pubsub replay: dynamic settings converged", "pubsub", fresh.ID, "keys", len(dynamicOnly))
}

// changedPubSubStaticKeys 报告旧→新期望态间变更的 static 键集（排序稳定）。
func changedPubSubStaticKeys(tpl pubsubtemplate.Template, oldSettings, newSettings map[string]string) []string {
	oldStatic, _ := pubsubtemplate.SplitSettings(tpl.Knobs(), oldSettings)
	newStatic, _ := pubsubtemplate.SplitSettings(tpl.Knobs(), newSettings)
	changed := map[string]string{}
	for k, v := range newStatic {
		if ov, ok := oldStatic[k]; !ok || ov != v {
			changed[k] = v
		}
	}
	for k := range oldStatic {
		if _, ok := newStatic[k]; !ok {
			changed[k] = ""
		}
	}
	return sortedKeys(changed)
}

// InjectPubSubEnv 按 spec 的 process pubsub_refs 把 FLEETLY_PUB_* 合并进
// Workload Env（ADR-0096 决策 5：NAME=引用名 UPPER_SNAKE；nats 出 URL——
// _URL/_HOST/_PORT/_USER/_PASSWORD；kafka 出 bootstrap——_BOOTSTRAP/
// _MECHANISM/_HOST/_PORT/_USER/_PASSWORD，design §4.4；明文 env 承载凭证
// = ADR-0014 附录 A.2 追认范围显式扩至本族 refs）。缺实例/引擎未知/凭证
// 解不开都精确失败。
func (d *DataServices) InjectPubSubEnv(ctx context.Context, spec *specv1.AppSpec, ws []capability.Workload, projectID string) error {
	byName := make(map[string]*specv1.ProcessSpec, len(spec.GetProcesses()))
	for _, p := range spec.GetProcesses() {
		byName[p.GetName()] = p
	}
	for i := range ws {
		w := &ws[i]
		if err := d.ApplyPubSubEnv(ctx, w, byName[w.Process], projectID); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPubSubEnv 对单 Workload 合并其进程声明的 FLEETLY_PUB_* env（方言
// 分派 = 连接材料结构面：Bootstrap 在场即 kafka 形——BOOTSTRAP+MECHANISM，
// 否则 nats 形——URL；HOST/PORT/USER/PASSWORD 双方言共用）。
func (d *DataServices) ApplyPubSubEnv(ctx context.Context, w *capability.Workload, p *specv1.ProcessSpec, projectID string) error {
	if p == nil || len(p.GetPubsubRefs()) == 0 {
		return nil
	}
	for _, ref := range p.GetPubsubRefs() {
		row, err := d.repos.PubSubs.GetByName(ctx, projectID, ref)
		if err != nil {
			return fmt.Errorf("pubsub %q (pubsub_refs): not found — create the managed pubsub first (%w)", ref, err)
		}
		password, err := d.pubsubPassword(ctx, row)
		if err != nil {
			return fmt.Errorf("pubsub %q (pubsub_refs): %w", ref, err)
		}
		tpl, err := pubsubtemplate.For(row.Engine, row.Version)
		if err != nil {
			return fmt.Errorf("pubsub %q (pubsub_refs): %w", ref, err)
		}
		host := contract.PubSubDNSName(row.ID)
		mat := tpl.ConnMaterial(host, password)
		tok := dbEnvToken(ref)
		if w.Env == nil {
			w.Env = map[string]string{}
		}
		if mat.Bootstrap != "" {
			// kafka 方言：bootstrap host:port + SASL 机制（客户端零改动切
			// 换的方言轴——B6b redpanda 复用同面）。
			w.Env["FLEETLY_PUB_"+tok+"_BOOTSTRAP"] = mat.Bootstrap
			w.Env["FLEETLY_PUB_"+tok+"_MECHANISM"] = mat.Mechanism
		} else {
			w.Env["FLEETLY_PUB_"+tok+"_URL"] = mat.URL
		}
		w.Env["FLEETLY_PUB_"+tok+"_HOST"] = host
		w.Env["FLEETLY_PUB_"+tok+"_PORT"] = strconv.Itoa(int(tpl.Meta().Port))
		w.Env["FLEETLY_PUB_"+tok+"_USER"] = mat.User
		w.Env["FLEETLY_PUB_"+tok+"_PASSWORD"] = password
	}
	return nil
}
