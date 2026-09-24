package engine

// 库引用解析与注入（E4 托管数据库，managed-databases §2.4/§2.5，D-DB-4/
// D-DB-5/D-DB-3）：compose 服务 label fleetly.databases（逗号分隔实例名）
// → 发布引擎 preparing 期解析并落地三件事——
//
//  1. 存在性哨兵：引用实例必须存在且 state ∉ {deleting, deleted}，否则
//     E_DB_NOT_FOUND（404，附候选清单）；**不要求 ready**（建库与引用部署
//     可并行——bootstrap 顺序是用户事务；未就绪 → 计划警告
//     W_DB_REFERENCE_NOT_READY，不阻塞，D-DB-5）。
//  2. env 前缀冲突哨兵：同 app 引用的多实例前缀撞名（pg-prod vs pg_prod
//     均 → FLEETLY_DB_PG_PROD）→ E_DB_ENV_PREFIX_CONFLICT（422，点名的
//     冲突双方）。
//  3. 物化（引用登记 + system env）：单事务内「全清 + 替换」db_references
//     倒排（可从各 app 当前 compose 重建的派生登记——compose 是唯一期望态
//     真源），并按 dbtemplate.ConnectionVars 把连接信息 upsert 为引用 app
//     的 env_vars 行（source=system，密文落库）——走既有 upsert → pending
//     → 本次部署合并消费（platformEnvForMerge 读全量行）→ 成功后提升的
//     完整链路；label 移除后失配实例的物化行同事务删除。
//
// 网络牵线（§2.4 时序行 3）：带 label 的服务 spec 附加每实例共享网络
// fleetly-db-<name>-net（**无别名**——引用方以 Swarm 服务名可达；库侧别
// 名 = 实例名由模板渲染层供给）。applyDesired 的既有「期望 spec 引用的
// 平台侧网络一并 NetworkEnsure」循环（rustfs 先例）自然覆盖新网络——幂等。
//
// 明文纪律：凭据明文只存活于 解密 → ConnectionVars → box.Encrypt 的内存
// 投影路径与密文存储；不进日志/事件/审计/错误（state-model §2.9 同族）。
//
// 生效语义：物化行 upsert 即回 pending（SetAppEnv 既有语义），值未变的重
// 物化同样回 pending——「随本次部署合并消费、成功后提升」，与用户 env set
// 的生效链路同源（S16-C4）。

import (
	"context"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// W_DB_REFERENCE_NOT_READY 是引用未就绪的计划警告码（plan-warning 字面量，
// 不进 E_ 注册表——W_ENV_PLATFORM_OVERRIDE 同款先例：非阻断披露面）。
const W_DB_REFERENCE_NOT_READY = "W_DB_REFERENCE_NOT_READY"

// DatabaseTemplatePort 是引擎对库模板层的消费端口（dbtemplate → engine 的
// 单向桥）。分层事实：internal/dbtemplate 在 engine **之上**（模板渲染产物
// = engine.ServiceSpec 投影，渲染器 import engine）——引擎不能反向 import，
// 只能端口注入（ImageChecker/PlacementResolver 同款端口纪律），适配器在
// 装配层（internal/runtime）落。实现在 dbtemplate.EnvPrefix /
// dbtemplate.ConnectionVars（前缀与连接串键值的唯一定义点，§2.5）。
type DatabaseTemplatePort interface {
	// EnvPrefix 返回引用方物化 env 前缀 FLEETLY_DB_<NAME>（实例名 '-'→'_'
	// 大写形）。
	EnvPrefix(instance string) string
	// ConnectionVars 渲染引用方物化 env 键值集（明文；§2.5 键集表）。
	ConnectionVars(templateID, instance, password string) (map[string]string, error)
}

// materializedEnvSuffixes 是物化键的尾缀并集（§2.5 键集表 PG 全集 ∪ Redis
// 子集）。用途仅限**清理**：label 移除后按「前缀 + 尾缀并集」精确删除失
// 配实例的物化行——FLEETLY_ 前缀是平台保留名字空间（E_ENV_KEY_RESERVED
// 守卫封死用户写），该形态的行只可能由本物化路径产出。不进 dbtemplate
//（渲染器只管键集的产出口；清理面的并集口径与 §2.5 键集表逐字对应）。
var materializedEnvSuffixes = []string{"URL", "HOST", "PORT", "USER", "PASSWORD", "DATABASE"}

// resolveDatabaseReferences 解析本次发布的库引用面（preparing 现读库清单
// 与凭据，不缓存长驻）。返回：
//   - netsByService：带 label 服务 → 附加的库共享网络名列表（按键名字典序
//     ——desired-hash 确定性）；无 label 服务零附加，app 无 label 且无既有
//     引用时为 nil（零写入——从未引用过库的部署零额外副作用）；
//   - warnings：引用实例未就绪的 W_DB_REFERENCE_NOT_READY（不阻塞）；
//   - err：E_DB_NOT_FOUND / E_DB_ENV_PREFIX_CONFLICT（规划期诚实拒绝）。
//
// 物化副作用：引用实例全部通过哨兵后，在单事务内维护 db_references 与
// env_vars system 物化行（含失配行删除）；app 曾引用而本次声明全无（label
// 全部移除）→ 级联清理拍。
func (e *Engine) resolveDatabaseReferences(ctx context.Context, appID, appName string, spec *compose.Spec) (netsByService map[string][]string, warnings []compose.Warning, err error) {
	// 既有引用先行读取（全清 + 替换差集与「label 全部移除」判定都要用；
	// 一次索引查询——从未引用过库的 app 后续零额外读取）。
	existingRefs, err := e.store.ListDatabaseReferencesByApp(ctx, appID)
	if err != nil {
		return nil, nil, errorf("E_RUNTIME_UNAVAILABLE", "failed to read database references: %v", err)
	}

	labeled := 0
	for i := range spec.Services {
		if len(spec.Services[i].Databases) > 0 {
			labeled++
		}
	}
	if labeled == 0 {
		// 无 label：无既有引用 → 零写入（与 resolveS3Injection 同形的零成
		// 本缺省）；有既有引用 = label 全部移除的部署 → 级联清理（全清 +
		// 失配物化行删除，§2.4「label 移除并部署 = 行删除」）。
		if len(existingRefs) == 0 {
			return nil, nil, nil
		}
		if err := e.store.InTx(ctx, func(tx *state.Tx) error {
			return clearDatabaseReferences(ctx, tx, appID, existingRefs)
		}); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	// 模板端口未接线（装配缺失）：带 label 的部署规划期显式失败——静默
	// 跳过物化会产出「引用成功但连接信息缺失」的悬案。
	if e.dbTemplate == nil {
		return nil, nil, errorf("E_RUNTIME_UNAVAILABLE",
			"service(s) declare label %q but the database template port is not wired (assembly bug: the engine requires a DatabaseTemplatePort for database references)", compose.LabelDatabases)
	}

	instances, err := e.store.ListDatabaseInstances(ctx)
	if err != nil {
		return nil, nil, errorf("E_RUNTIME_UNAVAILABLE", "failed to read database instances: %v", err)
	}
	byName := make(map[string]state.DatabaseInstance, len(instances))
	candidates := make([]string, 0, len(instances))
	for _, in := range instances {
		byName[in.Name] = in
		candidates = append(candidates, in.Name)
	}

	// ── 逐服务解析：存在性哨兵 + 未就绪警告 + 网络牵线（只读判定先行，
	// 哨兵失败不动任何状态）──
	type resolvedRef struct {
		service  string
		instance state.DatabaseInstance
	}
	var refs []resolvedRef
	newPrefixes := map[string]string{} // env_prefix → 实例名（冲突哨兵输入）
	netsByService = make(map[string][]string, labeled)
	for i := range spec.Services {
		svc := &spec.Services[i]
		if len(svc.Databases) == 0 {
			continue
		}
		nets := make([]string, 0, len(svc.Databases))
		for _, name := range svc.Databases {
			inst, ok := byName[name]
			if !ok || inst.State == state.DatabaseDeleting || inst.State == state.DatabaseDeleted {
				return nil, nil, databaseNotFound(name, svc.Name, candidates)
			}
			// D-DB-5：引用前哨只查存在性不查就绪——未就绪（provisioning/
			// failed/degraded/paused）如实警告，不阻塞部署。
			if inst.State != state.DatabaseReady {
				warnings = append(warnings, compose.Warning{
					Code:    W_DB_REFERENCE_NOT_READY,
					Service: svc.Name,
					Message: "database instance " + inst.Name + " referenced by service " + svc.Name +
						" is in state " + string(inst.State) +
						": connections from this service will fail until the instance becomes ready (the reference is validated for existence only; the deployment proceeds)",
				})
			}
			// 前缀冲突哨兵（§2.4 时序行：同 app 两个前缀撞名 → 422 点名双方；
			// 跨 app 不判——前缀唯一性是 app 侧 env 键空间的问题）。同一实例
			// 被多服务引用合法（同名放行——冲突只判不同实例）。
			prefix := e.dbTemplate.EnvPrefix(inst.Name)
			if prev, dup := newPrefixes[prefix]; dup && prev != inst.Name {
				return nil, nil, apperr.New("E_DB_ENV_PREFIX_CONFLICT",
					"database instances %q and %q referenced by app %q derive the same env prefix %s (instance names map to env prefixes by uppercasing with '-' mapped to '_'; rename one of the instances or reference only one per colliding pair in this app)",
					prev, inst.Name, appName, prefix).
					WithContext("prefix", prefix).
					WithContext("instances", prev+","+inst.Name)
			}
			newPrefixes[prefix] = inst.Name
			// 库共享网络名随实例归属三段化（rbac-teams §4.3 库行：fleetly-
			// db-<team>-<prj>-<name>-net——slug 不可变，随行 join 装载）。
			net, nerr := naming.DBNetworkName(inst.TeamSlug, inst.ProjectSlug, inst.Name)
			if nerr != nil {
				return nil, nil, errorf("E_RUNTIME_UNAVAILABLE", "network naming failed for database instance %s: %v", inst.Name, nerr)
			}
			nets = append(nets, net)
			refs = append(refs, resolvedRef{service: svc.Name, instance: inst})
		}
		sort.Strings(nets)
		netsByService[svc.Name] = nets
	}

	// ── 物化：凭据解密（事务外——明文只进内存投影）+ 单事务引用登记与
	// env 行维护 ──
	type materialized struct {
		instance state.DatabaseInstance
		vars     map[string]string // 明文键值（ConnectionVars 产物，内存短命）
	}
	var mrows []materialized
	for _, r := range refs {
		seen := false
		for _, m := range mrows {
			if m.instance.ID == r.instance.ID {
				seen = true
				break
			}
		}
		if seen {
			continue // 跨服务去重：每实例一次解密与物化
		}
		plain, derr := e.box.Decrypt([]byte(r.instance.CredentialCipher))
		if derr != nil {
			return nil, nil, errorf("E_RUNTIME_UNAVAILABLE",
				"failed to decrypt credential of database instance %s (master key mismatch or corrupted ciphertext)", r.instance.Name)
		}
		vars, verr := e.dbTemplate.ConnectionVars(r.instance.Template, r.instance.Name, string(plain))
		if verr != nil {
			// 防御路径（理论不可达：模板在创建 API 已被守卫）。未知模板的
			// 用户面错误码映射在库 API 层（E_DB_TEMPLATE_UNSUPPORTED）——
			// 引擎侧只有行损坏可到此处，按运行时不可用处理。
			return nil, nil, errorf("E_RUNTIME_UNAVAILABLE",
				"failed to render connection vars for database instance %s: %v", r.instance.Name, verr)
		}
		mrows = append(mrows, materialized{instance: r.instance, vars: vars})
	}

	if err := e.store.InTx(ctx, func(tx *state.Tx) error {
		// 引用登记：全清 + 替换（可从 compose 重建的派生登记；label 移除 =
		// 行删除）。先取差集再清——失配实例的物化行删除需要旧登记的前缀面。
		removedPrefixes := map[string]bool{}
		for _, ref := range existingRefs {
			if _, still := newPrefixes[ref.EnvPrefix]; !still {
				removedPrefixes[ref.EnvPrefix] = true
			}
		}
		if _, err := tx.DeleteDatabaseReferencesForApp(ctx, appID); err != nil {
			return err
		}
		for _, r := range refs {
			if err := tx.UpsertDatabaseReference(ctx, state.DatabaseReference{
				DatabaseID: r.instance.ID,
				AppID:      appID,
				Service:    r.service,
				EnvPrefix:  e.dbTemplate.EnvPrefix(r.instance.Name),
			}); err != nil {
				return err
			}
		}
		// 物化行 upsert（source=system → pending，随本次部署合并消费）。
		for _, m := range mrows {
			for _, key := range sortedVarKeys(m.vars) {
				cipher, encErr := e.box.Encrypt([]byte(m.vars[key]))
				if encErr != nil {
					return errorf("E_RUNTIME_UNAVAILABLE", "failed to encrypt materialized env %s: %v", key, encErr)
				}
				if _, err := tx.SetAppEnv(ctx, appID, key, string(cipher), "system"); err != nil {
					return err
				}
			}
		}
		return deleteMaterializedRowsForPrefixes(ctx, tx, appID, removedPrefixes)
	}); err != nil {
		return nil, nil, err
	}
	return netsByService, warnings, nil
}

// clearDatabaseReferences 是「label 全部移除」部署的清理拍：全清引用行 +
// 全部既有前缀的物化行删除（与主路径的差集清理共用行级删除原语）。
func clearDatabaseReferences(ctx context.Context, tx *state.Tx, appID string, existing []state.DatabaseReference) error {
	if _, err := tx.DeleteDatabaseReferencesForApp(ctx, appID); err != nil {
		return err
	}
	prefixes := make(map[string]bool, len(existing))
	for _, ref := range existing {
		prefixes[ref.EnvPrefix] = true
	}
	return deleteMaterializedRowsForPrefixes(ctx, tx, appID, prefixes)
}

// deleteMaterializedRowsForPrefixes 删除给定前缀集的物化行（前缀 + 尾缀并
// 集精确匹配——FLEETLY_ 名字空间是平台保留区（E_ENV_KEY_RESERVED 守卫封死
// 用户写），该形态行只由物化路径产出）。幂等：无行时零删除。
func deleteMaterializedRowsForPrefixes(ctx context.Context, tx *state.Tx, appID string, prefixes map[string]bool) error {
	for prefix := range prefixes {
		for _, suffix := range materializedEnvSuffixes {
			if _, err := tx.DeleteAppEnv(ctx, appID, prefix+"_"+suffix); err != nil {
				return err
			}
		}
	}
	return nil
}

// databaseNotFound 构造 E_DB_NOT_FOUND（404；候选清单进 message 与 context
// ——「不存在」与「进入 deleting/deleted」共用同一码面，设计 §5.2）。
func databaseNotFound(name, service string, candidates []string) error {
	msg := "database instance " + name + " referenced by service " + service + " does not exist"
	if len(candidates) > 0 {
		msg += " (known instances: " + strings.Join(candidates, ", ") + ")"
	}
	return apperr.New("E_DB_NOT_FOUND",
		"%s: references are validated for existence at plan time; create the instance first or fix the %q label", msg, compose.LabelDatabases).
		WithContext("database", name).
		WithContext("service", service).
		WithContext("candidates", strings.Join(candidates, ","))
}

// sortedVarKeys 返回物化键值集的键字典序（upsert 顺序确定性）。
func sortedVarKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
