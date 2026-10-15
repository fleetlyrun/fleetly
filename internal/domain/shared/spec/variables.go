// 三级变量合成（F2.9 ADR-0043 两级 → ADR-0072 三级）：Project 级
// SharedVariable 在下、源声明 env（AppSpec 进程字段，部署源直传）居中、
// App 级平台变量 AppVariable 在顶（同键覆盖）；归一化期合成进 AppSpec，
// Revision 冻结即最终生效集（ADR-0027 案 a）。本文件只持纯函数——
// SharedVariable/AppVariable 的装载是受理咽喉（application 层
// engine.SubmitSpec）的事。
package spec

import (
	"regexp"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// EnvNamePattern 钉死变量名（env 键）字符集（ADR-0043 决策 2）：POSIX env
// 键形态——首字符字母/下划线，余字母数字/下划线，长度 ≤64。变量名直接
// 成为容器 env 键，白名单外形态受理面即拒（新增面执法：SharedVariable
// 名与 DeployRequest.env 键；存量 compose environment 键不追溯）。
const EnvNamePattern = `[A-Za-z_][A-Za-z0-9_]{0,63}` //nolint:gosec // 字符集正则字面量, 非凭证值

var envNameRe = regexp.MustCompile(`^` + EnvNamePattern + `$`)

// ValidEnvName 报告变量名是否落在 env 键白名单。
func ValidEnvName(name string) bool { return envNameRe.MatchString(name) }

// ValidateEnvKeys 校验一批变量名（field 前缀精确到调用面路径）。
func ValidateEnvKeys(field string, env map[string]string) error {
	for k := range env {
		if !ValidEnvName(k) {
			return invalidf(field,
				"variable name %q must match %q (variable names become environment keys)", k, EnvNamePattern)
		}
	}
	return nil
}

// MergeSharedEnv 把 Project 层共享变量合成进 AppSpec 的全部进程
// （Processes 与 FirstBootJobs 的 Process——迁移 job 同享共享层，ADR-0043
// 决策 3）：flat union，共享层在下、进程自身 env（App 级）覆盖同键。
// 冻结咽喉（engine.SubmitSpec）在 marshal 前调用——Revision 冻结的即最终
// 生效集。protojson 规范序列化 map 按键排序，合成结果确定性可复现
// （同 Source 同变量状态两次归一化逐字节相等）。
func MergeSharedEnv(s *specv1.AppSpec, shared map[string]string) {
	if len(shared) == 0 {
		return
	}
	for _, p := range s.GetProcesses() {
		p.Env = mergeEnvLayer(p.GetEnv(), shared)
	}
	for _, j := range s.GetFirstBootJobs() {
		if j.GetProcess() != nil {
			j.Process.Env = mergeEnvLayer(j.Process.GetEnv(), shared)
		}
	}
}

// MergeAppEnv 把 App 级平台变量（AppVariable）合成进 AppSpec 的全部进程
// （ADR-0072 三级合成的顶层）：源声明 env 在下、AppVariable 覆盖同键——
// env 不写死在源/配置文件里，平台侧 store 是权威覆盖层。与
// MergeSharedEnv 同在冻结咽喉、marshal 前调用（三级叠加：Shared 底 <
// 源声明 env 中 < AppVariable 顶）；合成值进冻结体，digest 覆盖——
// 变量轮换产生新内容即新 Revision，需显式部署生效。
func MergeAppEnv(s *specv1.AppSpec, appVars map[string]string) {
	if len(appVars) == 0 {
		return
	}
	for _, p := range s.GetProcesses() {
		p.Env = mergeEnvLayer(appVars, p.GetEnv())
	}
	for _, j := range s.GetFirstBootJobs() {
		if j.GetProcess() != nil {
			j.Process.Env = mergeEnvLayer(appVars, j.Process.GetEnv())
		}
	}
}

// mergeEnvLayer 合成单进程 env：app（首参）覆盖同键、shared 在下——
// 首参恒为覆盖层（Shared 合成传进程 env、AppVariable 合成传 store 值）。
func mergeEnvLayer(app map[string]string, shared map[string]string) map[string]string {
	out := make(map[string]string, len(shared)+len(app))
	for k, v := range shared {
		out[k] = v
	}
	for k, v := range app {
		out[k] = v
	}
	return out
}
