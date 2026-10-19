package keyring

// KEK 落盘纪律（R07-L3）：平台最高等级的密钥文件必须 tmp + fsync + rename
// + 目录 fsync + 权限复核——单次 WriteFile 在崩溃/掉电窗口会留下空/半截
// master.agekey，下次启动解析失败即不可自愈（此时虽无密文行，但 daemon
// 起不来）。半成品永不成为正式路径；失败路径清理 tmp。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// writeFileAtomic 把 data 原子写到 path。
//
// 序：MkdirAll(0700) + 目录权限复核 → 同目录唯一 tmp（0600）→ 写 → 文件
// fsync → close → rename → 目录 fsync → 正式文件权限复核。rename 是
// POSIX 原子换名：正式路径要么不存在，要么是完整新内容。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := enforceablePermCheck(dir, 0o700, true); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return enforceablePermCheck(path, perm, false)
}

// syncDir 把 rename 落进目录项（掉电后目录项不丢）。Windows 的目录句柄
// 不支持 fsync，跳过（Go 的 perm 位在该平台也不承载 ACL 语义）。
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir) //nolint:gosec // 平台私有数据根下的 keys 目录
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck // 只读句柄，关闭错误无处置面
	return d.Sync()
}

// enforceablePermCheck 复核目录/文件权限（POSIX：owner-only）。过宽先
// chmod 修复再复核，仍不达标即硬错——权限是 KEK 的防线之一，不允许
// "写成功了但谁都能读"的静默降级。
func enforceablePermCheck(path string, want os.FileMode, isDir bool) error {
	if runtime.GOOS == "windows" {
		return nil // 已知环境项：Go perm 位不表达 Windows ACL
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, want); err != nil {
		return fmt.Errorf("material: fix key permission: %w", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		kind := "file"
		if isDir {
			kind = "directory"
		}
		return fmt.Errorf("material: key %s permissions are too open (want %04o, got %04o)", kind, want, info.Mode().Perm())
	}
	return nil
}
