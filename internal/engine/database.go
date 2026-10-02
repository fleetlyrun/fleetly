package engine

// Database 域收敛环（ADR-0029 决策 3）：用户域受管形态的第三条部署轨——
// 不进 Deployment 状态机（App 键控）、不走 fleetly/system 受管域（数据库
// 挂项目网供 App 连）。活跃行 → DatabaseSpec（IR 单真源）→ Workload 投影
// → Runtime.Ensure（唯一写动词，幂等重放）；per-Database Generation 落行
// 持久（重启安全：指纹未变则同号重放，载体不滚）。

import (
	"context"
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
	"github.com/oklog/ulid/v2"
)

// databaseDomainKeyPrefix 是 Database 域在归属/期望缓存中的键前缀
// （database/<id>——非 App 行键；driftScan 的 spec 对照面据此跳过，稳态
// 看门狗面保留，managed 域键同款）。
const databaseDomainKeyPrefix = "database/"

// dbCredentialSecretPrefix 是数据库凭证 Secret 的命名约定（database:<name>；
// registry:<host> 先例。值 = 完整连接 URL，ADR-0029 决策 6）。
const dbCredentialSecretPrefix = "database:"

// DBCredentialSecretName 铸凭证 Secret 名（API 创建面与投影共用的单源
// 公式）。
func DBCredentialSecretName(databaseName string) string {
	return dbCredentialSecretPrefix + databaseName
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
func (e *Engine) reconcileDatabase(ctx context.Context, row *dbrepo.Database) {
	// 单步带界（staging 实证：无界的 docker API hang 卡死单写者循环）。
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
	defer cancel()

	tpl, ok := dbTemplateFor(row.Engine)
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
	materials, err := e.databaseMaterials(stepCtx, row, tpl)
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
		e.log.Error("database reconcile: apply volume pinning", "database", row.ID, "err", err)
		return
	}
	gen, err := e.databases.EnsureGeneration(stepCtx, e.db.Runner(), row.ID, managedFingerprint(ws))
	if err != nil {
		e.log.Error("database reconcile: generation", "database", row.ID, "err", err)
		return
	}
	if err := e.runtime.Ensure(stepCtx, ns, ws, capability.Generation(gen), materials); err != nil {
		e.log.Error("database reconcile: ensure", "database", row.ID, "generation", gen, "err", err)
		return
	}
	// 归属/期望登记（观测路由 + 稳态看门狗；driftScan 对 database/ 前缀
	// 跳过 spec 对照——非 App 行键）。
	e.obsMu.Lock()
	e.workloadApp[w.ID] = databaseDomainKeyPrefix + row.ID
	e.ensuredGen[w.ID] = gen
	e.obsMu.Unlock()
	e.expectMu.Lock()
	e.expected[databaseDomainKeyPrefix+row.ID] = gen
	e.expectMu.Unlock()

	// 状态推进（观测缓存，gen 匹配才采信——旧 gen 的迟到观测不翻状态）。
	ev, seen := e.observationOf(w.ID)
	if status, ok := dbStatusFromObservation(ev, gen); ok && seen && status != row.Status {
		if err := e.databases.SetStatus(stepCtx, e.db.Runner(), row.ID, status); err != nil {
			e.log.Error("database reconcile: set status", "database", row.ID, "err", err)
		}
	}
}

// observationOf 读单 Workload 的最新观测（观测缓存拷贝）。
func (e *Engine) observationOf(workloadID string) (capability.WorkloadEvent, bool) {
	e.obsMu.RLock()
	defer e.obsMu.RUnlock()
	ev, ok := e.observations[workloadID]
	return ev, ok
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
// diff 分发——轮换 Secret 即滚动替换，zot 附录 B 同款机制）。
func (e *Engine) databaseMaterials(ctx context.Context, row *dbrepo.Database, tpl dbTemplate) (capability.Materials, error) {
	if e.cipher == nil {
		return capability.Materials{}, fmt.Errorf("database credentials require the master key (data root keys/ missing)")
	}
	sec, err := e.secrets.GetByName(ctx, e.db.Runner(), row.ProjectID, row.CredentialsRef)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err)
	}
	plain, err := e.cipher.Open(sec.Ciphertext)
	if err != nil {
		return capability.Materials{}, fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := dbPasswordFromURL(string(plain))
	if err != nil {
		return capability.Materials{}, fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	return capability.Materials{SecretFiles: tpl.materialsRender(password)}, nil
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
// 项目网——受管 Edge 同款语义、限本项目，ADR-0029 决策 5）。
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
func databaseSpecFromRow(row *dbrepo.Database, tpl dbTemplate) *specv1.DatabaseSpec {
	return &specv1.DatabaseSpec{
		SchemaVersion:  specir.SchemaVersion,
		Database:       &specv1.DatabaseRef{Id: row.ID, Project: row.ProjectID},
		Engine:         row.Engine,
		Version:        tpl.version,
		CredentialsRef: row.CredentialsRef,
	}
}

// TeardownDatabase 收口拆除（ADR-0029 决策 8）：Runtime.Remove（幂等拆域
// 内全部载体）+ 清归属/期望/观测缓存。tombstone 与事件由 API 受理位在
// 后续事务落（先变更后留痕，ADR-0023 同款序）。
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
	if err := e.runtime.Remove(ctx, ns); err != nil {
		return fmt.Errorf("database teardown: %w", err)
	}
	e.obsMu.Lock()
	delete(e.observations, row.ID)
	delete(e.workloadApp, row.ID)
	delete(e.ensuredGen, row.ID)
	e.obsMu.Unlock()
	e.expectMu.Lock()
	delete(e.expected, databaseDomainKeyPrefix+row.ID)
	e.expectMu.Unlock()
	return nil
}
