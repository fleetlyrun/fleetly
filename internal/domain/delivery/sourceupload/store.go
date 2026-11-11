package sourceupload

// store.go 是上传 blob 面的领域契约（ADR-0019 附录 A）：路径公式、超限
// 哨兵、接收/存储接口住 domain；文件系统实现住 infrastructure/upload，
// 组合根注入。interfaces 与 application 只依赖本契约，不再 import 基础
// 设施实现包。

import (
	"fmt"
	"io"
	"path/filepath"
)

// 上传限额缺省（ADR-0019 附录 A；非正值由实现回退到本值）。
const (
	DefaultLimit        = 512 << 20
	DefaultProjectQuota = 4 << 30
)

// BlobPath 返回 digest 的 blob 路径（DataRoot/uploads/<sha256hex>）。
// 包级纯公式：构建链解包与存储实现共用同一布局。
func BlobPath(dataRoot, digest string) string {
	return filepath.Join(dataRoot, "uploads", digest)
}

// ErrTooLarge 是单上传超限的哨兵（API 面映射 E_UPLOAD_TOO_LARGE；信封带
// 限额与实际收到字节数）。
type ErrTooLarge struct {
	Limit    int64
	Received int64
}

func (e *ErrTooLarge) Error() string {
	return fmt.Sprintf("upload exceeds the size limit: received %d bytes, limit %d", e.Received, e.Limit)
}

// Receiver 是一次上传的接收态契约：追加字节、丢弃、取内容摘要与已收
// 字节数。实现负责 tmp 文件与哈希；调用方不感知存储布局。
type Receiver interface {
	Write([]byte) error
	Abort()
	Digest() (string, int64)
}

// Store 是上传 blob 面的 API 消费契约（gRPC 流式上传面）。
type Store interface {
	Begin() (Receiver, error)
	Commit(Receiver) (bool, error)
	BlobExists(digest string) bool
	Quota() int64
}

// BuildSourceStore 是构建源消费契约（intake 解包面）：按 digest 定位
// blob，并把 tar 流安全解包到目标目录。
type BuildSourceStore interface {
	BlobPath(digest string) string
	ExtractTar(dst string, src io.Reader, limit int64) error
}
