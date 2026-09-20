//go:build windows

package execrun

import "os/exec"

// armGroupKill 在 Windows 上是 no-op：无跨平台进程组原语，ctx 取消后依赖
// CommandContext 缺省的 Process.Kill + WaitDelay 兜底（目标部署形态是
// Linux——组杀在那侧生效；Windows 开发形态保底「直接子进程必死、Wait
// 限时返回」）。
func armGroupKill(*exec.Cmd) {}
