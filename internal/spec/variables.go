// 两级变量合成（F2.9，ADR-0043）：Project 级 SharedVariable 在下、App 级
// env（部署源直传）覆盖；归一化期合成进 AppSpec，Revision 冻结即最终
// 生效集（ADR-0027 案 a）。本文件只持纯函数——SharedVariable 的装载是
// API 层（freezeRevision 咽喉）的事。
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
// 冻结咽喉（freezeRevision）在 marshal 前调用——Revision 冻结的即最终
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

// mergeEnvLayer 合成单进程 env：shared 在下、app 层覆盖同键。
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
