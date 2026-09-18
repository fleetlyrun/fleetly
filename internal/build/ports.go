package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// 端口定义（架构 §2.8 纪律：Go interface 端口在核心，第三方适配器在
// internal/substrate——moby 类型不出现在本包，适配器按签名隐式实现）。

// ErrImageNotFound 表示镜像在本机 daemon 不存在（preflight 的
// E_IMAGE_UNAVAILABLE 归因来源）。
var ErrImageNotFound = errors.New("image not found in local daemon")

// ImageInfo 是一次本机镜像可见性查询的结果。
type ImageInfo struct {
	// ID 是不可变镜像 ID（`sha256:<hex>` 配置摘要，D9 部署引用形态）。
	ID string
}

// ImageSource 是本机镜像可见性端口（T2.9 镜像身份：构建后取 digest、
// preflight 检查、digest→ref 登记的消费源）。
type ImageSource interface {
	// InspectImage 按引用检查本机镜像；缺失返回 ErrImageNotFound（适配器
	// 必须把底座 not-found 归一为该哨兵）。
	InspectImage(ctx context.Context, ref string) (ImageInfo, error)
	// LoadImage 把 docker-format tar 流装入本机 daemon（构建产物落本机；
	// 流由 buildkit docker 导出器客户端侧管道给出）。
	LoadImage(ctx context.Context, dockerTar io.Reader) error
}

// DaemonSpec 是平台自管 buildkitd 容器的目标形态（幂等收敛的期望态）。
type DaemonSpec struct {
	// Name 是容器名（缺省 fleetly-buildkit）。
	Name string
	// Image 是钉版镜像（moby/buildkit:v0.32.2）。
	Image string
	// MemoryBytes / NanoCPUs 是 cgroup 硬限额（Spike A E4 形态）。
	MemoryBytes int64
	NanoCPUs    int64
	// CacheVolume 是挂到容器内部工作缓存目录（InternalCacheMountPath）的
	// 命名卷——层缓存随容器重建不丢。空 = 无内部缓存挂载。
	CacheVolume string
}

// DaemonManager 是 buildkitd 容器编排端口（容器形态的平台自管；实现必须
// 幂等：镜像缺失拉取、容器缺失创建、未运行启动、已在运行 no-op）。
type DaemonManager interface {
	// EnsureImagePresent 确保镜像在本机 daemon 存在（缺失时拉取并排空
	// 响应流）。
	EnsureImagePresent(ctx context.Context, image string) error
	// EnsureVolumePresent 确保命名卷存在（幂等）。
	EnsureVolumePresent(ctx context.Context, name string) error
	// EnsureContainerRunning 把容器收敛到「存在且运行」。
	EnsureContainerRunning(ctx context.Context, spec DaemonSpec) error
}

// SecretsHash 计算构建凭证的稳定失效键（Spike A E2a 形态：缓存键含
// secrets-hash，任一凭证值变化即相关层失效；内容不变则哈希稳定命中）。
//
// 实现说明：railpack 自带 getSecretsHash 按 map 迭代序拼接值——多凭证时
// 顺序随机、哈希漂移（上游缺陷）。本实现按 key 排序拼接 `k=v\n`，多凭证
// 下确定；键集与键名变化同样失效。凭证值只进哈希、不落盘不进日志。
func SecretsHash(secrets map[string]string) string {
	pairs := make([]string, 0, len(secrets))
	for k, v := range secrets {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	h := sha256.New()
	for _, p := range pairs {
		h.Write([]byte(p))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CacheKeyPrefix 返回 railpack 缓存挂载键前缀（按应用稳定：同应用跨构建
// 复用 cache mount，跨应用隔离）。计算失败仅在 app 名非法时发生（调用方
// 已受控子集校验），失败回落固定前缀并携带错误说明。
func CacheKeyPrefix(app string) string {
	lowered := strings.ToLower(app)
	if !tagComponentRe.MatchString(lowered) {
		return "fleetly-invalid-app"
	}
	return "fleetly-" + lowered
}

// logTailBytes 是 E_BUILD_FAILED context 携带的日志尾部上限（stderr 尾部
// 进错误上下文——T2.8 验收「失败带 stderr 与建议」；4KiB 兼顾定位信息与
// 信封体积）。
const logTailBytes = 4096

// tailFile 返回文件内容尾部（至多 logTailBytes 字节；按行边界截齐，保证
// 信封里不出半行）。文件不存在返回空串。
func tailFile(path string) string {
	raw, err := os.ReadFile(path) //nolint:gosec // G304：path 是平台产物归档内的构建日志（E_BUILD_FAILED 尾部取证）
	if err != nil {
		return ""
	}
	if len(raw) > logTailBytes {
		raw = raw[len(raw)-logTailBytes:]
		// 截齐到行边界（丢弃残行首段）。
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		}
	}
	return string(raw)
}

// fmtErr 是包内错误包装的统一前缀。
func fmtErr(format string, args ...any) error {
	return fmt.Errorf("build: "+format, args...)
}
