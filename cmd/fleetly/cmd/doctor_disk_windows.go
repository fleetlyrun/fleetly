package cmd

// 磁盘余量探针（Windows 面）：GetDiskFreeSpaceEx 取调用方可用量。

import (
	"golang.org/x/sys/windows"
)

// syscallDiskProbe 返回 path 所在卷的可用字节数。
func syscallDiskProbe(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(free), nil
}
