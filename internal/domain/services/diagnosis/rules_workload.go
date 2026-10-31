// rules_workload.go 是载体终态类规则（crash-loop / OOM / 健康检查 / 镜像
// 拉取）。判定口径共同点：**终态计数 + 终止态推断，不信单一 waiting
// reason**（设计 §2.5：reason 文案是弱信号，只有「窗口内终态次数 + 最近
// 终止态仍在」才构成循环证据）。
package diagnosis

import (
	"strconv"
	"strings"
)

// CrashLoopThreshold 是窗口内终态次数阈值：≥N 且最近终止态仍在才算崩溃
// 循环（N=3：两次可能是启动竞态/节点抖动，三次是模式）。
const CrashLoopThreshold = 3

// ruleCrashLoop：窗口内终态次数 ≥ CrashLoopThreshold 且最新观测仍是失败/
// 失联态（最近终止态仍在——已恢复的循环不报）。exit code 语义区分：
// 126（不可执行）/127（命令不存在）是声明面问题 → BAD_COMMAND 条目
// （declaration 归属）；137/143 在 Params 透出语义（SIGKILL 常为 OOM killer，
// 由 OOM 规则并诊）；其余 → 通用条目（app 归属）。
var ruleCrashLoop = Rule{
	ID: "crash-loop",
	Check: func(facts Facts) []Finding {
		obs, ok := facts.observations()
		if !ok {
			return nil // 观测面读不了：沉默（不猜）
		}
		var out []Finding
		for _, o := range obs {
			if o.RestartCount < CrashLoopThreshold {
				continue // 窗口内终态次数不足：不是循环（负向主体——节点重启/单次崩溃/先败后成）
			}
			if !stillTerminated(o.State) {
				continue // 最近终止态不在（已恢复/已停止/已完成）：不是进行中的循环
			}
			out = append(out, crashLoopFinding(o))
		}
		return out
	},
}

// crashLoopFinding 按最近退出码选择条目与解释参数。
func crashLoopFinding(o WorkloadObservationFact) Finding {
	params := map[string]string{
		"restart_count": strconv.Itoa(o.RestartCount),
		"state":         string(o.State),
	}
	if code := exitCodeString(o.ExitCode); code != "" {
		params["exit_code"] = code
	}
	if meaning := ExitCodeMeaning(derefInt(o.ExitCode)); meaning != "" {
		params["exit_code_meaning"] = meaning
	}
	// 命令面失败是声明问题：单独条目（declaration），修复走部署面预填 command。
	if o.ExitCode != nil && (*o.ExitCode == ExitNotExecutable || *o.ExitCode == ExitCommandNotFound) {
		return instantiate("F_WORKLOAD_CRASH_LOOP_BAD_COMMAND", o, nil, params)
	}
	// 通用循环：restart 动词预填 process（B1 已裁：restart 豁免强制确认）。
	return instantiate("F_WORKLOAD_CRASH_LOOP", o, map[string]string{"process": o.Process}, params)
}

// ruleOOM：OOM 证据 = 最近终态退出码 137 或 Reason 带 OOM 标记（编排器
// 原文透传契约：容器 cgroup OOM 以 "OOMKilled" 标记入 Reason——W1b 装配
// 责任），且最近终止态仍在（恢复了的 OOM 不报）。两 variant（设计 §2.5）：
//   - 带 OOMKilled 标记 → WITH_LIMIT：撞的是自己声明的内存上限；
//   - 137/内存耗尽原文但无标记 → WITHOUT_LIMIT：未声明 limit，节点内存
//     压力杀进程。
var ruleOOM = Rule{
	ID: "oom",
	Check: func(facts Facts) []Finding {
		obs, ok := facts.observations()
		if !ok {
			return nil
		}
		var out []Finding
		for _, o := range obs {
			if !stillTerminated(o.State) {
				continue
			}
			if !oomEvidence(o) {
				continue
			}
			id := "F_WORKLOAD_OOM_WITHOUT_LIMIT"
			if marksContainerOOM(o.Reason) {
				id = "F_WORKLOAD_OOM_WITH_LIMIT"
			}
			params := map[string]string{"state": string(o.State)}
			if code := exitCodeString(o.ExitCode); code != "" {
				params["exit_code"] = code
			}
			if o.Reason != "" {
				params["reason"] = o.Reason
			}
			out = append(out, instantiate(id, o, nil, params))
		}
		return out
	},
}

// oomEvidence 报告观测是否携带 OOM 证据（137 或 Reason 内存耗尽原文）。
func oomEvidence(o WorkloadObservationFact) bool {
	if o.ExitCode != nil && *o.ExitCode == ExitSIGKILL {
		return true
	}
	r := strings.ToLower(o.Reason)
	return strings.Contains(r, "oomkilled") || strings.Contains(r, "out of memory")
}

// marksContainerOOM 报告 Reason 是否带容器 cgroup OOM 标记（Docker
// OOMKilled 旗标面：撞的是自己声明的 limit；无标记的 137 是节点级内存
// 压力/外部 SIGKILL——按无 limit variant 解释）。
func marksContainerOOM(reason string) bool {
	return strings.Contains(strings.ToLower(reason), "oomkilled")
}

// ruleHealthCheck：健康探针持续失败。契约：编排器健康探针失败原文
// （"health check failed"/"unhealthy" 等）透传进 Reason；判定 = 失败/失联
// 态 + Reason 含 "health"（子串弱信号仅作分类器，不作循环证据——循环证据
// 仍是 RestartCount）。
var ruleHealthCheck = Rule{
	ID: "health-check",
	Check: func(facts Facts) []Finding {
		obs, ok := facts.observations()
		if !ok {
			return nil
		}
		var out []Finding
		for _, o := range obs {
			if !stillTerminated(o.State) {
				continue
			}
			if !strings.Contains(strings.ToLower(o.Reason), "health") {
				continue
			}
			params := map[string]string{"state": string(o.State)}
			if o.Reason != "" {
				params["reason"] = o.Reason
			}
			if code := exitCodeString(o.ExitCode); code != "" {
				params["exit_code"] = code
			}
			out = append(out, instantiate("F_WORKLOAD_HEALTH_CHECK_FAILING", o, nil, params))
		}
		return out
	},
}

// imagePullSignals 是镜像拉取失败的编排器原文信号子串（小写比对；契约：
// 拉取失败原文透传进 Reason）。external 归属（镜像源/registry 面）；引用
// 拼错与源不可达的进一步区分需要 spec 对照事实，v1 以 Summary 双Hint。
var imagePullSignals = []string{
	"no such image",
	"manifest unknown",
	"image pull",
	"pull access",
	"failed to pull",
	"pull rate limit",
}

// ruleImagePull：载体起不来因为镜像拉不下来（rejected/failed + 拉取原文）。
var ruleImagePull = Rule{
	ID: "image-pull",
	Check: func(facts Facts) []Finding {
		obs, ok := facts.observations()
		if !ok {
			return nil
		}
		var out []Finding
		for _, o := range obs {
			if !stillTerminated(o.State) {
				continue
			}
			r := strings.ToLower(o.Reason)
			hit := false
			for _, sig := range imagePullSignals {
				if strings.Contains(r, sig) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			params := map[string]string{"state": string(o.State)}
			if o.Reason != "" {
				params["reason"] = o.Reason
			}
			out = append(out, instantiate("F_WORKLOAD_IMAGE_PULL_FAILING", o, nil, params))
		}
		return out
	},
}

// derefInt 安全解引用（nil → 0，配合 ExitCodeMeaning 的 default 分支）。
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
