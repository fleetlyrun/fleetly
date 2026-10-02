// Package upload 承载上传产物（UploadSource，ADR-0019 附录 A，F1.10）的
// blob 面：内容寻址落盘（DataRoot/uploads/<sha256hex>，tmp + 原子 rename）、
// 单上传字节上限、tar 安全解包（tar-slip 防护）与孤儿 tmp 清扫。行事实住
// internal/state/sourceupload；受理与配额在 API 受理位（finalize 事务内）。
package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"time"
)

// 限额缺省（ADR-0019 附录 A.3）：单上传 512MiB、项目存量 4GiB。分帧
// chunk（≤1MiB）远小于 gRPC 收包上限 32MiB（assembly grpcMaxRecvMsgSize）
// ——单条消息永不撞传输层上限，总量上限由本包字节计数执法（诚实
// E_UPLOAD_TOO_LARGE，Q-11 限额错位教训）。
const (
	DefaultLimit        = 512 << 20
	DefaultProjectQuota = 4 << 30
)

// ErrTooLarge 是单上传超限的哨兵（API 面映射 E_UPLOAD_TOO_LARGE；信封带
// limit 与已收字节）。
type ErrTooLarge struct {
	Limit    int64
	Received int64
}

func (e *ErrTooLarge) Error() string {
	return fmt.Sprintf("upload exceeds the size limit: received %d bytes, limit %d", e.Received, e.Limit)
}

// Store 是上传 blob 面（root=DataRoot；limit/quota 非正值回退缺省，测试
// 注入小值）。
type Store struct {
	dataRoot string
	root     string
	limit    int64
	quota    int64
}

// NewStore 构造。
func NewStore(dataRoot string, limit, quota int64) *Store {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if quota <= 0 {
		quota = DefaultProjectQuota
	}
	return &Store{dataRoot: dataRoot, root: filepath.Join(dataRoot, "uploads"), limit: limit, quota: quota}
}

// Limit 返回单上传字节上限。
func (s *Store) Limit() int64 { return s.limit }

// Quota 返回项目存量字节配额。
func (s *Store) Quota() int64 { return s.quota }

// BlobPath 返回 digest 的 blob 路径（engine 构建链消费——解包真源；包级
// 函数使消费面无需构造 Store）。
func BlobPath(dataRoot, digest string) string {
	return filepath.Join(dataRoot, "uploads", digest)
}

// BlobPath 是 Store 实例形态（同包级函数）。
func (s *Store) BlobPath(digest string) string {
	return BlobPath(s.dataRoot, digest)
}

// BlobExists 报告 blob 是否在盘。
func (s *Store) BlobExists(digest string) bool {
	_, err := os.Stat(s.BlobPath(digest))
	return err == nil
}

// DeleteBlob 删除 blob（清扫路径；行 refcount 归零时由调用方触发）。
func (s *Store) DeleteBlob(digest string) error {
	if err := os.Remove(s.BlobPath(digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Receiver 是一次上传的接收态：tmp 文件 + sha256 + 字节计数。
type Receiver struct {
	store *Store
	file  *os.File
	hash  hash.Hash
	n     int64
}

// Begin 起一次接收（tmp-<ulid> 落盘；目录惰性创建）。
func (s *Store) Begin() (*Receiver, error) {
	if err := os.MkdirAll(s.root, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(s.root, "tmp-") //nolint:gosec // 数据根私有目录
	if err != nil {
		return nil, err
	}
	return &Receiver{store: s, file: f, hash: sha256.New()}, nil
}

// Write 追加一段字节（计数执法：超单上传上限 → *ErrTooLarge，tmp 留给
// Abort/延迟清扫）。
func (r *Receiver) Write(p []byte) error {
	if r.n+int64(len(p)) > r.store.limit {
		return &ErrTooLarge{Limit: r.store.limit, Received: r.n + int64(len(p))}
	}
	if _, err := r.file.Write(p); err != nil {
		return err
	}
	_, _ = r.hash.Write(p) // hash.Write 恒成功（接口契约）
	r.n += int64(len(p))
	return nil
}

// Abort 丢弃本次接收（关闭 + 删 tmp）。
func (r *Receiver) Abort() {
	_ = r.file.Close()
	_ = os.Remove(r.file.Name())
}

// Digest 返回当前内容摘要与已收字节数。
func (r *Receiver) Digest() (string, int64) {
	return hex.EncodeToString(r.hash.Sum(nil)), r.n
}

// FilePath 返回接收期 tmp 路径（观测/测试面）。
func (r *Receiver) FilePath() string { return r.file.Name() }

// Commit 收口一次接收：blob 已在（并发同内容或重传）→ dedup=true 删 tmp；
// 否则原子 rename 落位（Windows rename 不覆盖已存在目标——rename 失败后
// 回查存在性，并发竞态按 dedup 收口）。
func (s *Store) Commit(r *Receiver) (deduplicated bool, err error) {
	digest, _ := r.Digest()
	if err := r.file.Close(); err != nil {
		_ = os.Remove(r.file.Name())
		return false, err
	}
	blob := s.BlobPath(digest)
	if _, serr := os.Stat(blob); serr == nil {
		_ = os.Remove(r.file.Name())
		return true, nil
	}
	if err := os.Rename(r.file.Name(), blob); err != nil {
		// 并发同内容：另一 Commit 先落位（Windows rename 撞已存在目标）。
		if _, serr := os.Stat(blob); serr == nil {
			_ = os.Remove(r.file.Name())
			return true, nil
		}
		_ = os.Remove(r.file.Name())
		return false, err
	}
	return false, nil
}

// SweepOrphanTmp 清理孤儿 tmp（崩溃遗留；>age 未见 Commit/Abort）。返回
// 清理数供观测日志。
func (s *Store) SweepOrphanTmp(age time.Duration) (int, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-age)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !tmpPrefixMatch(e.Name()) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if rerr := os.Remove(filepath.Join(s.root, e.Name())); rerr == nil {
			n++
		}
	}
	return n, nil
}

// tmpPrefixMatch 报告是否 CreateTemp 的 tmp-<random> 前缀形态（blob 名是
// 64 位 hex，不以 tmp- 开头，不冲突）。
func tmpPrefixMatch(name string) bool {
	const prefix = "tmp-"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return false
	}
	for _, c := range name[len(prefix):] {
		if !isTmpChar(c) {
			return false
		}
	}
	return true
}

func isTmpChar(c rune) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z':
		return true
	default:
		return false
	}
}
