package guards

// 冻结面反扫（ADR-0017 附录 A.3 同批守卫）：proto 全部 RPC 必须落进三桶
// 之一——冻结封禁面（governance.FrozenVerbs）/ 豁免面（带理由）/ 读面
// （Get/List/Wait/Stream/WhoAmI/Explain 前缀）。新 RPC 未分类即红：冻结
// 是"变更动词都该被拦"的承诺，漏分类 = 冻结静默缺一块。反向死条目
//（冻结表/豁免表引用 proto 不存在的方法）同样红；豁免不再命中即红
//（白名单双向保鲜，guard C 同款）。

import (
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/governance"
)

// freezeExemptions 是"变更型但显式不冻结"的豁免表（FullMethod → 理由，
// ADR-0017 附录 A.3 豁免面）。
var freezeExemptions = map[string]string{
	// 停止族：冻结期间安全收口必须可用（拦停不是变更）。
	"/fleetly.automation.v1.TasksService/StopTask":             "stop family: safety stops stay available during a freeze",
	"/fleetly.automation.v1.RunsService/StopRun":               "stop family: safety stops stay available during a freeze",
	"/fleetly.delivery.v1.DeploymentsService/CancelDeployment": "stop family: cancelling an in-flight deployment reduces change, it does not add any",
	// 租约保温：冻结不杀保温（resident 池的存活心跳）。
	"/fleetly.automation.v1.TasksService/RenewTask": "owner-lease keep-alive must keep working during a freeze (freezing must not drain live pools)",
	// runtime 运维面：集群运维不是变更控制面。
	"/fleetly.runtime.v1.NodesService/EnrollNode":   "cluster ops: enrollment is runtime administration, not workload change control",
	"/fleetly.runtime.v1.NodesService/DrainNode":    "cluster ops: drain is runtime administration, not workload change control",
	"/fleetly.runtime.v1.NodesService/CordonNode":   "cluster ops: cordon is runtime administration, not workload change control",
	"/fleetly.runtime.v1.NodesService/UncordonNode": "cluster ops: uncordon is runtime administration, not workload change control",
	// identity 全部变更：账号/Token 管理不属变更控制，且冻结解除依赖这些
	// 面可用（冻结不得把自己锁在门外）。
	"/fleetly.identity.v1.UsersService/CreateUser":             "identity management is outside change control; lifting a freeze depends on it",
	"/fleetly.identity.v1.UsersService/DeleteUser":             "identity management is outside change control",
	"/fleetly.identity.v1.TeamsService/CreateTeam":             "identity management is outside change control",
	"/fleetly.identity.v1.TeamsService/DeleteTeam":             "identity management is outside change control",
	"/fleetly.identity.v1.RolesService/CreateRole":             "identity management is outside change control",
	"/fleetly.identity.v1.RolesService/DeleteRole":             "identity management is outside change control",
	"/fleetly.identity.v1.TokensService/CreateToken":           "identity management is outside change control",
	"/fleetly.identity.v1.TokensService/RevokeToken":           "identity management is outside change control; revoking a runaway token during a freeze is a brake, not a change",
	"/fleetly.identity.v1.InvitationsService/CreateInvitation": "identity management is outside change control",
	"/fleetly.identity.v1.InvitationsService/AcceptInvitation": "identity management is outside change control",
	// 冻结管理本体。
	"/fleetly.system.v1.GovernanceService/SetChangeFreeze":   "the freeze surface itself",
	"/fleetly.system.v1.GovernanceService/LiftChangeFreeze":  "the freeze surface itself",
	"/fleetly.system.v1.GovernanceService/ListChangeFreezes": "the freeze surface itself",
	// 订阅票据铸造：读路径的凭证面。
	"/fleetly.telemetry.v1.EventsService/IssueEventTicket": "read-path auth: minting a subscription ticket changes no platform state",
	// Backup 触发（F2.2，ADR-0039 决策 10）：保护性操作——只铸台账行 +
	// 只读导出运行中的库；冻结窗（变更冻结语义）不得停摆备份。
	"/fleetly.structure.v1.DatabasesService/TriggerBackup": "protective operation: backup trigger writes only a ledger row and exports a running database read-only; a change freeze must not stop backups (ADR-0039)",
	// Platform Backup 手动触发（F2.3，ADR-0039 决策 10）：保护性操作——
	// 冻结期备份不停摆同理由；升级序的前置动词更不得被冻结拦停。
	"/fleetly.system.v1.PlatformService/TriggerPlatformBackup": "protective operation: a snapshot of the control-plane state reduces risk during a freeze; upgrade sequencing depends on it (ADR-0039/ADR-0015)",
	// VerifyBackup：无状态迁移的校验面（重算摘要比对回执——读路径的
	// 执行面，动词名不在读前缀集，豁免登记）。
	"/fleetly.structure.v1.DatabasesService/VerifyBackup": "read-path execution: recomputes the object digest against the ledger receipt, changes no platform state",
}

// freezeReadPrefixes 是读面前缀（无副作用，免冻结分类；Diff 是两 Revision
// 对照的读面）。
var freezeReadPrefixes = []string{"Get", "List", "Wait", "Stream", "WhoAmI", "Explain", "Diff"}

// isReadVerb 报告方法名是否落读面前缀。
func isReadVerb(fullMethod string) bool {
	name := fullMethod
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	for _, p := range freezeReadPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// TestFreezeSurfaceCompleteClassification：proto 全方法 × 三桶分类完备对账。
func TestFreezeSurfaceCompleteClassification(t *testing.T) {
	all := protoAllMethods(t)
	if len(all) < 40 {
		t.Fatalf("proto method scan found only %d methods — guard is blind, fix the scan", len(all))
	}
	usedExemptions := map[string]bool{}

	var unclassified []string
	for m := range all {
		if _, frozen := governance.FrozenVerbs[m]; frozen {
			continue
		}
		if reason, ok := freezeExemptions[m]; ok && reason != "" {
			usedExemptions[m] = true
			continue
		}
		if isReadVerb(m) {
			continue
		}
		unclassified = append(unclassified, m)
	}
	sort.Strings(unclassified)
	for _, m := range unclassified {
		t.Errorf("RPC %s is not classified for the change freeze surface — add it to governance.FrozenVerbs, or exempt it with a reason, or give it a read-verb prefix (ADR-0017 appendix A.3 guard)", m)
	}

	// 反向：冻结表出现 proto 不存在的方法 = 死条目。
	var dead []string
	for m := range governance.FrozenVerbs {
		if !all[m] {
			dead = append(dead, m)
		}
	}
	sort.Strings(dead)
	for _, m := range dead {
		t.Errorf("governance.FrozenVerbs lists %s but no such proto method exists — remove the stale entry", m)
	}

	// 双向保鲜：豁免不再命中（已进冻结表/读面/proto 消失）即红。
	for m, reason := range freezeExemptions {
		if reason == "" {
			t.Errorf("freeze exemption %s must carry a reason", m)
		}
		if !all[m] {
			t.Errorf("freeze exemption %s no longer matches any proto method; remove the entry", m)
			continue
		}
		if _, frozen := governance.FrozenVerbs[m]; frozen {
			t.Errorf("freeze exemption %s is now in the frozen set; remove the stale exemption", m)
			continue
		}
		// 读面前缀的豁免也是死条目（分诊重复）。
		if !usedExemptions[m] {
			t.Errorf("freeze exemption %s no longer covers a real gap; remove the entry", m)
		}
	}
}
