//go:build unix

package execrun

import (
	"os/exec"
	"syscall"
)

// armGroupKill 把子进程放入独立进程组并接管 ctx 取消语义（MG-1）：
// CommandContext 缺省的 Process.Kill 只杀直接子进程，孙进程存活且仍持有
// 管道写端；改为组杀 syscall.Kill(-pgid, SIGKILL) 连同孙进程一并回收
// （目标部署形态 Linux 生效；WaitDelay 仍是 I/O 兜底的双保险另一半）。
func armGroupKill(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil // Start 之前 ctx 已取消：无进程可杀，由 Start/Wait 报 ctx 错
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
