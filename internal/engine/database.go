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
	"errors"
	"fmt"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
	"github.com/oklog/ulid/v2"
)

// dbCredentialSecretPrefix 是数据库凭证 Secret 的命名约定（database:<name>；
// registry:<host> 先例。值 = 完整连接 URL，ADR-0029 决策 6）。
const dbCredentialSecretPrefix = "database:"

// DBCredentialSecretName 铸凭证 Secret 名（API 创建面与投影共用的单源
// 公式）。
func DBCredentialSecretName(databaseName string) string {
	return dbCredentialSecretPrefix + databaseName
}

// IsDatabaseCredentialSecret 报告 Secret 名是否落在数据库凭证保留前缀
// （API PutSecret 的受理守卫消费：用户覆写会破坏连接串单真源——值必须
// 恒为完整连接 URL，ADR-0029 决策 6）。
func IsDatabaseCredentialSecret(name string) bool {
	return strings.HasPrefix(name, dbCredentialSecretPrefix)
}

// DatabaseDNSName 铸 per-Database 稳定 DNS 名（engine 铸名公式真源，
// TaskDNSName 先例；Provider 经 Addressing 声明映射为自己的原语）。模板
// 子包不拥有铸名——ConnURL 收 host 注入（dbtemplate 包注释）。
func DatabaseDNSName(databaseID string) string {
	return "db-" + strings.ToLower(databaseID)
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
func (e *Engine) databaseStep(ctx context.Context) {
	rows, err := e.databases.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("database step: list", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		e.reconcileDatabase(ctx, &rows[i])
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
func (e *Engine) reconcileDatabase(ctx context.Context, row *dbrepo.Database) {
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
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
	defer cancel()

	tpl, ok := dbtemplate.For(row.Engine)
	if !ok {
		// 受理位已校验值域；此处防御模板下线后的存量行（诚实记日志，
		// 载体保持现状不受扰）。
		e.log.Error("database reconcile: engine left the template registry", "database", row.ID, "engine", row.Engine)
		return
	}
	team, err := e.projectTeam(ctx, row.ProjectID)
	if err != nil {
		e.log.Error("database reconcile: resolve project", "database", row.ID, "err", err)
		return
	}
	spec := databaseSpecFromRow(row, tpl)
	if err := specir.ValidateDatabase(spec); err != nil {
		e.log.Error("database reconcile: invalid spec", "database", row.ID, "err", err)
		return
	}
	networks := e.projectNetworkNames(stepCtx, row.ProjectID)
	w, ns, err := ProjectDatabase(spec, team, row.Name, networks, tpl)
	if err != nil {
		e.log.Error("database reconcile: project spec", "database", row.ID, "err", err)
		return
	}
	// 凭证 Secret 行预读（不解密）：短路签名的材料面 + 下发段的解密输入
	// 单源。读失败与既有材料失败路径同形（不 Ensure，载体保持现状）。
	sec, err := e.secrets.GetByName(stepCtx, e.db.Runner(), row.ProjectID, row.CredentialsRef)
	if err != nil {
		e.log.Error("database reconcile: resolve credentials", "database", row.ID,
			"err", fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err))
		return
	}
	sig := managedFingerprint([]capability.Workload{w}) + "\x00" + ns.String() + "\x00" + credentialFingerprint(sec.Ciphertext)
	now := e.clock.Now()
	if memo, fresh := e.ensureFresh(e.database.ensure, row.ID, sig, now); fresh {
		e.advanceDatabaseStatus(stepCtx, row, memo.gen)
		return
	}
	materials, err := e.databaseMaterials(stepCtx, row, tpl, sec)
	if err != nil {
		// 凭证面失败 = 不 Ensure（载体保持现状），下一拍重试——静默拆载体
		// 比收敛失败更糟。
		e.log.Error("database reconcile: resolve credentials", "database", row.ID, "err", err)
		return
	}
	ws := []capability.Workload{w}
	// 卷行幂等补建（卷名 = 数据库名的确定性公式，ADR-0029 决策 5 的挂靠
	// 面）——收敛环拥有数据库收敛的全部所有权，卷行缺失（人工误删等）
	// 在此自愈，不依赖 API 侧时序约定。
	if err := e.ensureDatabaseVolume(stepCtx, row); err != nil {
		e.log.Error("database reconcile: ensure volume", "database", row.ID, "err", err)
		return
	}
	// 卷钉住既有面：无显式钉住的卷首挂锚定，钉住合并进 Placement。
	if err := e.pinVolumes(stepCtx, ws, row.ProjectID); err != nil {
		e.log.Error("database reconcile: pin volumes", "database", row.ID, "err", err)
		return
	}
	if err := e.applyVolumePinning(stepCtx, ws, row.ProjectID); err != nil {
		e.log.Error("database reconcile: volume pinning merge", "database", row.ID, "err", err)
		return
	}
	gen, err := e.databases.EnsureGeneration(stepCtx, e.db.Runner(), row.ID, managedFingerprint(ws))
	if err != nil {
		e.log.Error("database reconcile: generation", "database", row.ID, "err", err)
		return
	}
	if err := e.runtime.Ensure(stepCtx, ns, ws, capability.Generation(gen), materials); err != nil {
		e.log.Error("database reconcile: ensure", "database", row.ID, "generation", gen, "err", err)
		e.ensureForget(e.database.ensure, row.ID) // 失败清签名：下拍重试
		return
	}
	e.ensureRemember(e.database.ensure, row.ID, ensureMemo{sig: sig, gen: gen, at: now})
	// 归属/期望登记（观测路由 + 稳态看门狗；driftScan 对 database/ 前缀
	// 跳过 spec 对照——非 App 行键）。
	e.obs.recordOwners(gen, []capability.Workload{w}, func(capability.Workload) workloadOwner {
		return databaseOwner(row.ID)
	})
	e.expect.mu.Lock()
	e.expect.expected[databaseOwner(row.ID)] = gen
	e.expect.mu.Unlock()
	e.advanceDatabaseStatus(stepCtx, row, gen)
}

// advanceDatabaseStatus 从观测缓存推进行状态（gen 匹配才采信——旧 gen 的
// 迟到观测不翻状态）。Ensure 成功拍与签名短路拍共用（短路拍 Ensure 不发，
// 状态收敛不停——观测经 Watch 流独立到达）。
func (e *Engine) advanceDatabaseStatus(ctx context.Context, row *dbrepo.Database, gen uint64) {
	// 观测缓存键 = 投影 Workload ID = 数据库行 ID（ProjectDatabase 铸造式；
	// database_test 以行 ID 注入观测即依赖此恒等）。
	ev, seen := e.observationOf(row.ID)
	if status, ok := dbStatusFromObservation(ev, gen); ok && seen && status != row.Status {
		if err := e.databases.SetStatus(ctx, e.db.Runner(), row.ID, status); err != nil {
			e.log.Error("database reconcile: set status", "database", row.ID, "err", err)
		}
	}
}

// credentialFingerprint 是凭证密文的轻量摘要（短路签名的材料面；sha256
// 截断——防碰撞足够，密文不可逆）。
func credentialFingerprint(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	return hex.EncodeToString(sum[:8])
}

// observationOf 读单 Workload 的最新观测（观测缓存拷贝）。
func (e *Engine) observationOf(workloadID string) (capability.WorkloadEvent, bool) {
	return e.obs.observationOf(workloadID)
}

// dbStatusFromObservation 把 Workload 观测映射为 Database 状态（长运行
// 面：一次性终态不适用；gen 不匹配返回 ok=false）。
func dbStatusFromObservation(ev capability.WorkloadEvent, gen uint64) (string, bool) {
	if uint64(ev.Generation) != gen {
		return "", false
	}
	switch ev.State {
	case capability.WorkloadRunning:
		return dbrepo.StatusRunning, true
	case capability.WorkloadPending:
		return dbrepo.StatusPending, true
	case capability.WorkloadDegraded:
		return dbrepo.StatusDegraded, true
	case capability.WorkloadStopped:
		return dbrepo.StatusStopped, true
	default:
		return "", false // completed/failed：长运行库不落在一次性终态
	}
}

// databaseMaterials 装配 DB Workload 的凭证材料：解密凭证 Secret → 连接
// 串 → 密码回读 → 模板渲染（postgres 密码文件 / redis 配置文件）。密码
// 不进 env/label/argv（ADR-0014 材料纪律；值经载体按值指纹命名随 spec
// diff 分发——轮换 Secret 即滚动替换，zot 附录 B 同款机制）。sec 由调用方
// 预读传入（短路签名的同一次读取——不做二次点查）。
func (e *Engine) databaseMaterials(ctx context.Context, row *dbrepo.Database, tpl dbtemplate.Template, sec *secret.Secret) (capability.Materials, error) {
	if e.cipher == nil {
		return capability.Materials{}, fmt.Errorf("database credentials require the master key (data root keys/ missing)")
	}
	plain, err := e.cipher.Open(sec.Ciphertext)
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

// ensureDatabaseVolume 幂等补建挂靠卷行（名 = 数据库名；NotFound 即建，
// 其余错误上抛）。预创建的同名卷被复用（用户可借 CreateVolume 预先钉
// 住节点——数据库收敛尊重既有 Placement）。
func (e *Engine) ensureDatabaseVolume(ctx context.Context, row *dbrepo.Database) error {
	if _, err := e.volumes.GetByName(ctx, e.db.Runner(), row.ProjectID, row.Name); err == nil {
		return nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return e.volumes.Create(ctx, e.db.Runner(), &volume.Volume{
		ID: ulid.Make().String(), ProjectID: row.ProjectID, Name: row.Name,
	})
}

// projectNetworkNames 返回 Project 的活跃网络名列表（数据库挂全部活跃
// 项目网——受管 Proxy 同款语义、限本项目，ADR-0029 决策 5）。
func (e *Engine) projectNetworkNames(ctx context.Context, projectID string) []string {
	rows, err := e.networks.ListByProject(ctx, e.db.Runner(), projectID)
	if err != nil {
		e.log.Error("database reconcile: list project networks", "project", projectID, "err", err)
		return nil
	}
	names := make([]string, 0, len(rows))
	for _, n := range rows {
		names = append(names, n.Name)
	}
	return names
}

// databaseSpecFromRow 由行 + 模板组装 DatabaseSpec（IR 单真源：投影输入
// 恒经 spec 形态，叶子校验可用）。
func databaseSpecFromRow(row *dbrepo.Database, tpl dbtemplate.Template) *specv1.DatabaseSpec {
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
func (e *Engine) TeardownDatabase(ctx context.Context, id string) error {
	row, err := e.databases.Get(ctx, e.db.Runner(), id)
	if err != nil {
		return err // ErrNotFound → API 404（对齐 TeardownApp 口径）
	}
	team, err := e.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, Database: row.ID}
	// 维护互斥读半边（项目删除级联批，2026-10-05）：Remove 是载体写动词，
	// 与 Ensure 族同面——网络重建（写半边）的 detach→rm→create→attach 全
	// 序期间不得插入拆载体（半拆网与半拆库交错会把重建的 re-attach 面对
	// 已逝归属）。锁等待不占步预算（排队语义，lockMaintenance 约定：读锁
	// 在带界 ctx 派生之前获取）。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（B15-1，批 3 判定的反转）：API 请求路径的 Remove 挂死会
	// 卡住 API 调用本身（与收敛环卡死同害）。带 ManagedStepTimeout 硬上限，
	// 超时如实上抛（API 404/冲突语义不变，行保持可重试收口）。
	rctx, rcancel := e.boundedStep(ctx)
	removeErr := e.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("database teardown: %w", removeErr)
	}
	e.obs.forget(row.ID)
	e.expect.mu.Lock()
	delete(e.expect.expected, databaseOwner(row.ID))
	e.expect.mu.Unlock()
	e.ensureForget(e.database.ensure, row.ID) // 签名随域收口作废（同 ID 永不复用，防御性清理）
	return nil
}
