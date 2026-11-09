package dataservices

// bucketstore 家族收敛步（B5，ADR-0096 决策 1/2/4/5；design §4.3/§4.4）：
// pubsub 族分支的同构复制——活跃行 → 投影（Workload：Image/Command/Probe/
// 卷 bkt 名公式/Ports 9000/Addressing bkt-<id>/项目全部活跃网 + EgressSubset
// ——**root AK/SK 经 Workload env 下发**，ADR-0014 A.2 追认）→ 材料（恒空：
// silo 无 conf 面）→ gen 口 → Ensure → 观测登记与状态推进；Teardown + 级联
// （项目删除逐 bucket store 收口）。零旋钮 v1：无 dynamic 重放门、Update
// 单车道（resources 面落行 + Kick）。凭证轮换 = 声明式（RotateSpec Argv 空
// ——新 SK 进 Workload env → 投影指纹变 → Generation 推进 → stop-first 滚动）。
//
// 与 pubsub 族的两处结构性差异（env 凭证形态的固有代价，设计 §4.4 裁定）：
//
//  1. **每拍解密**：SK 住 Workload env，投影前必须解封凭证 Secret（database/
//     pubsub 族以密文摘要代理、不 fresh 才解密的省法在此不成立——投影本身
//     需要明文）。代价 = age 解封 µs 级 × ≤4 行/项目（配额），可接受。
//  2. **指纹脱敏**：Workload JSON 含明文 SK——EnsureGeneration 的
//     spec_fingerprint 列与 memo 签名首段一律取 **sha256 摘要**（
//     bucketStoreSpecFingerprint），明文永不落行、不进进程内 memo；等值
//     比对语义不变（轮换 → 摘要变 → gen+1）。
//
// 族状态面分持（三环并发调 Step 的并发安全约定）：ensureBkt memo 表随域
// （组合根铸造注入）；rotateMu 跨族共用（轮换是罕见运维动作，全域串行
// 是最小诚实防线）。
//
// 卷实例标记门（ADR-0097 实体中性内核的 bucketstore 消费）：
// ensureBucketStoreVolumeOrigin 与 CheckVolumeOriginForCreate(
// FamilyBucketStore) 在 origin.go——silo 首启在卷内落 family:bucketstore
// 标记，跨族/异名陈旧卷 fail-closed（红队 B-击三三家族闭环）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// IsBucketStoreCredentialSecret 报告 Secret 名是否落在 bucketstore 凭证保留
// 前缀（API PutSecret 受理守卫消费：用户覆写会破坏连接材料单真源）。
func IsBucketStoreCredentialSecret(name string) bool {
	return strings.HasPrefix(name, contract.BKTCredentialSecretPrefix)
}

// BucketStoreConnMaterial 铸连接材料方言串（API 创建面消费的单源公式：模板
// 查询 + 铸名 + 模板 ConnMaterial）。host = bkt-<id>（只在项目网内可解析）；
// access key = 平台合成用户（SiloPlatformAccessKey）。
func BucketStoreConnMaterial(engineName, version, bucketStoreID, secretKey string) (string, error) {
	tpl, err := bucketstoretemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return tpl.ConnMaterial(contract.BucketStoreDNSName(bucketStoreID), bucketstoretemplate.SiloPlatformAccessKey, secretKey), nil
}

// stepBucketStore 是 bucketstore 族收敛环的单次推进（单写者；逐行收敛，
// 错误逐行记日志不阻断）。经分族步 Step(ctx, FamilyBucketStore) 调入。
func (d *DataServices) stepBucketStore(ctx context.Context) {
	rows, err := d.repos.BucketStores.List(ctx)
	if err != nil {
		d.log.Error("bucket store step: list", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		d.reconcileBucketStore(ctx, &rows[i])
		// 子资源尾段（B8，design §4.7）：Bucket 行集对账。
		d.reconcileBucketSubResources(ctx, &rows[i])
	}
}

// reconcileBucketStore 收敛单行（pubsub 族 reconcilePubSub 同构：解封凭证
// → 投影〔含 env 凭证〕→ 脱敏签名短路 → 卷补建/钉住/adoption 守卫 → 标记
// 门 → gen 口 → Ensure → 归属登记与状态推进）。
func (d *DataServices) reconcileBucketStore(ctx context.Context, row *bucketstore.BucketStore) {
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := d.boundedStep(ctx)
	defer cancel()

	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
	if err != nil {
		d.log.Error("bucket store reconcile: engine left the template registry", "bucket_store", row.ID, "engine", row.Engine, "err", err)
		return
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		d.log.Error("bucket store reconcile: resolve project", "bucket_store", row.ID, "err", err)
		return
	}
	staticOnly, _ := bucketstoretemplate.SplitSettings(tpl.Knobs(), row.Settings)
	networks, egress := d.projectNetworkFacts(stepCtx, row.ProjectID)
	volRow, err := d.ensureBucketStoreVolume(stepCtx, row)
	if err != nil {
		d.log.Error("bucket store reconcile: ensure volume", "bucket_store", row.ID, "err", err)
		return
	}
	sec, err := d.repos.Secrets.GetByName(stepCtx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		d.log.Error("bucket store reconcile: resolve credentials", "bucket_store", row.ID,
			"err", fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err))
		return
	}
	// 每拍解密（包注差异 1）：SK 进投影 env。
	material, err := d.bucketStoreMaterial(row, sec)
	if err != nil {
		d.log.Error("bucket store reconcile: resolve credentials", "bucket_store", row.ID, "err", err)
		return
	}
	w, ns, err := ProjectBucketStore(row, team, volRow.ID, networks, egress, tpl, bucketStoreResources(row), staticOnly, material.AccessKey, material.SecretKey)
	if err != nil {
		d.log.Error("bucket store reconcile: project", "bucket_store", row.ID, "err", err)
		return
	}
	ws := []capability.Workload{w}
	// 签名四段（database/pubsub 族同款复合代理；首段取脱敏摘要——包注差异 2）。
	specFP := bucketStoreSpecFingerprint(ws)
	sig := specFP + "\x00" + ns.String() + "\x00" +
		credentialFingerprint(sec.Ciphertext) + "\x00" + settingsDigest(staticOnly)
	now := d.clock.Now()
	if memo, fresh := d.ensureFresh(d.ensureBkt, row.ID, sig, now); fresh {
		d.advanceBucketStoreStatus(stepCtx, row, memo.Gen)
		return
	}
	files, err := tpl.Materials(staticOnly)
	if err != nil {
		d.log.Error("bucket store reconcile: render materials", "bucket_store", row.ID, "err", err)
		return
	}
	materials := capability.Materials{SecretFiles: files}
	if err := d.pinVolumes(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("bucket store reconcile: pin volumes", "bucket_store", row.ID, "err", err)
		return
	}
	if err := d.applyVolumePinning(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("bucket store reconcile: volume pinning merge", "bucket_store", row.ID, "err", err)
		return
	}
	if err := d.guardVolumeCarriers(stepCtx, ns, ws, row.ProjectID); err != nil {
		d.log.Error("bucket store reconcile: volume carrier adoption guard", "bucket_store", row.ID, "err", err)
		return
	}
	// bucketstore 卷实例标记门（ADR-0097 决策 4 实体中性化）：Ensure 前读/铸
	// fleetly-instance.json——异源标记 = 拒绝确保（fail-closed）；在 gen 口
	// 之前——拒绝不推进代次。
	if err := d.ensureBucketStoreVolumeOrigin(stepCtx, row, tpl, volRow); err != nil {
		d.log.Error("bucket store reconcile: volume origin gate", "bucket_store", row.ID, "err", err)
		return
	}
	gen, err := d.repos.BucketStores.EnsureGeneration(stepCtx, row.ID, specFP)
	if err != nil {
		d.log.Error("bucket store reconcile: generation", "bucket_store", row.ID, "err", err)
		return
	}
	report, err := d.runtime.Ensure(stepCtx, ns, ws, capability.Generation(gen), materials)
	if err != nil {
		d.log.Error("bucket store reconcile: ensure", "bucket_store", row.ID, "generation", gen, "err", err)
		d.ensureForget(d.ensureBkt, row.ID)
		return
	}
	wireGen := report.WireGen(w, capability.Generation(gen))
	d.ensureRemember(d.ensureBkt, row.ID, contract.Memo{Sig: sig, Gen: uint64(wireGen), At: now})
	d.recordBucketStoreOwners(row.ID, uint64(wireGen), ws)
	d.recordBucketStoreExpected(row.ID, uint64(wireGen))
	d.advanceBucketStoreStatus(stepCtx, row, uint64(wireGen))
}

// bucketStoreSpecFingerprint 是投影指纹的脱敏形态（包注差异 2）：
// ManagedFingerprint 的 sha256 hex——Workload env 携带明文 SK，摘要化后
// 行列与 memo 零明文；等值语义与原指纹一致（轮换/镜像 bump/资源变更 →
// 摘要变 → Generation 推进）。
func bucketStoreSpecFingerprint(ws []capability.Workload) string {
	sum := sha256.Sum256([]byte(contract.ManagedFingerprint(ws)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// bucketStoreResources 把行资源面折为载体上限（nil = 无限制）。
func bucketStoreResources(row *bucketstore.BucketStore) *capability.Resources {
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

// advanceBucketStoreStatus 从观测缓存推进行状态（gen 匹配才采信；观测缓存
// 键 = 投影 Workload ID = 行 ID）。
func (d *DataServices) advanceBucketStoreStatus(ctx context.Context, row *bucketstore.BucketStore, gen uint64) {
	ev, seen := d.observationOf(row.ID)
	if status, ok := dbStatusFromObservation(ev, gen); ok && seen && status != row.Status {
		if err := d.repos.BucketStores.SetStatus(ctx, row.ID, status); err != nil {
			d.log.Error("bucket store reconcile: set status", "bucket_store", row.ID, "err", err)
		}
	}
}

// bucketStoreMaterial 解封凭证 Secret → 连接材料方言串 → 三段回读（写读
// 同源律：bucketstoretemplate.ParseConnMaterial）。
func (d *DataServices) bucketStoreMaterial(row *bucketstore.BucketStore, sec *secret.Secret) (bucketstoretemplate.ConnMaterial, error) {
	if d.cipher == nil {
		return bucketstoretemplate.ConnMaterial{}, fmt.Errorf("bucket store credentials require the master key (data root keys/ missing)")
	}
	plain, err := d.cipher.Open(sec.Ciphertext)
	if err != nil {
		return bucketstoretemplate.ConnMaterial{}, fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	material, err := bucketstoretemplate.ParseConnMaterial(string(plain))
	if err != nil {
		return bucketstoretemplate.ConnMaterial{}, fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	return material, nil
}

// ProjectBucketStore 把行投影为用户域受管 Workload（pubsub 族 ProjectPubSub
// 同构：NamespaceRef.BucketStore 轴；镜像/端口/卷目标来自模板；Workload ID
// = 行 ID；Addressing = bkt-<id>；挂全部活跃项目网 + EgressSubset；env 携带
// root AK/SK——ADR-0014 A.2 追认范围）。
func ProjectBucketStore(row *bucketstore.BucketStore, team, volumeID string, networks []string, egress map[string]bool, tpl bucketstoretemplate.Template, limits *capability.Resources, staticSettings map[string]string, accessKey, secretKey string) (capability.Workload, capability.NamespaceRef, error) {
	if row.ID == "" || row.ProjectID == "" {
		return capability.Workload{}, capability.NamespaceRef{}, fmt.Errorf("bucket store row has no identity")
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name}
	env, command, err := tpl.Workload(accessKey, secretKey, staticSettings)
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
		// 对象存储干净关停（在途 multipart/元数据 fsync 收口）：与数据库族
		// 同口径 60s。
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
		Addressing: []capability.Address{{Name: contract.BucketStoreDNSName(row.ID)}},
		Resources:  limits,
	}
	return w, ns, nil
}

// TeardownBucketStore 收口拆除（Runtime.Remove 幂等拆域内全部载体 + 清
// 归属/期望/观测缓存；tombstone 与事件由 API 受理位在后续事务落——先变更
// 后留痕）。单实例删除与项目删除级联共用本口。
func (d *DataServices) TeardownBucketStore(ctx context.Context, id string) error {
	row, err := d.repos.BucketStores.Get(ctx, id)
	if err != nil {
		return err
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name}
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	rctx, rcancel := d.boundedStep(ctx)
	removeErr := d.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("bucket store teardown: %w", removeErr)
	}
	d.forgetObservation(row.ID)
	d.forgetBucketStoreExpected(row.ID)
	d.ensureForget(d.ensureBkt, row.ID)
	return nil
}

// RotateBucketStorePassword 轮换凭证（ADR-0096 决策 4：silo 声明式——
// RotateSpec Argv 空，Secret 重写后收敛环以新 SK 重投影 → 指纹变 →
// Generation 推进 → stop-first 滚动即生效；Argv 非空形态〔未来引擎〕走工具
// 容器数据面改密同 database 族）。rotateMu 跨族共用（全域单飞）。
func (d *DataServices) RotateBucketStorePassword(ctx context.Context, bucketStoreID string) error {
	d.rotateMu.Lock()
	defer d.rotateMu.Unlock()

	row, err := d.repos.BucketStores.Get(ctx, bucketStoreID)
	if err != nil {
		return err
	}
	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
	if err != nil {
		return fmt.Errorf("engine %q left the template registry: %w", row.Engine, err)
	}
	current, err := d.bucketStoreSecretKey(ctx, row)
	if err != nil {
		return err
	}
	next := bucketstoretemplate.MintPassword()
	spec, err := tpl.RotatePassword(contract.BucketStoreDNSName(row.ID), current, next)
	if err != nil {
		return err
	}
	if len(spec.Argv) > 0 {
		req := capability.UtilityRequest{
			ID:          "rotate-" + row.ID,
			Namespace:   capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name},
			Image:       tpl.Image(),
			User:        tpl.ToolUser(), // B8-fix 通道；silo 缺省空（Argv 空——面缺席，通道预留）
			Argv:        spec.Argv,
			Env:         spec.Env,
			Networks:    d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
			SecretFiles: spec.SecretFiles,
		}
		if err := d.runUtilityContainer(ctx, req); err != nil {
			return fmt.Errorf("%w: %s", ErrRotateRejected, err)
		}
	}
	newMaterial := tpl.ConnMaterial(contract.BucketStoreDNSName(row.ID), bucketstoretemplate.SiloPlatformAccessKey, next)
	sealed, err := d.cipher.Produce([]byte(newMaterial))
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
	d.kickBucketStores()
	return nil
}

// BucketStoreUpdateRequest 是 bucketstore Update 的域输入（PubSubUpdateRequest
// 同形；Admit 钩子按 bucketstore 行签名——锁内行 = 执行真源）。
type BucketStoreUpdateRequest struct {
	Settings  map[string]string
	Resources *ResourceUpdate
	Admit     func(row *bucketstore.BucketStore) (redeem func(), err error)
}

// UpdateBucketStore 执行期望态变更（ADR-0095 决策 5 双车道在本族的单车道
// 形态——v1 零旋钮：resources 面落行 + Kick，收敛环受监督滚动；settings
// 恒空集由受理位 ValidateSettings 钉死）。utilityMu per-entity 全程持锁
// （Admit→行→烧票原子化）。
func (d *DataServices) UpdateBucketStore(ctx context.Context, bucketStoreID string, req BucketStoreUpdateRequest) (UpdateResult, error) {
	unlock := d.utilityMu.acquire(bucketStoreID)
	defer unlock()

	row, err := d.repos.BucketStores.Get(ctx, bucketStoreID)
	if err != nil {
		return UpdateResult{}, err
	}
	var redeem func()
	if req.Admit != nil {
		if redeem, err = req.Admit(row); err != nil {
			return UpdateResult{}, err
		}
	}
	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
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
	// dynamic 车道防御（pubsub 族同款哨兵）：本族无 dynamic 旋钮。
	_, dynamicOnly := bucketstoretemplate.SplitSettings(tpl.Knobs(), nextSettings)
	if len(dynamicOnly) > 0 {
		return UpdateResult{}, fmt.Errorf("%w: engine %q declares dynamic knobs but the bucket store family carries no synchronous dynamic-settings lane yet", ErrSettingsApplyRejected, row.Engine)
	}
	if err := d.repos.BucketStores.UpdateDesired(ctx, row.ID, nextSettings, nextCPU, nextMemory); err != nil {
		return UpdateResult{}, err
	}
	res := UpdateResult{StaticRolled: changedBucketStoreStaticKeys(tpl, row.Settings, nextSettings)}
	if redeem != nil {
		redeem()
	}
	d.kickBucketStores()
	return res, nil
}

// changedBucketStoreStaticKeys 报告旧→新期望态间变更的 static 键集（排序
// 稳定；零旋钮族恒空）。
func changedBucketStoreStaticKeys(tpl bucketstoretemplate.Template, oldSettings, newSettings map[string]string) []string {
	oldStatic, _ := bucketstoretemplate.SplitSettings(tpl.Knobs(), oldSettings)
	newStatic, _ := bucketstoretemplate.SplitSettings(tpl.Knobs(), newSettings)
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

// InjectBucketStoreEnv 按 spec 的 process bucketstore_refs 把 FLEETLY_BKT_*
// 合并进 Workload Env（ADR-0096 决策 5：NAME=引用名 UPPER_SNAKE；S3 方言
// 出 _ENDPOINT/_ACCESS_KEY/_SECRET_KEY；明文 env 承载凭证 = ADR-0014 附录
// A.2 追认范围显式扩至本族 refs——SECRET_KEY 是长效凭证、暴露面大于可原地
// 轮换的 DB 密码，轮换=滚动补偿）。缺实例/引擎未知/凭证解不开都精确失败。
func (d *DataServices) InjectBucketStoreEnv(ctx context.Context, spec *specv1.AppSpec, ws []capability.Workload, projectID string) error {
	byName := make(map[string]*specv1.ProcessSpec, len(spec.GetProcesses()))
	for _, p := range spec.GetProcesses() {
		byName[p.GetName()] = p
	}
	for i := range ws {
		w := &ws[i]
		if err := d.ApplyBucketStoreEnv(ctx, w, byName[w.Process], projectID); err != nil {
			return err
		}
	}
	return nil
}

// ApplyBucketStoreEnv 对单 Workload 合并其进程声明的 FLEETLY_BKT_* env。
func (d *DataServices) ApplyBucketStoreEnv(ctx context.Context, w *capability.Workload, p *specv1.ProcessSpec, projectID string) error {
	if p == nil || len(p.GetBucketstoreRefs()) == 0 {
		return nil
	}
	for _, ref := range p.GetBucketstoreRefs() {
		row, err := d.repos.BucketStores.GetByName(ctx, projectID, ref)
		if err != nil {
			return fmt.Errorf("bucket store %q (bucketstore_refs): not found — create the managed bucket store first (%w)", ref, err)
		}
		sec, err := d.repos.Secrets.GetByName(ctx, projectID, row.CredentialsRef)
		if err != nil {
			return fmt.Errorf("bucket store %q (bucketstore_refs): lookup credential secret %q: %w", ref, row.CredentialsRef, err)
		}
		material, err := d.bucketStoreMaterial(row, sec)
		if err != nil {
			return fmt.Errorf("bucket store %q (bucketstore_refs): %w", ref, err)
		}
		tok := dbEnvToken(ref)
		if w.Env == nil {
			w.Env = map[string]string{}
		}
		w.Env["FLEETLY_BKT_"+tok+"_ENDPOINT"] = material.Endpoint
		w.Env["FLEETLY_BKT_"+tok+"_ACCESS_KEY"] = material.AccessKey
		w.Env["FLEETLY_BKT_"+tok+"_SECRET_KEY"] = material.SecretKey
	}
	return nil
}
