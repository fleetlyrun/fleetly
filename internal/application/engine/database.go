package engine

// Database 域收敛环（ADR-0029 决策 3）：用户域受管形态的第三条部署轨——
// 不进 Deployment 状态机（App 键控）、不走 fleetly/system 受管域（数据库
// 挂项目网供 App 连）。活跃行 → DatabaseSpec（IR 单真源）→ Workload 投影
// → Runtime.Ensure（唯一写动词，幂等重放）；per-Database Generation 落行
// 持久（重启安全：指纹未变则同号重放，载体不滚）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// dbCredentialSecretPrefix 是数据库凭证 Secret 的命名约定（database:<name>；
// registry:<host> 先例。值 = 完整连接 URL，ADR-0029 决策 6）。
const dbCredentialSecretPrefix = "database:"

// databases 是 Database 域卫星实例态（ADR-0029 用户域收敛环）。P3 批 4 收编：
// databaseDomain → databases（复数普通名词零后缀，ADR-0075 命名法则）；P3
// 收官批 8 收编：类型体自 engine.go 归位 database.go（壳收口）；P4 wave-2a
// 嵌入改形（ADR-0088 决策 2/3/4）：接收者已下域 *databases（域内短名 d，
// b/d/m 三星同批裁定），database.go/databaseenv.go/rotate.go 的方法经嵌入
// 提升（databaseStep 仍在环表、gRPC Database 受理/轮换面、materialize 的
// env 注入面与 task 的 per-Workload 注入零改）；依赖显式逐字段 + 机制端口
// 注入（construct.go 装配）。禁挂 *Engine 回引（enginebackref 守卫执法）。
type databases struct {
	ensure map[string]ensureMemo // databaseID → 上次成功 Ensure
	// rotateMu 串行化 Database 凭证轮换（IA v3 二期⑤b）：并发轮换的
	// utility/Secret 写交错可能落下 Secret 与数据面不一致的终态——全域
	// 单飞是罕见运维动作的最小诚实防线（rotate.go 注）。P4 wave-2a 随域
	// 迁入（ADR-0088 决策 4：rotateMu→databases）。
	rotateMu sync.Mutex

	// 显式依赖（实测消费面，P4 wave-2a 逐字段注入）：runtime 是载体动词
	// 消费面（Ensure/Remove——核副本双持，P5 过渡形态）；repos 是行读写
	// 聚合目录（Databases 行集/Secrets 凭证——transact.Stores 整体注入，
	// ADR-0088 决策 3）；cipher 是凭证 Secret 解封/重封（核副本双持）；
	// utility 是 Runtime 工具容器子面（rotate 的数据面改密消费——装配期
	// 经 FacesOf 探测，与 backups 卫星同名副本双持，P5 过渡形态通则二；
	// 旋转恢复链的 backups 消费经提升读卫星自有副本=各自单一真源）；
	// log/clock 域内日志与节拍。
	runtime capability.Runtime
	repos   transact.Stores
	cipher  *material.Cipher
	utility capability.RuntimeUtility
	log     *slog.Logger
	clock   state.Clock

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（Ensure/Remove 与网络重建
	// 串行化，ADR-0046；端口名对齐锁入口方法名集——maintenanceguard 判据
	// 继续命中）；boundedStep 是步带界端口（ManagedStepTimeout 事实面，
	// ensure.go 统一入口）；ensureFresh/ensureRemember/ensureForget 是
	// memo 端口（ensureMu 单锁留核、memo map 随域，ADR-0088 决策 4）；
	// projectTeam/projectTeamOf/projectNetworkFacts/projectNetworkFactsNamesOnly
	// 是 facts 端口（解析面五族住 resolve.go，resolutionface 锚不动）；
	// ensureDatabaseVolume/databasePassword 是 facts 端口的卷登记/凭证解
	// 析族（同面）；pinVolumes/applyVolumePinning 是 volumePinner 端口
	//（卷钉住双动词仍挂 *Engine 在 materials.go——intake 卫星化是 wave-2b，
	// 端口是 volumePinner 白名单实体的物理首形，ADR-0088 决策 7）；kick-
	// Databases 是轮换后的收敛环唤醒（Loop 挂载留核）；recordOwners/
	// recordExpected/forgetExpected/forgetObservation/observationOf 是观测
	// 窄接口（obs/expect 域登记函数与单点读，禁止卫星直写内部——P4
	// wave-2a 设计点，backups/metrics 的 ownerOf 先例续）。
	lockMaintenance              func() func()
	boundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	ensureFresh                  func(memo map[string]ensureMemo, key, sig string, now time.Time) (ensureMemo, bool)
	ensureRemember               func(memo map[string]ensureMemo, key string, rec ensureMemo)
	ensureForget                 func(memo map[string]ensureMemo, key string)
	projectTeam                  func(ctx context.Context, projectID string) (string, error)
	projectTeamOf                func(projectID string) string
	projectNetworkFacts          func(ctx context.Context, projectID string) ([]string, map[string]bool)
	projectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	ensureDatabaseVolume         func(ctx context.Context, row *database.Database) error
	databasePassword             func(ctx context.Context, row *database.Database) (string, error)
	pinVolumes                   func(ctx context.Context, ws []capability.Workload, projectID string) error
	applyVolumePinning           func(ctx context.Context, ws []capability.Workload, projectID string) error
	kickDatabases                func()
	recordOwners                 func(gen uint64, ws []capability.Workload, ownerOf func(capability.Workload) workloadOwner)
	recordExpected               func(owner workloadOwner, gen uint64)
	forgetExpected               func(owner workloadOwner)
	forgetObservation            func(wid string)
	observationOf                func(wid string) (capability.WorkloadEvent, bool)
}

// IsDatabaseCredentialSecret 报告 Secret 名是否落在数据库凭证保留前缀
// （API PutSecret 的受理守卫消费：用户覆写会破坏连接串单真源——值必须
// 恒为完整连接 URL，ADR-0029 决策 6）。
func IsDatabaseCredentialSecret(name string) bool {
	return strings.HasPrefix(name, dbCredentialSecretPrefix)
}

// DatabaseConnectionURL 铸连接串（API 创建面消费的单源公式；组合 =
// 模板查询 + 铸名 + 模板 ConnURL）。host 是网内 DNS 名 db-<id>——只在
// 项目网内可解析，这是诚实的连接面。
func DatabaseConnectionURL(engineName, databaseID, password string) (string, error) {
	tpl, ok := dbtemplate.For(engineName)
	if !ok {
		return "", fmt.Errorf("engine: unknown database engine %q", engineName)
	}
	return tpl.ConnURL(DatabaseDNSName(databaseID), password), nil
}

// KickDatabases 唤醒 Database 收敛环（API 受理面消费：创建/删除后立即
// 驱动）。
func (e *Engine) KickDatabases() { e.databaseLoop.Kick() }

// databaseStep 是 Database 收敛环的单次推进（单写者；逐行收敛，错误逐行
// 记日志不阻断）。
func (d *databases) databaseStep(ctx context.Context) {
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
func (d *databases) reconcileDatabase(ctx context.Context, row *database.Database) {
	// 恢复挂起中的预置卷形态（redis）：载体首启前不 Ensure——空卷首启即
	// 数据丢失，预置完成由 backup 环清位后收敛（ADR-0039 决策 6；探测只
	// 消费 Mode，密码传占位值过渲染闸）。
	if row.RestoreFromBackup != "" {
		if tpl, ok := dbtemplate.For(row.Engine); ok {
			if spec, err := tpl.Restore(DatabaseDNSName(row.ID), "probe"); err == nil && spec.Mode == dbtemplate.RestorePreseed {
				return
			}
		}
	}
	// 维护互斥读半边（ADR-0046）：Ensure 族与网络重建的串行化锚；锁等待
	// 不占步预算（排队语义）。单步带界（staging 实证：无界的 docker API
	// hang 卡死单写者循环）。
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := d.boundedStep(ctx)
	defer cancel()

	tpl, ok := dbtemplate.For(row.Engine)
	if !ok {
		// 受理位已校验值域；此处防御模板下线后的存量行（诚实记日志，
		// 载体保持现状不受扰）。
		d.log.Error("database reconcile: engine left the template registry", "database", row.ID, "engine", row.Engine)
		return
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		d.log.Error("database reconcile: resolve project", "database", row.ID, "err", err)
		return
	}
	spec := databaseSpecFromRow(row, tpl)
	if err := specir.ValidateDatabase(spec); err != nil {
		d.log.Error("database reconcile: invalid spec", "database", row.ID, "err", err)
		return
	}
	networks, egress := d.projectNetworkFacts(stepCtx, row.ProjectID)
	w, ns, err := ProjectDatabase(spec, team, row.Name, networks, egress, tpl)
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
	sig := managedFingerprint([]capability.Workload{w}) + "\x00" + ns.String() + "\x00" + credentialFingerprint(sec.Ciphertext)
	now := d.clock.Now()
	if memo, fresh := d.ensureFresh(d.ensure, row.ID, sig, now); fresh {
		d.advanceDatabaseStatus(stepCtx, row, memo.gen)
		return
	}
	materials, err := d.databaseMaterials(stepCtx, row, tpl, sec)
	if err != nil {
		// 凭证面失败 = 不 Ensure（载体保持现状），下一拍重试——静默拆载体
		// 比收敛失败更糟。
		d.log.Error("database reconcile: resolve credentials", "database", row.ID, "err", err)
		return
	}
	ws := []capability.Workload{w}
	// 卷行幂等补建（卷名 = 数据库名的确定性公式，ADR-0029 决策 5 的挂靠
	// 面）——收敛环拥有数据库收敛的全部所有权，卷行缺失（人工误删等）
	// 在此自愈，不依赖 API 侧时序约定。
	if err := d.ensureDatabaseVolume(stepCtx, row); err != nil {
		d.log.Error("database reconcile: ensure volume", "database", row.ID, "err", err)
		return
	}
	// 卷钉住既有面：无显式钉住的卷首挂锚定，钉住合并进 Placement。
	if err := d.pinVolumes(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("database reconcile: pin volumes", "database", row.ID, "err", err)
		return
	}
	if err := d.applyVolumePinning(stepCtx, ws, row.ProjectID); err != nil {
		d.log.Error("database reconcile: volume pinning merge", "database", row.ID, "err", err)
		return
	}
	gen, err := d.repos.Databases.EnsureGeneration(stepCtx, row.ID, managedFingerprint(ws))
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
	d.ensureRemember(d.ensure, row.ID, ensureMemo{sig: sig, gen: uint64(wireGen), at: now})
	// 归属/期望登记（观测路由 + 稳态看门狗；driftScan 对 database/ 前缀
	// 跳过 spec 对照——非 App 行键）。P4 wave-2a 经观测窄接口端口，卫星
	// 不直写 obs/expect 内部。
	d.recordOwners(uint64(wireGen), []capability.Workload{w}, func(capability.Workload) workloadOwner {
		return databaseOwner(row.ID)
	})
	d.recordExpected(databaseOwner(row.ID), uint64(wireGen))
	d.advanceDatabaseStatus(stepCtx, row, uint64(wireGen))
}

// advanceDatabaseStatus 从观测缓存推进行状态（gen 匹配才采信——旧 gen 的
// 迟到观测不翻状态）。gen 是载体实际在 wire 上的代次（回执 wire-gen，与
// 归属登记同律 R13-L3——call-gen 在内容等价 skip 下恒不等于观测）。Ensure
// 成功拍与签名短路拍共用（短路拍 Ensure 不发，状态收敛不停——观测经
// Watch 流独立到达）。
func (d *databases) advanceDatabaseStatus(ctx context.Context, row *database.Database, gen uint64) {
	// 观测缓存键 = 投影 Workload ID = 数据库行 ID（ProjectDatabase 铸造式；
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

// observationOf 读单 Workload 的最新观测（观测缓存拷贝）：P4 wave-2a 起
// 经观测窄接口端口消费（bind e.obs.observationOf 域方法值），原 *Engine
// 包装层退役——Database 域是唯一消费面。

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
// 串 → 密码回读 → 模板渲染（postgres 密码文件 / redis 配置文件）。密码
// 不进 env/label/argv（ADR-0014 材料纪律；值经载体按值指纹命名随 spec
// diff 分发——轮换 Secret 即滚动替换，zot 附录 B 同款机制）。sec 由调用方
// 预读传入（短路签名的同一次读取——不做二次点查）。
func (d *databases) databaseMaterials(ctx context.Context, row *database.Database, tpl dbtemplate.Template, sec *secret.Secret) (capability.Materials, error) {
	if d.cipher == nil {
		return capability.Materials{}, fmt.Errorf("database credentials require the master key (data root keys/ missing)")
	}
	plain, err := d.cipher.Open(sec.Ciphertext)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := dbtemplate.PasswordFromURL(string(plain))
	if err != nil {
		return capability.Materials{}, fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	files, err := tpl.Materials(password)
	if err != nil {
		// 渲染面字符集闸（dbtemplate.validatePassword）：fail-closed，载体
		// 保持现状不受扰。
		return capability.Materials{}, fmt.Errorf("render materials for engine %q: %w", row.Engine, err)
	}
	return capability.Materials{SecretFiles: files}, nil
}

// egressNetworkMap 返回 Project 活跃网络的 egress:none 名集（投影期的
// 网络事实面——k3s Provider 铸载体级 deny 的数据源，ADR-0052 决策 6；
// 查询失败如实记日志返回空集：隔离是收敛不变式，下一拍重放自愈）。
func (d *databases) egressNetworkMap(ctx context.Context, projectID string) map[string]bool {
	_, egress := d.projectNetworkFacts(ctx, projectID)
	return egress
}

// databaseSpecFromRow 由行 + 模板组装 DatabaseSpec（IR 单真源：投影输入
// 恒经 spec 形态，叶子校验可用）。
func databaseSpecFromRow(row *database.Database, tpl dbtemplate.Template) *specv1.DatabaseSpec {
	return &specv1.DatabaseSpec{
		SchemaVersion:  specir.SchemaVersion,
		Database:       &specv1.DatabaseRef{Id: row.ID, Project: row.ProjectID},
		Engine:         row.Engine,
		Version:        tpl.Meta().Version,
		CredentialsRef: row.CredentialsRef,
	}
}

// TeardownDatabase 收口拆除（ADR-0029 决策 8）：Runtime.Remove（幂等拆域
// 内全部载体）+ 清归属/期望/观测缓存。tombstone 与事件由 API 受理位在
// 后续事务落（先变更后留痕，ADR-0023 同款序）。单库删除与项目删除级联
// 共用本口（级联语义见 ADR-0029 追记 2026-10-05）。
func (d *databases) TeardownDatabase(ctx context.Context, id string) error {
	row, err := d.repos.Databases.Get(ctx, id)
	if err != nil {
		return err // ErrNotFound → API 404（对齐 TeardownApp 口径）
	}
	team, err := d.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, Database: row.ID}
	// 维护互斥读半边（项目删除级联批，2026-10-05）：Remove 是载体写动词，
	// 与 Ensure 族同面——网络重建（写半边）的 detach→rm→create→attach 全
	// 序期间不得插入拆载体（半拆网与半拆库交错会把重建的 re-attach 面对
	// 已逝归属）。锁等待不占步预算（排队语义，lockMaintenance 约定：读锁
	// 在带界 ctx 派生之前获取）。
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（B15-1，批 3 判定的反转）：API 请求路径的 Remove 挂死会
	// 卡住 API 调用本身（与收敛环卡死同害）。带 ManagedStepTimeout 硬上限，
	// 超时如实上抛（API 404/冲突语义不变，行保持可重试收口）。
	rctx, rcancel := d.boundedStep(ctx)
	removeErr := d.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("database teardown: %w", removeErr)
	}
	// 收口清理经观测窄接口端口（forgetObservation/forgetExpected），卫星
	// 不直写 obs/expect 内部。
	d.forgetObservation(row.ID)
	d.forgetExpected(databaseOwner(row.ID))
	d.ensureForget(d.ensure, row.ID) // 签名随域收口作废（同 ID 永不复用，防御性清理）
	return nil
}
