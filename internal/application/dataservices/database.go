package dataservices

// database 类型收敛步（ADR-0029 用户域收敛循环：用户域受管形态的第三条部署
// 轨——不进 Deployment 状态机（App 键控）、不走 fleetly/system 受管域（数据
// 库挂项目网供 App 连）。活跃行 → DatabaseSpec（IR 单真源）→ Workload 投影
// → Runtime.Ensure（唯一写动词，幂等重放）；per-Database Generation 落行
// 持久（重启安全：指纹未变则同号重放，载体不滚）。
//
// 承继：P3 批 4 收编（databaseDomain → databases）→ P4 wave-2a 嵌入改形 →
// P5 wave-4 拆包（internal/application/databases，ADR-0088 决策 2 P5 形态）
// → B2e 应用模块归一（ADR-0096 决策 1）：应用模块壳（DataServices/Deps/New/分类型步）
// 移居 dataservices.go，本文件只剩 database 类型的收敛步与受理帮手；循环表
// database 循环经 StepFor(KindDatabase) 调入 stepDatabase——行为与归一前
// 逐字节一致（明示漂移清单见包注）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// IsDatabaseCredentialSecret 报告 Secret 名是否落在数据库凭证保留前缀
// （API PutSecret 的受理契约测试消费：用户覆写会破坏连接串单真源——值必须
// 恒为完整连接 URL，ADR-0029 决策 6）。P5 wave-4 拆包导出（gRPC 受理面
// 经核声明别名零改）。
func IsDatabaseCredentialSecret(name string) bool {
	return strings.HasPrefix(name, contract.DBCredentialSecretPrefix)
}

// DatabaseConnectionURL 铸连接串（API 创建面消费的单源公式；组合 =
// 模板查询 + 铸名 + 模板 ConnURL）。host 是网内 DNS 名 db-<id>——只在
// 项目网内可解析，这是诚实的连接面。B2c 起按行值绑版（version 来自
// databases 行；URL 方言跨版本无分叉，行为不变——绑版是纪律不是分叉面）。
// P5 wave-4 拆包导出（核侧经声明别名零改）。
func DatabaseConnectionURL(engineName, version, databaseID, password string) (string, error) {
	tpl, err := databasetemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return tpl.ConnURL(contract.DatabaseDNSName(databaseID), password), nil
}

// stepDatabase 是 database 类型收敛循环的单次推进（单写者；逐行收敛，错误逐行
// 记日志不阻断）。B2e 起不再直接挂循环表——经分类型步 Step(ctx, KindDatabase)
// 调入（construct.go 的 database 循环 step = e.dataservices.StepFor(
// dataservicetemplate.KindDatabase)）；函数体与归一前的导出 Step 逐字节一致。
func (d *DataServices) stepDatabase(ctx context.Context) {
	rows, err := d.repos.Databases.List(ctx)
	if err != nil {
		d.log.Error("database step: list", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		d.reconcileDatabase(ctx, &rows[i])
	}
}

// reconcileDatabase 收敛单行：投影（模板 + 项目网 + 寻址 + 卷）→ 材料
// （凭证 Secret 解密回读）→ gen 口（指纹变化才推进）→ Ensure → 归属登记
// 与状态推进。
//
// 签名短路（N1 C16）：Ensure 全部输入的指纹（投影集 + 寻址域 + 凭证密文
// 指纹）未变且未到强制重放节拍 → 跳过解密/卷补建/钉住/gen 口/Ensure 全套
// 重活，仅保留观测驱动的状态推进（观测缓存与 Ensure 无关，短路拍照常收
// 敛状态）。凭证以 Secret 行密文摘要为材料指纹的代理——轮换/重封装改变
// 密文即短路失效，无需解密（宁重下发不漏变更）。
func (d *DataServices) reconcileDatabase(ctx context.Context, row *database.Database) {
	// 恢复未终态成功的预置卷形态（redis）：载体首启前不 Ensure——空卷首启
	// 即数据丢失，预置完成由 backup 环落 succeeded 后收敛（ADR-0039 决策 6
	// 修订：pending/running 在途与 failed 终态都挡住 Ensure——半恢复卷不得
	// 静默首启；succeeded 才放行，restore_from_backup 保留不影响门控；探测
	// 只消费 Mode，密码传占位值过渲染闸）。B2c 起按行值绑版。
	if row.RestoreFromBackup != "" {
		switch row.RestoreState {
		case database.RestoreStatePending, database.RestoreStateRunning, database.RestoreStateFailed:
			if tpl, err := databasetemplate.For(row.Engine, row.Version); err == nil {
				if spec, err := tpl.Restore(contract.DatabaseDNSName(row.ID), "probe"); err == nil && spec.Mode == databasetemplate.RestorePreseed {
					return
				}
			}
		}
	}
	// 重放门（B2d，ADR-0095 决策 6）：dynamic 子集的失配重放在 ensure cache 短路
	// 判定**之前**执行——dynamic 半边是事件触发收敛，不随 Ensure 短路
	// 停摆（红队 B-AT-08 的降格口径）。门内自判 running/恢复挂起/退避。
	// 落点在 maintenance 读锁**之外**（锁序 utilityMu → maintenance，与
	// Update 同序）：工具容器执行链自取 maintenance 读锁，锁内调用即
	// RWMutex 读锁递归——网络重建写者排队时单写者循环自死锁（update.go 包注）。
	d.replayDynamicSettings(ctx, row)
	// 维护互斥读半边（ADR-0046）：Ensure 族与网络重建的串行化锚；锁等待
	// 不占步预算（排队语义）。单步带界（staging 实证：无界的 docker API
	// hang 卡死单写者循环）。
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := d.boundedStep(ctx)
	defer cancel()

	// B2c 起按行值绑版（version 列单源；弃用版本照常绑——受理拒 vs 行解析
	// 放行分档，ADR-0094 决策 6；For 未命中 = 版本已移除的存量行，防御跳过
	//——移除序）。
	tpl, err := databasetemplate.For(row.Engine, row.Version)
	if err != nil {
		// 受理位已校验值域；此处防御模板下线后的存量行（诚实记日志，
		// 载体保持现状不受扰）。
		d.log.Error("database reconcile: engine left the template registry", "database", row.ID, "engine", row.Engine, "err", err)
		return
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		d.log.Error("database reconcile: resolve project", "database", row.ID, "err", err)
		return
	}
	// 喂入律（B2d，ADR-0095 决策 4 / design §3.2）：static 子集在此单点
	// 预过滤——投影（Workload 命令面）与材料（Materials 渲染）只喂
	// staticOnly，dynamic 键结构性不进滚动面（契约测试
	// TestDynamicKeysAbsentFromProjection 执法）。
	staticOnly, _ := databasetemplate.SplitSettings(tpl.Knobs(), row.Settings)
	spec := databaseSpecFromRow(row, tpl)
	if err := specir.ValidateDatabase(spec); err != nil {
		d.log.Error("database reconcile: invalid spec", "database", row.ID, "err", err)
		return
	}
	networks, egress := d.projectNetworkFacts(stepCtx, row.ProjectID)
	// 卷行幂等补建前移到投影之前（ADR-0097 决策 2）：投影载体键取卷行 id
	//（名 = 数据库名的确定性公式处解析成行 id；行缺失在此自愈出生——
	// 出生序按既有法：收敛循环补建，不依赖 API 侧时序约定）。
	volRow, err := d.ensureDatabaseVolume(stepCtx, row)
	if err != nil {
		d.log.Error("database reconcile: ensure volume", "database", row.ID, "err", err)
		return
	}
	w, ns, err := ProjectDatabase(spec, team, volRow.ID, row.Name, networks, egress, tpl, rowResources(row), staticOnly)
	if err != nil {
		d.log.Error("database reconcile: project spec", "database", row.ID, "err", err)
		return
	}
	// 凭证 Secret 行预读（不解密）：短路签名的材料面 + 下发段的解密输入
	// 单源。读失败与既有材料失败路径同形（不 Ensure，载体保持现状）。
	sec, err := d.repos.Secrets.GetByName(stepCtx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		d.log.Error("database reconcile: resolve credentials", "database", row.ID,
			"err", fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err))
		return
	}
	// 签名四段（B2d，ADR-0095 决策 4 红队 A5 复合代理）：投影集 + 寻址域
	// + 凭证密文摘要 + **static 子集摘要**。第四段不得字面用
	// MaterialsFingerprint（吃已渲染字节 → 每拍解密凭证，推翻「凭证以
	// 密文摘要为代理」立法）——以旋钮子集（行值、零解密）为代理：static
	// 变更即时失效短路 → Ensure 真实重发（材料含新值），不等 60s 兜底
	// 节拍。Generation 不随材料推进（RotatePassword 先例——static 滚动
	// 由材料/spec 指纹机械承载）。
	sig := contract.ManagedFingerprint([]capability.Workload{w}) + "\x00" + ns.String() + "\x00" +
		credentialFingerprint(sec.Ciphertext) + "\x00" + settingsDigest(staticOnly)
	now := d.clock.Now()
	if memo, fresh := d.ensureFresh(d.ensure, row.ID, sig, now); fresh {
		d.advanceDatabaseStatus(stepCtx, row, memo.Gen)
		return
	}
	materials, err := d.databaseMaterials(stepCtx, row, tpl, sec, staticOnly)
	if err != nil {
		// 凭证面失败 = 不 Ensure（载体保持现状），下一拍重试——静默拆载体
		// 比收敛失败更糟。
		d.log.Error("database reconcile: resolve credentials", "database", row.ID, "err", err)
		return
	}
	ws := []capability.Workload{w}
	// 卷钉住既有面：无显式钉住的卷首挂锚定，钉住合并进 Placement。
	if err := d.pinVolumes(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("database reconcile: pin volumes", "database", row.ID, "err", err)
		return
	}
	if err := d.applyVolumePinning(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("database reconcile: volume pinning merge", "database", row.ID, "err", err)
		return
	}
	// 存量 adoption 契约测试（ADR-0097 决策 3）：旧名载体在场且新键缺席 =
	// 显式拒绝（载体保持现状不受扰；搬移动词留后续）。契约测试在 gen 口
	// 之前——拒绝不推进代次。
	if err := d.guardVolumeCarriers(stepCtx, ns, ws, row.ProjectID); err != nil {
		d.log.Error("database reconcile: volume carrier adoption guard", "database", row.ID, "err", err)
		return
	}
	// 库卷实例标记门（ADR-0097 决策 4）：Ensure 前读/铸 fleetly-instance.json
	//——异源标记（同名陈旧卷）= 拒绝确保（fail-closed：新凭证对旧数据面的
	// 隐蔽认证失败在此显式化）；同源/缺席即过。在 gen 口之前——拒绝不推进
	// 代次。
	if err := d.ensureVolumeOrigin(stepCtx, row, tpl, volRow); err != nil {
		d.log.Error("database reconcile: volume origin gate", "database", row.ID, "err", err)
		return
	}
	gen, err := d.repos.Databases.EnsureGeneration(stepCtx, row.ID, contract.ManagedFingerprint(ws))
	if err != nil {
		d.log.Error("database reconcile: generation", "database", row.ID, "err", err)
		return
	}
	report, err := d.runtime.Ensure(stepCtx, ns, ws, capability.Generation(gen), materials)
	if err != nil {
		d.log.Error("database reconcile: ensure", "database", row.ID, "generation", gen, "err", err)
		d.ensureForget(d.ensure, row.ID) // 失败清签名：下拍重试
		return
	}
	// ADR-0064 归属回写（与 materialize 同律）：内容等价 skip 的载体保留
	// 现存代次——登记必须按回执 wire-gen，call-gen 会与载体现行 gen 错位、
	// Watch 对照发假 drift（2026-10-18 upgrade 矩阵实证：指纹跨二进制
	// 漂移 → gen 推进 → 载体 skip 在旧 gen → call-gen 登记即漂移假阳性）。
	wireGen := report.WireGen(w, capability.Generation(gen))
	d.ensureRemember(d.ensure, row.ID, contract.Memo{Sig: sig, Gen: uint64(wireGen), At: now})
	// 归属/期望登记（观测路由 + 稳态看门狗；driftScan 对 database/ 前缀
	// 跳过 spec 对照——非 App 行键）。P4 wave-2a 经观测窄接口端口，P5
	// wave-4 拆包起窄化为 database 轴签名（databaseOwner 铸造留核绑定
	// 闭包——域判别不在应用模块）。
	d.recordOwners(row.ID, uint64(wireGen), []capability.Workload{w})
	d.recordExpected(row.ID, uint64(wireGen))
	d.advanceDatabaseStatus(stepCtx, row, uint64(wireGen))
}

// rowResources 把行资源面折为载体上限（nil = 无限制 = 现状，ADR-0095
// 决策 7；static 语义——指纹进 ManagedFingerprint，资源变更即受监督滚动）。
func rowResources(row *database.Database) *capability.Resources {
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

// advanceDatabaseStatus 从观测缓存推进行状态（gen 匹配才采信——旧 gen 的
// 迟到观测不翻状态）。gen 是载体实际在 wire 上的代次（回执 wire-gen，与
// 归属登记同律 R13-L3——call-gen 在内容等价 skip 下恒不等于观测）。Ensure
// 成功拍与签名短路拍共用（短路拍 Ensure 不发，状态收敛不停——观测经
// Watch 流独立到达）。
func (d *DataServices) advanceDatabaseStatus(ctx context.Context, row *database.Database, gen uint64) {
	// 观测缓存键 = 投影 Workload ID = 数据库行 ID（projectDatabase 铸造式；
	// database_test 以行 ID 注入观测即依赖此恒等）。
	ev, seen := d.observationOf(row.ID)
	if status, ok := dbStatusFromObservation(ev, gen); ok && seen && status != row.Status {
		if err := d.repos.Databases.SetStatus(ctx, row.ID, status); err != nil {
			d.log.Error("database reconcile: set status", "database", row.ID, "err", err)
		}
	}
}

// credentialFingerprint 是凭证密文的轻量摘要（短路签名的材料面；sha256
// 截断——防碰撞足够，密文不可逆）。
func credentialFingerprint(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	return hex.EncodeToString(sum[:8])
}

// dbStatusFromObservation 把 Workload 观测映射为 Database 状态（长运行
// 面：一次性终态不适用；gen 不匹配返回 ok=false）。
func dbStatusFromObservation(ev capability.WorkloadEvent, gen uint64) (string, bool) {
	if uint64(ev.Generation) != gen {
		return "", false
	}
	switch ev.State {
	case capability.WorkloadRunning:
		return database.StatusRunning, true
	case capability.WorkloadPending:
		return database.StatusPending, true
	case capability.WorkloadDegraded:
		return database.StatusDegraded, true
	case capability.WorkloadStopped:
		return database.StatusStopped, true
	default:
		return "", false // completed/failed：长运行库不落在一次性终态
	}
}

// databaseMaterials 装配 DB Workload 的凭证材料：解密凭证 Secret → 连接
// 串 → 密码回读 → 模板渲染（postgres 密码文件 + conf 覆写片段 / redis
// 配置文件 / mysql 双密码文件）。密码不进 env/label/argv（ADR-0014 材料
// 纪律；值经载体按值指纹命名随 spec diff 分发——轮换 Secret 即滚动替换，
// zot 附录 B 同款机制）。sec 由调用方预读传入（短路签名的同一次读取——
// 不做二次点查）。B2d 起喂入律接线：第二参 = SplitSettings 预过滤的
// static 子集（B2b 遗留清偿——空 settings 渲染面与现状逐字节一致）。
func (d *DataServices) databaseMaterials(ctx context.Context, row *database.Database, tpl databasetemplate.Template, sec *secret.Secret, staticOnly map[string]string) (capability.Materials, error) {
	if d.cipher == nil {
		return capability.Materials{}, fmt.Errorf("database credentials require the master key (data root keys/ missing)")
	}
	plain, err := d.cipher.Open(sec.Ciphertext)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := databasetemplate.PasswordFromURL(string(plain))
	if err != nil {
		return capability.Materials{}, fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	files, err := tpl.Materials(password, staticOnly)
	if err != nil {
		// 渲染面字符集闸（databasetemplate.validatePassword）：fail-closed，载体
		// 保持现状不受扰。
		return capability.Materials{}, fmt.Errorf("render materials for engine %q: %w", row.Engine, err)
	}
	return capability.Materials{SecretFiles: files}, nil
}

// EgressNetworkMap 返回 Project 活跃网络的 egress:none 名集（投影期的
// 网络事实面——k3s Provider 铸载体级 deny 的数据源，ADR-0052 决策 6；
// 查询失败如实返回空集：隔离是收敛不变式，下一拍重放自愈）。P5 wave-4
// 拆包导出（原 egressNetworkMap——核 render/task/bluegreen 的投影消费经
// 应用模块指针，消费点零改）。
func (d *DataServices) EgressNetworkMap(ctx context.Context, projectID string) map[string]bool {
	_, egress := d.projectNetworkFacts(ctx, projectID)
	return egress
}

// databaseSpecFromRow 由行 + 模板组装 DatabaseSpec（IR 单真源：投影输入
// 恒经 spec 形态，叶子校验可用）。B2c 起行值直读（ADR-0094 决策 3 回显
// 换源：Version = databases 行 version 列——退役模板动态取双源）。
func databaseSpecFromRow(row *database.Database, tpl databasetemplate.Template) *specv1.DatabaseSpec {
	return &specv1.DatabaseSpec{
		SchemaVersion:  specir.SchemaVersion,
		Database:       &specv1.DatabaseRef{Id: row.ID, Project: row.ProjectID},
		Engine:         row.Engine,
		Version:        row.Version,
		CredentialsRef: row.CredentialsRef,
	}
}

// TeardownDatabase 收口拆除（ADR-0029 决策 8）：Runtime.Remove（幂等拆域
// 内全部载体）+ 清归属/期望/观测缓存。tombstone 与事件由 API 受理位在
// 后续事务落（先变更后留痕，ADR-0023 同款序）。单库删除与项目删除级联
// 共用本口（级联语义见 ADR-0029 追记 2026-10-05）。
func (d *DataServices) TeardownDatabase(ctx context.Context, id string) error {
	row, err := d.repos.Databases.Get(ctx, id)
	if err != nil {
		return err // ErrNotFound → API 404（对齐 TeardownApp 口径）
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, Database: row.ID, DatabaseName: row.Name}
	// 维护互斥读半边（项目删除级联批，2026-10-05）：Remove 是载体写动词，
	// 与 Ensure 族同面——网络重建（写半边）的 detach→rm→create→attach 全
	// 序期间不得插入拆载体（半拆网与半拆库交错会把重建的 re-attach 面对
	// 已逝归属）。锁等待不占步预算（排队语义，lockMaintenance 约定：读锁
	// 在带界 ctx 派生之前获取）。
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（B15-1，批 3 判定的反转）：API 请求路径的 Remove 挂死会
	// 卡住 API 调用本身（与收敛循环卡死同害）。带 ManagedStepTimeout 硬上限，
	// 超时如实上抛（API 404/冲突语义不变，行保持可重试收口）。
	rctx, rcancel := d.boundedStep(ctx)
	removeErr := d.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("database teardown: %w", removeErr)
	}
	// 收口清理经观测窄接口端口（forgetObservation/forgetExpected），应用模块
	// 不直写 obs/expect 内部。
	d.forgetObservation(row.ID)
	d.forgetExpected(row.ID)
	d.ensureForget(d.ensure, row.ID)   // 签名随域收口作废（同 ID 永不复用，防御性清理）
	d.appliedForget(d.applied, row.ID) // dynamic 热应用 ensure cache 同域收口（B2d）
	d.clearReplayFailure(row.ID)       // 重放退避锚同域收口（B2d）
	return nil
}
