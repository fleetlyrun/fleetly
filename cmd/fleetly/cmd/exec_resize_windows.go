//go:build windows

package cmd

// TTY resize 传播（Windows 面）：Windows 控制台无 SIGWINCH 等价信号，本阶段
// 不传播尺寸变更（no-op）。保留与 Unix 面相同的签名，pumpExecUpstream 保持
// 平台无关；Windows 终端的 resize 传播留待后续，不在本阶段引入新依赖。

import "os"

// startTerminalResize Windows 面 no-op（无 SIGWINCH 可订阅）。
func startTerminalResize(ch chan os.Signal) {
	_ = ch
}

// stopTerminalResize Windows 面 no-op（无订阅可停）。
func stopTerminalResize(ch chan os.Signal) {
	_ = ch
}
