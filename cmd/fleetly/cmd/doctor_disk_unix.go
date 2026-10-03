//go:build !windows

package cmd

// 磁盘余量探针（POSIX 面）：statfs 的 f_bavail×f_bsize（调用方可用量）。

import (
	"math"
	"syscall"
)

// syscallDiskProbe 返回 path 所在卷的可用字节数。
func syscallDiskProbe(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// 字段宽度按平台不一（linux：Bavail uint64 / Bsize int64；darwin 均
	// uint64），双平台裸窄化逃不开 gosec G115——守卫形态先证明范围再转
	//（Bsize 恒正且远小于 MaxInt64；乘积超界钳位，调用方只消费有界值）。
	if st.Bsize < 1 {
		return 0, syscall.EINVAL
	}
	bsize := uint64(st.Bsize)
	avail := st.Bavail * bsize
	if avail > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(avail), nil
}
