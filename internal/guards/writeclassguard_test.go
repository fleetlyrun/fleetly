package guards

// 写面立法执法面（ADR-0081 Batch B 落地）：附录完整分类表（27 ACTION +
// 53 DECLARATION + 74 READ = 154）是「未分类即红」的唯一清单，测试内单源
// 机械转写；READ 为余集按 §1 读前缀缺省归类不列。三从属断言把既有三张
// 守卫表挂上同一法源，防各自漂移：FrozenVerbs ⊆ DECLARATION∪ACTION（冻结
// 只拦写面）；idem.EnforcedMethods ⊆ DECLARATION∪豁免（幂等重放只挂声明
// 写面）；scope 单向断言（DECLARATION ⇒ WRITE/ADMIN，ACTION 的 READ-scope
// 经豁免面显式登记）。proto 零触碰：执法面是 Go 表，一方注解随配置面立项
// 另批（ADR-0081 决策 3）。
//
// H8 阶段 B（2026-10-19）新增只读 RPC QueryScopedMetrics → 154 方法
//（27 ACTION + 53 DECLARATION + 74 READ，仍为 READ 余集）；ADR-0081
// 附录的台账实数随 H8 批文档追记同批重编。

import (
	"sort"
	"testing"

	"github.com/lynx-go/grpcapi/authz"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/idem"
	"github.com/fleetlyrun/fleetly/internal/assembly"
)

// writeClassDeclarations 是 DECLARATION 桶（声明写，53 条）——经部署管线
// 或实体行落期望态，引擎负责收敛；未变声明重放 = 零扰动 no-op。ADR-0081
// 附录机械转写（Create/Put/Delete/Set/Revoke 前缀实体行写是缺省分类，故
// 不逐条带理由；非自明条目随行注记出处）。
var writeClassDeclarations = map[string]bool{
	// —— automation/v1（附录 A.1）：Task/Schedule 实体行落定/删除。
	"/fleetly.automation.v1.TasksService/CreateTask":         true,
	"/fleetly.automation.v1.TasksService/ScaleTask":          true, // desired_concurrency 落行（池期望态，补足环对照它）
	"/fleetly.automation.v1.TasksService/DeleteTask":         true,
	"/fleetly.automation.v1.SchedulesService/CreateSchedule": true,
	"/fleetly.automation.v1.SchedulesService/DeleteSchedule": true,

	// —— delivery/v1（附录 A.2/A.3/A.5）：部署管线声明写与实体行写。
	"/fleetly.delivery.v1.DeploymentsService/Deploy":            true, // 冻结 Revision → 管线收敛
	"/fleetly.delivery.v1.DeploymentsService/Rollback":          true, // 向旧 Revision 的声明性回归，经管线 Replay
	"/fleetly.delivery.v1.BuildsService/UploadSource":           true, // 上传产物落材料库（材料实体，供部署声明引用）
	"/fleetly.delivery.v1.HooksService/SetGitHook":              true,
	"/fleetly.delivery.v1.HooksService/DeleteGitHook":           true, // 解除通道（W-8）
	"/fleetly.delivery.v1.GitHubAppsService/SetGitHubApp":       true, // 接入材料行落定（受理即验证，ADR-0073）
	"/fleetly.delivery.v1.GitHubAppsService/DeleteGitHubApp":    true,
	"/fleetly.delivery.v1.TemplatesService/InstantiateTemplate": true, // 蓝图展开为实体声明集（App/Secret/Database/Route 声明）

	// —— identity/v1（附录 A.6）：身份实体行；identity 域状态即事实、
	// 无运行时基底可收敛（与 SetUserPassword 边界判据同源）。
	"/fleetly.identity.v1.UsersService/CreateUser":             true,
	"/fleetly.identity.v1.UsersService/SetUserPassword":        true, // 密码行落定即状态即事实（无收敛面接棒——与 Rotate 系判据分界）
	"/fleetly.identity.v1.UsersService/DeleteUser":             true,
	"/fleetly.identity.v1.TeamsService/CreateTeam":             true,
	"/fleetly.identity.v1.TeamsService/DeleteTeam":             true,
	"/fleetly.identity.v1.RolesService/CreateRole":             true,
	"/fleetly.identity.v1.RolesService/DeleteRole":             true,
	"/fleetly.identity.v1.TokensService/CreateToken":           true,
	"/fleetly.identity.v1.TokensService/RevokeToken":           true, // 吊销落行（状态即事实）
	"/fleetly.identity.v1.InvitationsService/CreateInvitation": true,

	// —— proxy/v1（附录 A.7）：Route 行落定/删除（行集 + 签名短路的收敛面输入）。
	"/fleetly.proxy.v1.RoutesService/CreateRoute": true,
	"/fleetly.proxy.v1.RoutesService/DeleteRoute": true,

	// —— runtime/v1（附录 A.9）：节点行（Cordon 落 schedulable 期望态，
	// 可对照、可收敛回真——与 DrainNode 的 ACTION 分界见附录边界案例）。
	"/fleetly.runtime.v1.NodesService/EnrollNode":   true, // 节点实体落地落行
	"/fleetly.runtime.v1.NodesService/CordonNode":   true,
	"/fleetly.runtime.v1.NodesService/UncordonNode": true, // 期望态翻回

	// —— structure/v1（附录 A.11）：平台实体行落定/删除。
	"/fleetly.structure.v1.ProjectsService/CreateProject":               true,
	"/fleetly.structure.v1.ProjectsService/DeleteProject":               true,
	"/fleetly.structure.v1.AppsService/CreateApp":                       true,
	"/fleetly.structure.v1.AppsService/DeleteApp":                       true,
	"/fleetly.structure.v1.SecretsService/PutSecret":                    true, // 值永不回显（ADR-0014）
	"/fleetly.structure.v1.SecretsService/DeleteSecret":                 true,
	"/fleetly.structure.v1.ConfigsService/PutConfig":                    true, // 版本化冻结新版本
	"/fleetly.structure.v1.SharedVariablesService/PutSharedVariable":    true,
	"/fleetly.structure.v1.SharedVariablesService/DeleteSharedVariable": true,
	"/fleetly.structure.v1.AppVariablesService/PutAppVariable":          true, // 三级合成顶层（ADR-0072）
	"/fleetly.structure.v1.AppVariablesService/DeleteAppVariable":       true,
	"/fleetly.structure.v1.VolumesService/CreateVolume":                 true,
	"/fleetly.structure.v1.VolumesService/DeleteVolume":                 true,
	"/fleetly.structure.v1.DatabasesService/CreateDatabase":             true,
	"/fleetly.structure.v1.DatabasesService/DeleteDatabase":             true,
	"/fleetly.structure.v1.NetworksService/CreateNetwork":               true,
	"/fleetly.structure.v1.NetworksService/DeclareNetworkPeer":          true, // peer 行声明（pending 行落定）
	"/fleetly.structure.v1.NetworksService/ApproveNetworkPeer":          true, // peer 行裁决落定
	"/fleetly.structure.v1.NetworksService/RevokeNetworkPeer":           true, // peer 行吊销落定

	// —— system/v1（附录 A.12）：治理态落行/解除（ADR-0017）。
	"/fleetly.system.v1.GovernanceService/SetChangeFreeze":  true,
	"/fleetly.system.v1.GovernanceService/LiftChangeFreeze": true,

	// —— telemetry/v1（附录 A.14）：告警通道/规则实体行落定/删除（ADR-0041）。
	"/fleetly.telemetry.v1.AlertingService/CreateNotificationChannel": true,
	"/fleetly.telemetry.v1.AlertingService/DeleteNotificationChannel": true,
	"/fleetly.telemetry.v1.AlertingService/CreateAlertRule":           true,
	"/fleetly.telemetry.v1.AlertingService/DeleteAlertRule":           true,
}

// writeClassActions 是 ACTION 桶（动作写，27 条）——无终态可持有的过渡/
// 仪式/修护动词，例外清单：每一入桶成员带一句理由（ADR-0081 §1；例外
// 不再命中即红，双向保鲜由 TestWriteClassCompleteClassification 执法）。
// 铸造短命凭证/会话族成组裁决（§5）显式分组。
var writeClassActions = map[string]string{
	// —— automation/v1（附录 A.1）：停止族与仪式动词。
	"/fleetly.automation.v1.TasksService/StopTask":            "stop instruction in a one-shot semantic domain; the row carries no 'stopped' desired state",
	"/fleetly.automation.v1.RunsService/StopRun":              "terminates one in-flight execution; the terminal state is owned by the run state machine",
	"/fleetly.automation.v1.TasksService/RenewTask":           "owner-lease renewal ritual; transient lease semantics (ADR-0012)",
	"/fleetly.automation.v1.SchedulesService/TriggerSchedule": "ritual verb: mints one immediate run on demand; the rule row is unchanged",

	// —— delivery/v1（附录 A.2/A.3/A.5）：过渡动词、仪式动词、修护动词。
	"/fleetly.delivery.v1.DeploymentsService/CancelDeployment":    "terminates an in-flight rollout; convergence ownership of residual carriers is adjudicated by ADR-0082",
	"/fleetly.delivery.v1.DeploymentsService/RestartProcess":      "ADR-0069 zero-lineage action face (--runtime escape hatch); touches no Spec/Revision",
	"/fleetly.delivery.v1.HooksService/RotateHookToken":           "Rotate family: mints replacement token material that reaches convergence via the material fingerprint",
	"/fleetly.delivery.v1.HooksService/RefreshGitHook":            "remediation verb: re-fetches hook configuration from GitHub to realign the registered state",
	"/fleetly.delivery.v1.HooksService/ReceiveWebhook":            "ritual verb: external event intake entrance (ADR-0009 pull-only)",
	"/fleetly.delivery.v1.GitHubAppsService/StartManifestFlow":    "manifest ritual trio: signs a redirect URL; zero rows written (ADR-0073 decision 8)",
	"/fleetly.delivery.v1.GitHubAppsService/ConsumeManifestCode":  "manifest ritual trio: exchanges a one-time code into the in-process flow store; zero rows written (ADR-0073 decision 8)",
	"/fleetly.delivery.v1.GitHubAppsService/CompleteManifestFlow": "manifest ritual trio: flow-closing row-writing step; the declaration face of the access material is SetGitHubApp (ADR-0073 decision 8)",
	"/fleetly.delivery.v1.TemplatesService/RefreshTemplates":      "upstream template catalog rebuild; not an entity-row write (ADR-0050)",

	// —— identity/v1（附录 A.6）：验证/兑换仪式。
	"/fleetly.identity.v1.UsersService/Login":                  "ritual verb: authentication mints a session credential; no platform desired state",
	"/fleetly.identity.v1.InvitationsService/AcceptInvitation": "ritual verb: one-shot redemption into a User; the redemption is the whole effect, nothing to hold",

	// —— 铸造短命凭证/会话族（ADR-0081 §5 成组裁决）：铸短命会话/一次性
	// 票据，非资源行（不可列表回读，台账 = 审计）。
	"/fleetly.runtime.v1.ExecService/CreateExecSession":     "mint family: mints a short-lived exec session, not a resource row (ADR-0049)",
	"/fleetly.runtime.v1.ExecService/StreamExecSession":     "mint family: the session's bidirectional transport body carrying stdin (ADR-0049)",
	"/fleetly.telemetry.v1.EventsService/IssueEventTicket":  "mint family: one-shot read-only SSE ticket (ADR-0026)",
	"/fleetly.structure.v1.DatabasesService/BrowseDatabase": "mint family: browse session admission mints a short-lived session (ADR-0051)",

	// —— runtime/v1（附录 A.9）：迁移语义归编排器的过渡过程（与 Cordon 的
	// DECLARATION 分界见附录边界案例）。
	"/fleetly.runtime.v1.NodesService/DrainNode": "eviction semantics owned by the orchestrator as a transient process; the node row carries no 'draining' desired state",

	// —— structure/v1（附录 A.11）：过程性动作与 Rotate 系（动作改事实 +
	// 收敛面接棒的混合范式）。
	"/fleetly.structure.v1.DatabasesService/TriggerBackup":          "procedural action producing a backup artifact, not a desired state (ADR-0039)",
	"/fleetly.structure.v1.DatabasesService/VerifyBackup":           "produces a one-shot verification fact; no terminal state to hold",
	"/fleetly.structure.v1.DatabasesService/RotateDatabasePassword": "Rotate family: mints credentials that reach convergence via the material fingerprint (mixed paradigm)",
	"/fleetly.structure.v1.NetworksService/RebuildNetwork":          "remediation verb: the desired state is the network row itself; the rebuild repairs an overlay lesion (ADR-0046)",

	// —— system/v1（附录 A.13）：受管域治理动词与控制面导出过程。
	"/fleetly.system.v1.SystemService/RestartComponent":        "managed-domain governance verb: orchestrator force-reschedule semantics, no user spec face (ADR-0069 precedent)",
	"/fleetly.system.v1.PlatformService/TriggerPlatformBackup": "procedural action: a control-plane export run (ADR-0039)",

	// —— telemetry/v1（附录 A.14）：诊断仪式。
	"/fleetly.telemetry.v1.AlertingService/TestNotificationChannel": "diagnostic ritual: sends one outbound test payload; the channel row is unchanged (ADR-0041)",
}

// actionReadScopeExemptions 是单向 scope 断言的豁免面：ACTION 携 READ
// scope 必须在此显式登记（一句理由），未登记即红；不再命中（动作改类/
// scope 翻向/proto 消失）同样红——双向保鲜。初值按 assembly.NewPolicySet()
// 读出的在役 scope 实况登记（2026-10-18）：ACTION 桶 27 条中仅 3 条声明
// read 方向；铸造族的 CreateExecSession/StreamExecSession 声明 exec:write
// （WRITE 方向天然合法，无需豁免），ReceiveWebhook/Login/AcceptInvitation
// 是 PUBLIC 无 scope 面（不在断言域）。
var actionReadScopeExemptions = map[string]string{
	"/fleetly.structure.v1.DatabasesService/VerifyBackup":   "one-shot verification: recomputes the object digest against the ledger receipt; databases:read matches the read capability exercised, no state changes",
	"/fleetly.structure.v1.DatabasesService/BrowseDatabase": "mint family: admits a short-lived read-only browse session; databases:read matches the delegated capability the session carries (ADR-0051)",
	"/fleetly.telemetry.v1.EventsService/IssueEventTicket":  "mints a read-only SSE ticket; events:read matches the delegated capability, not the mint action (ADR-0026)",
}

// idemActionExemptions 是幂等执法面的 ACTION 成员豁免面（ADR-0081 §4.5）：
// idem.EnforcedMethods 成员必须落 DECLARATION，或在此以「重放安全」理由
// 显式登记（ACTION 无终态可持有，幂等重放承诺须逐条自证）；未登记即红，
// 豁免不再命中（成员改类/离开执法面）同样红。初值 = 在表四条 ACTION
// （enforcer.go 表内注记为法源转写）。
var idemActionExemptions = map[string]string{
	"/fleetly.delivery.v1.HooksService/ReceiveWebhook":         "the gateway derives the idempotency key from X-GitHub-Delivery; at-least-once redelivery replays the first response (dedup anchor and side effect in one step, Q-21)",
	"/fleetly.structure.v1.DatabasesService/TriggerBackup":     "replay-safe: writes only a ledger row and exports a running database read-only (ADR-0039 decision 10)",
	"/fleetly.system.v1.PlatformService/TriggerPlatformBackup": "replay-safe: a control-plane snapshot export; a same-key retry returns the first response (ADR-0039 decision 10)",
	"/fleetly.structure.v1.NetworksService/RebuildNetwork":     "replay-safe: an already-attachable network takes the fast path with zero perturbation; an in-claim replay returns the first response (ADR-0046)",
}

// TestWriteClassCompleteClassification：proto 全方法 × 三桶完备对账（ADR-0081
// 验收锚，freezeguard「全方法 × 桶」既定形态）。DECLARATION/ACTION 两表
// 枚举，READ 是余集（§1 读前缀缺省归类，与冻结读面同前缀集）；两表皆无
// 且不携读前缀 = 未分类红；表条目在 proto 不存在 = 死条目红（更名/删除
// 曝光）；27/53/154 台账实数漂移 = 立法文件与代码脱钩红（同 commit 重编
// 附录）。
func TestWriteClassCompleteClassification(t *testing.T) {
	all := protoAllMethods(t)
	if len(all) < 150 {
		t.Fatalf("proto method scan found only %d methods — guard is blind, fix the scan", len(all))
	}

	// 台账实数对账（附录统计：27 ACTION + 53 DECLARATION + 74 READ = 154；
	// H8 阶段 B 的 QueryScopedMetrics 是 READ 余集）。
	if len(writeClassActions) != 27 {
		t.Errorf("writeClassActions holds %d entries; the ADR-0081 appendix ledger says 27 — re-transcribe the classification table and the appendix in the same commit", len(writeClassActions))
	}
	if len(writeClassDeclarations) != 53 {
		t.Errorf("writeClassDeclarations holds %d entries; the ADR-0081 appendix ledger says 53 — re-transcribe the classification table and the appendix in the same commit", len(writeClassDeclarations))
	}
	if len(all) != 154 {
		t.Errorf("proto surface holds %d methods; the ADR-0081 appendix ledger says 154 (27 ACTION + 53 DECLARATION + 74 READ) — reclassify and re-transcribe the appendix in the same commit", len(all))
	}

	var unclassified []string
	for m := range all {
		if _, ok := writeClassActions[m]; ok {
			continue
		}
		if writeClassDeclarations[m] {
			continue
		}
		if isReadVerb(m) {
			continue // READ 余集：按缺省读前缀归类的合法剩余（附录不枚举）
		}
		unclassified = append(unclassified, m)
	}
	sort.Strings(unclassified)
	for _, m := range unclassified {
		t.Errorf("RPC %s falls into no write class — it carries neither a read-verb prefix nor a DECLARATION/ACTION table entry; classify it consciously in writeclassguard_test.go and re-transcribe the ADR-0081 appendix in the same commit", m)
	}

	// 反向：表条目在 proto 不存在 = 死条目。
	var dead []string
	for m := range writeClassActions {
		if !all[m] {
			dead = append(dead, "ACTION "+m)
		}
	}
	for m := range writeClassDeclarations {
		if !all[m] {
			dead = append(dead, "DECLARATION "+m)
		}
	}
	sort.Strings(dead)
	for _, m := range dead {
		t.Errorf("write class table lists %s but no such proto method exists — remove the stale entry and re-transcribe the ADR-0081 appendix in the same commit", m)
	}
}

// TestWriteClassScopeDirection：单向 scope 断言（ADR-0081 §4.3，scopeguard
// 走真 descriptor 的形态）：DECLARATION ⇒ scope WRITE/ADMIN（声明写必是
// 授权写面，标 READ 即 fail-open 洞）；ACTION 允许 READ-scope（不设反向
// 断言 ACTION ⇒ WRITE）但必须落 actionReadScopeExemptions 豁免面；READ
// 余集与 PUBLIC/无 scope 面不在本守卫域（读型动词方向漂移由 scopeguard 执法）。
func TestWriteClassScopeDirection(t *testing.T) {
	policy, err := assembly.NewPolicySet()
	if err != nil {
		t.Fatalf("policy build failed: %v", err)
	}
	methods := policy.Methods()
	if len(methods) < 150 {
		t.Fatalf("policy surface suspiciously small (%d methods) — guard is blind, fix the scan", len(methods))
	}

	usedExemptions := map[string]bool{}
	for _, p := range methods {
		if p.Access != authz.AccessServer || p.Scope == nil {
			continue // PUBLIC/PERMISSION 面与无 scope 方法不在本守卫域
		}
		if _, isAction := writeClassActions[p.Method]; isAction {
			if p.Scope.Op != authz.ScopeRead {
				continue // WRITE/ADMIN 方向对 ACTION 天然合法
			}
			reason, ok := actionReadScopeExemptions[p.Method]
			if !ok || reason == "" {
				t.Errorf("ACTION RPC %s declares scope %s:%s without a registered exemption — a read-scoped action write is only legal through the explicit exemption channel; register it in actionReadScopeExemptions with a reason (ADR-0081 §4.3)",
					p.Method, p.Scope.Resource, p.Scope.Op)
				continue
			}
			usedExemptions[p.Method] = true
			continue
		}
		if writeClassDeclarations[p.Method] {
			if p.Scope.Op == authz.ScopeRead {
				t.Errorf("DECLARATION RPC %s declares scope %s:%s — a declaration write is always the authorized write face (WRITE/ADMIN); a read scope on it is the fail-open hole this guard exists to close (ADR-0081 §4.3)",
					p.Method, p.Scope.Resource, p.Scope.Op)
			}
			continue
		}
		// READ 余集成员：本守卫域外（scopeguard 已对账动词 × 方向）。
	}

	// 双向保鲜：豁免不再命中即红。
	var stale []string
	for m, reason := range actionReadScopeExemptions {
		if reason == "" {
			t.Errorf("action read-scope exemption %s must carry a reason", m)
		}
		if !usedExemptions[m] {
			stale = append(stale, m)
		}
	}
	sort.Strings(stale)
	for _, m := range stale {
		t.Errorf("action read-scope exemption %s no longer covers a real gap (member reclassified or scope flipped); remove the entry", m)
	}
}

// TestWriteClassFrozenVerbsConsistency：冻结封禁面从属断言（ADR-0081 §4.4 /
// ADR-0017 附录 A.3）：governance.FrozenVerbs 键集 ⊆ DECLARATION∪ACTION——
// 冻结只拦写面；READ 或未分类成员即红，死条目（proto 无此方法）同红。
func TestWriteClassFrozenVerbsConsistency(t *testing.T) {
	all := protoAllMethods(t)
	var offenders []string
	decl, act := 0, 0
	for m := range governance.FrozenVerbs {
		if !all[m] {
			t.Errorf("governance.FrozenVerbs lists %s but no such proto method exists — remove the stale entry", m)
			continue
		}
		if _, ok := writeClassActions[m]; ok {
			act++
			continue
		}
		if writeClassDeclarations[m] {
			decl++
			continue
		}
		offenders = append(offenders, m)
	}
	sort.Strings(offenders)
	for _, m := range offenders {
		t.Errorf("governance.FrozenVerbs lists %s which classifies as READ (or is unclassified) — the change freeze only intercepts the write face (DECLARATION∪ACTION, ADR-0081 §4.4); fix the classification or drop the verb from the freeze table", m)
	}
	// 对账注记：在表实数（ADR-0081 §4.4 散文的台账形态，42 = 34 + 8）。
	t.Logf("FrozenVerbs reconciliation: %d entries = %d DECLARATION + %d ACTION", len(governance.FrozenVerbs), decl, act)
}

// TestWriteClassIdemConsistency：幂等执法面从属断言（ADR-0081 §4.5 /
// ADR-0024）：idem.EnforcedMethods 全体成员（创建型动词的收网面）⊆
// DECLARATION∪豁免——幂等重放承诺只能挂在声明写面；执法面的 ACTION
// 成员（重放安全但无终态可持有）必须经 idemActionExemptions 豁免面带
// 「重放安全」理由显式登记；READ 或未分类成员即红，死条目同红。
func TestWriteClassIdemConsistency(t *testing.T) {
	all := protoAllMethods(t)
	if len(idem.EnforcedMethods) < 10 {
		t.Fatalf("enforced surface suspiciously small (%d) — guard is blind, fix the scan", len(idem.EnforcedMethods))
	}

	usedExemptions := map[string]bool{}
	var offenders []string
	for m := range idem.EnforcedMethods {
		if !all[m] {
			t.Errorf("idem.EnforcedMethods lists %s but no such proto method exists — remove the stale entry", m)
			continue
		}
		if _, isAction := writeClassActions[m]; isAction {
			reason, ok := idemActionExemptions[m]
			if !ok || reason == "" {
				t.Errorf("idem.EnforcedMethods member %s classifies as ACTION without a registered exemption — idempotent replay on an action write needs an explicit replay-safety reason; register it in idemActionExemptions (ADR-0081 §4.5)", m)
				continue
			}
			usedExemptions[m] = true
			continue
		}
		if writeClassDeclarations[m] {
			continue
		}
		offenders = append(offenders, m)
	}
	sort.Strings(offenders)
	for _, m := range offenders {
		t.Errorf("idem.EnforcedMethods member %s classifies as READ (or is unclassified) — the idempotency guarantee only attaches to the write face (DECLARATION∪registered ACTION, ADR-0081 §4.5); fix the classification or drop the method from the enforced set", m)
	}

	// 双向保鲜：豁免不再命中即红。
	var stale []string
	for m, reason := range idemActionExemptions {
		if reason == "" {
			t.Errorf("idem action exemption %s must carry a reason", m)
		}
		if !usedExemptions[m] {
			stale = append(stale, m)
		}
	}
	sort.Strings(stale)
	for _, m := range stale {
		t.Errorf("idem action exemption %s no longer covers a real gap; remove the entry", m)
	}
}
