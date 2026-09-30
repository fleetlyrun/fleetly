//go:build !windows

package cmd

// 磁盘余量探针（POSIX 面）：statfs 的 f_bavail×f_bsize（调用方可用量）。

import (
	"syscall"
)

// syscallDiskProbe 返回 path 所在卷的可用字节数。
func syscallDiskProbe(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
