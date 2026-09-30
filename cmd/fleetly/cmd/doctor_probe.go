package cmd

// doctor 探针的底层助手（exec/TCP 拨号/磁盘余量）。探针接缝在 doctor.go
//（probeDocker/probePort/probeDisk/probeRemote）；本文件是接缝背后的真
// 实实现。

import (
	"bytes"
	"context"
	"net"
	"os/exec"
	"strings"
	"time"
)

// execCommand 带超时跑外部命令并回收 stdout。
func execCommand(ctx context.Context, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // 命令名是探针常量（docker）
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// trimSpace 是 strings.TrimSpace 的本地别名（doctor 渲染小助手）。
func trimSpace(s string) string { return strings.TrimSpace(s) }

// dialTCPTimeout 探测 TCP 端口监听（连通即视为在监听）。
func dialTCPTimeout(addr string, wait time.Duration) error {
	d := net.Dialer{Timeout: wait}
	conn, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close() //nolint:errcheck // 探测连接，关闭错误无处置面
}
