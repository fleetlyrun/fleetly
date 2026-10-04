// Package s3objectstore 实现 ObjectStore Capability 的外置 S3 兼容 Provider
// （F2.8，ADR-0042）：目标是 platform_backup.s3 五元组指向的远端桶，对象键
// 落桶根 backups/ 命名空间（prefix 归 restic 仓，本 Provider 不消费）。
// 行为语义与 localobjectstore 同构（端口契约的 adapter 执法者）：safeKey
// 钳制、Put 流式 sha256 回执、Get/Stat 缺键哨兵、Delete 幂等、List 稳定排序。
package s3objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// client 是 S3 客户端六操作缝（resticExec/fileSync 函数值缝的接口形态）：
// 单测注入内存 fake 钉行为语义（键钳制/digest/哨兵映射/幂等），真实
// minio-go 链路由 dind e2e 对真 MinIO 覆盖。
type client interface {
	// Put 上传对象（未知尺寸流式）并回执字节数。
	Put(ctx context.Context, key string, r io.Reader) (int64, error)
	// Get 打开读流（缺键须即时报错——端口哨兵契约要求 Get 不惰性）。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat 回读元数据（不读体）。
	Stat(ctx context.Context, key string) (capability.ObjectInfo, error)
	// List 列举前缀下对象（前缀空 = 全部）。
	List(ctx context.Context, prefix string) ([]capability.ObjectInfo, error)
	// Remove 删除对象。
	Remove(ctx context.Context, key string) error
	// BucketExists 探测目标桶在场（健康面）。
	BucketExists(ctx context.Context) (bool, error)
}

// Provider 是外置 S3 兼容 ObjectStore。
type Provider struct {
	client   client
	endpoint string
	bucket   string
}

// 编译期契约断言。
var _ capability.ObjectStore = (*Provider)(nil)

// New 按 ADR-0042 配置面构造 Provider（minio-go 客户端；端点 scheme 语义
// 与 restic 对齐：http:// 前缀 = 明文，无 scheme / https:// = TLS）。
func New(cfg *capability.ObjectStoreS3Config) (*Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("s3objectstore: config is required")
	}
	var missing []string
	if cfg.Endpoint == "" {
		missing = append(missing, "endpoint")
	}
	if cfg.Bucket == "" {
		missing = append(missing, "bucket")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		missing = append(missing, "access_key_id/secret_access_key")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("s3objectstore: platform_backup.s3 is incomplete (missing %s); fill the five-tuple or remove it to keep the local backup target", strings.Join(missing, ", "))
	}
	c, err := newMinioClient(cfg)
	if err != nil {
		return nil, err
	}
	return newWithClient(cfg, c), nil
}

// newWithClient 是可测构造（缝注入面）。
func newWithClient(cfg *capability.ObjectStoreS3Config, c client) *Provider {
	return &Provider{client: c, endpoint: cfg.Endpoint, bucket: cfg.Bucket}
}

// minioClient 是六操作缝的 minio-go 生产实现。
type minioClient struct {
	cli    *minio.Client
	bucket string
}

func newMinioClient(cfg *capability.ObjectStoreS3Config) (*minioClient, error) {
	secure := !strings.HasPrefix(strings.ToLower(cfg.Endpoint), "http://")
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("s3objectstore: endpoint %q: %w", cfg.Endpoint, err)
	}
	return &minioClient{cli: cli, bucket: cfg.Bucket}, nil
}

func (m *minioClient) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	// size=-1：未知尺寸流式 multipart（内存界 ~64MiB/部件，磁盘零落；失败
	// 整体 abort——无半对象，ADR-0042 决策 6）。
	info, err := m.cli.PutObject(ctx, m.bucket, key, r, -1, minio.PutObjectOptions{})
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (m *minioClient) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := m.cli.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// minio-go 的 GetObject 惰性（GET 在首 Read 才发）；先 HEAD 一次把缺键
	// 即时暴露——端口契约要求 Get 当场返回哨兵，不把错误推迟到读流。
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, err
	}
	return obj, nil
}

func (m *minioClient) Stat(ctx context.Context, key string) (capability.ObjectInfo, error) {
	info, err := m.cli.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	return capability.ObjectInfo{Key: info.Key, Size: info.Size, ModTime: info.LastModified}, nil
}

func (m *minioClient) List(ctx context.Context, prefix string) ([]capability.ObjectInfo, error) {
	var out []capability.ObjectInfo
	for info := range m.cli.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if info.Err != nil {
			return nil, info.Err
		}
		out = append(out, capability.ObjectInfo{Key: info.Key, Size: info.Size, ModTime: info.LastModified})
	}
	return out, nil
}

func (m *minioClient) Remove(ctx context.Context, key string) error {
	return m.cli.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}

func (m *minioClient) BucketExists(ctx context.Context) (bool, error) {
	return m.cli.BucketExists(ctx, m.bucket)
}

// Describe 实现 Provider 契约三件套之一。Notes 承载 ADR-0042 的诚实边界：
// 凭证明文落点与切换前存量对象的归属。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "s3",
		Capability: capability.KindObjectStore,
		Version:    "1",
		Notes: []string{
			fmt.Sprintf("external S3-compatible backup target %s (bucket %s), configured via platform_backup.s3 (ADR-0042)", p.endpoint, p.bucket),
			"the access key lives in plain text in the daemon config (0600) — prefer a dedicated low-privilege key",
			"objects written before this provider was selected remain in the local backup directory",
		},
	}
}

// Health 实现 Provider 契约三件套之一（目标桶可达且在场；不代建——缺桶 =
// unhealthy + 处置指引）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	exists, err := p.client.BucketExists(ctx)
	switch {
	case err != nil:
		return capability.HealthReport{Healthy: false, Details: fmt.Sprintf("s3 target %s unreachable: %v", p.endpoint, err)}
	case !exists:
		return capability.HealthReport{Healthy: false, Details: fmt.Sprintf("bucket %q does not exist at %s — create it or fix platform_backup.s3.bucket", p.bucket, p.endpoint)}
	default:
		return capability.HealthReport{Healthy: true, Details: fmt.Sprintf("bucket %q reachable at %s", p.bucket, p.endpoint)}
	}
}

// Put 上传一个对象并回执元数据（TeeReader 流式铸 sha256——digest 是 verify
// 锚；ctx 入界检查，端口契约同 localobjectstore）。
func (p *Provider) Put(ctx context.Context, key string, r io.Reader) (capability.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return capability.ObjectInfo{}, err // 入界检查（端口契约；远端写经 multipart 流式，无长挂面）
	}
	k, err := safeKey(key)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	h := sha256.New()
	size, err := p.client.Put(ctx, k, io.TeeReader(r, h))
	if err != nil {
		return capability.ObjectInfo{}, fmt.Errorf("s3objectstore: put %q: %w", k, err)
	}
	return capability.ObjectInfo{Key: k, Size: size, Digest: hex.EncodeToString(h.Sum(nil))}, nil
}

// Get 下载一个对象（ctx 取消即断流——minio-go 读流携带 Get ctx；调用方
// 负责 Close）。缺键即时返回哨兵（client 缝契约）。
func (p *Provider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // 入界取消：不开无用请求
	}
	k, err := safeKey(key)
	if err != nil {
		return nil, err
	}
	rc, err := p.client.Get(ctx, k)
	if err != nil {
		return nil, wrapIfNotFound(k, err)
	}
	return rc, nil
}

// Stat 回读单个对象的元数据（不读体、不重算 digest——端口契约）。
func (p *Provider) Stat(ctx context.Context, key string) (capability.ObjectInfo, error) {
	k, err := safeKey(key)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	info, err := p.client.Stat(ctx, k)
	if err != nil {
		return capability.ObjectInfo{}, wrapIfNotFound(k, err)
	}
	info.Key = k
	return info, nil
}

// List 列举前缀下对象（含元数据；前缀空 = 全部，尾斜杠剥后钳制，排序
// 稳定——S3 键序本就字典序，sort 兜底保证契约不依赖实现面）。
func (p *Provider) List(ctx context.Context, prefix string) ([]capability.ObjectInfo, error) {
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix != "" {
			cleaned, err := safeKey(prefix)
			if err != nil {
				return nil, err
			}
			prefix = cleaned
		}
	}
	out, err := p.client.List(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("s3objectstore: list %q: %w", prefix, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Delete 删除对象（幂等：不存在不报错——S3 DELETE 天然幂等，NoSuchKey
// 形态同样放行，保留策略执行器按清单滚动不被单键迟到缺失阻断）。
func (p *Provider) Delete(ctx context.Context, key string) error {
	k, err := safeKey(key)
	if err != nil {
		return err
	}
	if err := p.client.Remove(ctx, k); err != nil && !isNoSuchKey(err) {
		return fmt.Errorf("s3objectstore: delete %q: %w", k, err)
	}
	return nil
}

// isNoSuchKey 报告错误是否缺键形态（S3NoSuchKey / ObjectDoesNotExist 是
// minio-go 对 GET/HEAD 404 的两种码面；fake 缝复用同结构）。
func isNoSuchKey(err error) bool {
	var resp minio.ErrorResponse
	if !errors.As(err, &resp) {
		return false
	}
	return resp.Code == "NoSuchKey" || resp.Code == "ObjectDoesNotExist"
}

// wrapIfNotFound 把缺键错误换成端口哨兵形态（键名入报文，其余错误原样）。
func wrapIfNotFound(key string, err error) error {
	if isNoSuchKey(err) {
		return fmt.Errorf("s3objectstore: object %q: %w", key, capability.ErrObjectNotFound)
	}
	return err
}

// safeKey 钳制对象键（localobjectstore 同款规则的复刻——providers 互不
// import，重复是纪律的代价；端口契约：正斜杠分层、每段非空、禁穿越段/
// 反斜杠/Windows 盘符形态。S3 键虽无路径语义，穿越形态仍是攻击面：列举
// 前缀与跨 adapter 迁移面共享同一键语法）。
func safeKey(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("s3objectstore: key must not be empty")
	}
	if strings.Contains(key, "\\") {
		return "", fmt.Errorf("s3objectstore: key %q must use forward slashes only", key)
	}
	if len(key) >= 2 && key[1] == ':' {
		return "", fmt.Errorf("s3objectstore: key %q looks like a windows drive path", key)
	}
	parts := strings.Split(key, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("s3objectstore: key %q has an empty or traversing segment", key)
		}
	}
	return key, nil
}

// factory 是注册进能力注册表的工厂体（ADR-0042 决策 2：五元组经装配 ctx
// 注入——config 是唯一契约源，无 env 旧通道；具名是可测面）。
func factory(ctx context.Context) (capability.Provider, error) {
	cfg := capability.ObjectStoreS3FromContext(ctx)
	if cfg == nil {
		return nil, fmt.Errorf("s3objectstore: platform_backup.s3 is not configured; set the s3 five-tuple or keep the default local backup target")
	}
	return New(cfg)
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。
func init() {
	capability.RegisterFactory(capability.KindObjectStore, "s3", factory)
}
