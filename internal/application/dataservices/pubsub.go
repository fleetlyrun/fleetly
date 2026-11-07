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
	"net/url"
	"strconv"
	"strings"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// IsPubSubCredentialSecret 报告 Secret 名是否落在 pubsub 凭证保留前缀
// （API PutSecret 受理守卫消费：用户覆写会破坏连接材料单真源）。
func IsPubSubCredentialSecret(name string) bool {
	return strings.HasPrefix(name, engineports.PubCredentialSecretPrefix)
}

// PubSubConnectionURL 铸连接材料方言串（API 创建面消费的单源公式：模板
// 查询 + 铸名 + 模板 ConnURL）。host = pub-<id>（只在项目网内可解析）。
func PubSubConnectionURL(engineName, version, pubsubID, password string) (string, error) {
	tpl, err := pubtemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return tpl.ConnURL(engineports.PubSubDNSName(pubsubID), password), nil
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
	}
}

// reconcilePubSub 收敛单行（database 族 reconcileDatabase 同构：投影 →
// 材料 → 签名短路 → 卷补建/钉住/adoption 守卫 → gen 口 → Ensure → 归属
// 登记与状态推进）。恢复挂起中的行：nats 逐流恢复是 stream 形态（目标在
// 服后注入）——不跳过 Ensure（preseed 形态在本族不存在）。
func (d *DataServices) reconcilePubSub(ctx context.Context, row *pubsub.PubSub) {
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := d.boundedStep(ctx)
	defer cancel()

	tpl, err := pubtemplate.For(row.Engine, row.Version)
	if err != nil {
		d.log.Error("pubsub reconcile: engine left the template registry", "pubsub", row.ID, "engine", row.Engine, "err", err)
		return
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		d.log.Error("pubsub reconcile: resolve project", "pubsub", row.ID, "err", err)
		return
	}
	staticOnly, _ := pubtemplate.SplitSettings(tpl.Knobs(), row.Settings)
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
	sig := engineports.ManagedFingerprint([]capability.Workload{w}) + "\x00" + ns.String() + "\x00" +
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
	gen, err := d.repos.PubSubs.EnsureGeneration(stepCtx, row.ID, engineports.ManagedFingerprint(ws))
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
	d.ensureRemember(d.ensurePub, row.ID, engineports.Memo{Sig: sig, Gen: uint64(wireGen), At: now})
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
// 方言串 → 密码回读 → 模板渲染（nats-server.conf）。
func (d *DataServices) pubsubMaterials(row *pubsub.PubSub, tpl pubtemplate.Template, sec *secret.Secret, staticOnly map[string]string) (capability.Materials, error) {
	if d.cipher == nil {
		return capability.Materials{}, fmt.Errorf("pubsub credentials require the master key (data root keys/ missing)")
	}
	plain, err := d.cipher.Open(sec.Ciphertext)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := pubtemplate.PasswordFromURL(string(plain))
	if err != nil {
		return capability.Materials{}, fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	files, err := tpl.Materials(password, staticOnly)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("render materials for engine %q: %w", row.Engine, err)
	}
	return capability.Materials{SecretFiles: files}, nil
}

// ProjectPubSub 把行投影为用户域受管 Workload（database 族 ProjectDatabase
// 同构：NamespaceRef.PubSub 轴；镜像/端口/卷目标来自模板；Workload ID =
// 行 ID；Addressing = pub-<id>；挂全部活跃项目网 + EgressSubset）。
func ProjectPubSub(row *pubsub.PubSub, team, volumeID string, networks []string, egress map[string]bool, tpl pubtemplate.Template, limits *capability.Resources, staticSettings map[string]string) (capability.Workload, capability.NamespaceRef, error) {
	if row.ID == "" || row.ProjectID == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("pubsub row has no identity")
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	env, command := tpl.Workload(staticSettings)
	w := capability.Workload{
		ID:             row.ID,
		Process:        tpl.Engine(),
		Image:          tpl.Image(),
		Command:        command,
		Env:            env,
		Replicas:       1,
		Networks:       networks,
		EgressNetworks: engineports.EgressSubset(networks, egress),
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
		Addressing: []capability.Address{{Name: engineports.PubSubDNSName(row.ID)}},
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
// Argv 空，Secret 重写 + 收敛环重下发即生效；Argv 非空形态〔未来引擎〕
// 走工具容器数据面改密同 database 族）。rotateMu 跨族共用（全域单飞）。
func (d *DataServices) RotatePubSubPassword(ctx context.Context, pubsubID string) error {
	d.rotateMu.Lock()
	defer d.rotateMu.Unlock()

	row, err := d.repos.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return err
	}
	tpl, err := pubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return fmt.Errorf("engine %q left the template registry: %w", row.Engine, err)
	}
	current, err := d.pubsubPassword(ctx, row)
	if err != nil {
		return err
	}
	next := pubtemplate.MintPassword()
	spec, err := tpl.RotatePassword(engineports.PubSubDNSName(row.ID), current, next)
	if err != nil {
		return err
	}
	if len(spec.Argv) > 0 {
		req := capability.UtilityRequest{
			ID:          "rotate-" + row.ID,
			Namespace:   capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name},
			Image:       tpl.Image(),
			Argv:        spec.Argv,
			Env:         spec.Env,
			Networks:    d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
			SecretFiles: spec.SecretFiles,
		}
		if err := d.runUtilityContainer(ctx, req); err != nil {
			return fmt.Errorf("%w: %s", ErrRotateRejected, err)
		}
	}
	newURL := tpl.ConnURL(engineports.PubSubDNSName(row.ID), next)
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

// UpdatePubSub 执行期望态变更（ADR-0095 决策 5 双车道在本族的单车道形态
// ——首批旋钮全 static：落行 + Kick，收敛环受监督滚动；dynamic 子集恒空，
// 无热应用面）。utilityMu per-entity 全程持锁（Admit→行→烧票原子化）。
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
	tpl, err := pubtemplate.For(row.Engine, row.Version)
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
	// dynamic 车道防御：本族无 dynamic 旋钮（ValidateSettings 已把白名单
	// 收敛到全 static 集）；若未来引擎带 dynamic 面须在此接热应用——哨兵拒
	// 而非静默落行（喂入律：dynamic 键不得混进滚动材料）。
	_, dynamicOnly := pubtemplate.SplitSettings(tpl.Knobs(), nextSettings)
	if len(dynamicOnly) > 0 {
		return UpdateResult{}, fmt.Errorf("%w: engine %q declares dynamic knobs but the pubsub family carries no synchronous dynamic-settings lane yet", ErrSettingsApplyRejected, row.Engine)
	}
	if err := d.repos.PubSubs.UpdateDesired(ctx, row.ID, nextSettings, nextCPU, nextMemory); err != nil {
		return UpdateResult{}, err
	}
	res := UpdateResult{StaticRolled: changedPubSubStaticKeys(tpl, row.Settings, nextSettings)}
	if redeem != nil {
		redeem()
	}
	d.kickPubSubs()
	return res, nil
}

// changedPubSubStaticKeys 报告旧→新期望态间变更的 static 键集（排序稳定）。
func changedPubSubStaticKeys(tpl pubtemplate.Template, oldSettings, newSettings map[string]string) []string {
	oldStatic, _ := pubtemplate.SplitSettings(tpl.Knobs(), oldSettings)
	newStatic, _ := pubtemplate.SplitSettings(tpl.Knobs(), newSettings)
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
// _URL/_HOST/_PORT/_USER/_PASSWORD；明文 env 承载凭证 = ADR-0014 附录 A.2
// 追认范围显式扩至本族 refs）。缺实例/引擎未知/凭证解不开都精确失败。
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

// ApplyPubSubEnv 对单 Workload 合并其进程声明的 FLEETLY_PUB_* env。
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
		tpl, err := pubtemplate.For(row.Engine, row.Version)
		if err != nil {
			return fmt.Errorf("pubsub %q (pubsub_refs): %w", ref, err)
		}
		host := engineports.PubSubDNSName(row.ID)
		connURL := tpl.ConnURL(host, password)
		user := ""
		if u, perr := url.Parse(connURL); perr == nil && u.User != nil {
			user = u.User.Username()
		}
		tok := dbEnvToken(ref)
		if w.Env == nil {
			w.Env = map[string]string{}
		}
		w.Env["FLEETLY_PUB_"+tok+"_URL"] = connURL
		w.Env["FLEETLY_PUB_"+tok+"_HOST"] = host
		w.Env["FLEETLY_PUB_"+tok+"_PORT"] = strconv.Itoa(int(tpl.Meta().Port))
		w.Env["FLEETLY_PUB_"+tok+"_USER"] = user
		w.Env["FLEETLY_PUB_"+tok+"_PASSWORD"] = password
	}
	return nil
}
