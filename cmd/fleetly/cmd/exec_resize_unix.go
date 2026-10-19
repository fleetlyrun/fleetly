//go:build unix

package cmd

// TTY resize 传播（Unix 面）：SIGWINCH 是 Unix 专有信号（Windows 面见
// exec_resize_windows.go）；pumpExecUpstream 只经本文件的 helper 触碰
// signal/syscall，主文件因此保持平台无关。

import (
	"os"
	"os/signal"
	"syscall"
)

// startTerminalResize 把 SIGWINCH 投递到 ch（ch 由调用方创建与消费）。
func startTerminalResize(ch chan os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}

// stopTerminalResize 停止向 ch 投递（幂等；调用方 defer 收口）。
func stopTerminalResize(ch chan os.Signal) {
	signal.Stop(ch)
}
