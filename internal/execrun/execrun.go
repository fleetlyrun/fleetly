package execrun

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// defaultWaitDelay 是 ctx 取消后 cmd.Wait 回收 I/O 管道的兜底上限（与 E6
// 先例 gitWaitDelay 同值：30s 覆盖最慢的正常收尾，又足以在挂死形态下及时
// 把并发槽/调用方还回来）。
const defaultWaitDelay = 30 * time.Second

// Options 是 Run 的可选参数（零值全取缺省）。
type Options struct {
	// Dir 是子进程工作目录（空 = 继承当前进程）。
	Dir string
	// Env 是追加在 os.Environ() 之上的环境变量（KEY=VALUE 词形）。
	Env []string
	// WaitDelay 覆盖缺省兜底上限；<=0 取 defaultWaitDelay。
	WaitDelay time.Duration
}

// Run 执行外部命令并等待其结束，返回捕获的 stderr 字节与错误。
//
// 生命周期保证（MG-1）：ctx 取消 → 子进程（unix 上连带整个进程组）被杀；
// 孙进程持有 stderr 管道写端时 Wait 至多再等 WaitDelay 即返回。错误判定：
// 进程被杀形态返回 *exec.ExitError（signal: killed）；进程在取消后仍以
// 成功码退出等罕见竞态返回 ctx 错误——两者都把失败归因到「ctx 取消」这一
// 唯一触发源，调用方以 err != nil && ctx.Err() != nil 判定即可。
func Run(ctx context.Context, name string, args []string, opts Options) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204：调用方负责参数构造（包内调用点为白名单词形）
	cmd.Dir = opts.Dir
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	if opts.WaitDelay > 0 {
		cmd.WaitDelay = opts.WaitDelay
	} else {
		cmd.WaitDelay = defaultWaitDelay
	}
	armGroupKill(cmd) // unix：进程组 + ctx 取消组杀；Windows：no-op（仅 WaitDelay）
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	err := cmd.Run()
	return errOut.Bytes(), err
}
