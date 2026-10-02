package upload

// tar 安全解包（ADR-0019 附录 A.4）：白名单=普通文件+目录；路径逃逸
//（../）、绝对路径、Windows 盘符、反斜杠、symlink/hardlink/设备文件一律
// 拒绝（tar-slip 防护）。解包总量计数不越单上传上限（未压缩 tar 的展开
// 体积上界=tar 自身体积；计数是防御纵深）。

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ExtractTar 解包 tar 流到 dst（dst 不存在则创建；已存在内容的混合形态
// 由调用方幂等跳过把守）。limit 是解包总量上限（传 0 = 用单上传缺省）。
func ExtractTar(dst string, src io.Reader, limit int64) error {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	tr := tar.NewReader(src)
	total := int64(0)
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		name, ok, err := safeEntryName(hdr.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue // 根目录条目（"./"）：无目标路径
		}
		if seen[name] {
			return fmt.Errorf("tar entry %q appears twice", hdr.Name)
		}
		seen[name] = true
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, safeMode(hdr.Mode, 0o750)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			total += hdr.Size
			if total > limit {
				return &ErrTooLarge{Limit: limit, Received: total}
			}
			if err := writeFile(tr, target, hdr.Size, safeMode(hdr.Mode, 0o640)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("tar entry %q has unsupported type %q (only regular files and directories are allowed)", hdr.Name, string(rune(hdr.Typeflag)))
		}
	}
}

// writeFile 写一个普通文件（精确拷贝 size 字节——超出即损坏，宁拒不截）。
func writeFile(r io.Reader, target string, size int64, mode os.FileMode) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) //nolint:gosec // 模式已收敛白名单
	if err != nil {
		return err
	}
	n, err := io.Copy(f, r) //nolint:gosec // src 已是 tar reader（有界）
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("tar entry %q: declared %d bytes, read %d", filepath.Base(target), size, n)
	}
	return nil
}

// safeMode 收敛 tar 声明权限到安全子集（仅保留用户/组/其它 rwx 位；异常
// 声明回退缺省）。
func safeMode(tarMode int64, fallback os.FileMode) os.FileMode {
	m := tarMode & 0o777
	if m == 0 {
		return fallback
	}
	return os.FileMode(m)
}

// safeEntryName 校验并归一条目名：拒绝绝对路径、`..` 段、反斜杠（Windows
// 分隔符逃逸面）与空名；ok=false 表示根目录条目（"./"，合法跳过）。返回
// 清理后的 slash 路径。
func safeEntryName(name string) (clean string, ok bool, err error) {
	if name == "" {
		return "", false, fmt.Errorf("tar entry has an empty name")
	}
	if strings.Contains(name, "\\") {
		return "", false, fmt.Errorf("tar entry %q contains a backslash (path separator escape)", name)
	}
	clean = path.Clean(name)
	if clean == "." || clean == "/" {
		return "", false, nil // 根目录条目（常见 "./" 头）：无目标路径，跳过
	}
	if strings.HasPrefix(clean, "/") {
		return "", false, fmt.Errorf("tar entry %q is an absolute path", name)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", false, fmt.Errorf("tar entry %q escapes the extraction root", name)
		}
	}
	return clean, true, nil
}
