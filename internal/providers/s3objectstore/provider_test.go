package s3objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakeClient 是六操作缝的内存实现（并发安全——健康节拍与备份环可能同拍
// 探测）；错误面复用 minio.ErrorResponse，与生产客户端同构。
type fakeClient struct {
	mu      sync.Mutex
	objects map[string][]byte
	modTime map[string]time.Time
	exists  bool
	reachOK bool
}

func newFakeClient() *fakeClient {
	return &fakeClient{objects: map[string][]byte{}, modTime: map[string]time.Time{}, exists: true, reachOK: true}
}

func noSuchKey(key string) error {
	return minio.ErrorResponse{Code: "NoSuchKey", Key: key, StatusCode: 404}
}

func (f *fakeClient) Put(_ context.Context, key string, r io.Reader) (int64, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = body
	f.modTime[key] = time.Now().UTC()
	return int64(len(body)), nil
}

func (f *fakeClient) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.objects[key]
	if !ok {
		return nil, noSuchKey(key)
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

func (f *fakeClient) Stat(_ context.Context, key string) (capability.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, ok := f.objects[key]
	if !ok {
		return capability.ObjectInfo{}, noSuchKey(key)
	}
	return capability.ObjectInfo{Key: key, Size: int64(len(body)), ModTime: f.modTime[key]}, nil
}

func (f *fakeClient) List(_ context.Context, prefix string) ([]capability.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []capability.ObjectInfo
	for k, body := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, capability.ObjectInfo{Key: k, Size: int64(len(body)), ModTime: f.modTime[k]})
		}
	}
	return out, nil
}

func (f *fakeClient) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	// S3 DELETE 天然幂等：缺席对象同样 204（fake 与真实服务器同语义）。
	return nil
}

func (f *fakeClient) BucketExists(context.Context) (bool, error) {
	if !f.reachOK {
		return false, minio.ErrorResponse{Code: "InternalError", StatusCode: 500}
	}
	return f.exists, nil
}

func newProvider(t *testing.T) (*Provider, *fakeClient) {
	t.Helper()
	fc := newFakeClient()
	p := newWithClient(&capability.ObjectStoreS3Config{
		Endpoint: "minio.example.com:9000", Bucket: "fleetly-backups",
		AccessKeyID: "ak", SecretAccessKey: "sk",
	}, fc)
	return p, fc
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// roundtrip：Put 回执（流式 digest）→ Stat 元数据 → Get 体 → List 前缀
// → 幂等删 → 哨兵（与 localobjectstore 同构验收面）。
func TestPutGetListDeleteRoundTrip(t *testing.T) {
	p, _ := newProvider(t)
	ctx := context.Background()
	const k1 = "backups/shop/db1/20261004T000000Z-01HZ.archive"
	const k2 = "backups/shop/db2/20261004T000000Z-01HZ.archive"

	info1, err := p.Put(ctx, k1, strings.NewReader("payload-1"))
	require.NoError(t, err)
	assert.Equal(t, capability.ObjectInfo{Key: k1, Size: 9, Digest: sha256Hex("payload-1")}, info1)
	_, err = p.Put(ctx, k2, strings.NewReader("payload-2"))
	require.NoError(t, err)

	st, err := p.Stat(ctx, k1)
	require.NoError(t, err)
	assert.Equal(t, k1, st.Key)
	assert.Equal(t, int64(9), st.Size)
	assert.False(t, st.ModTime.IsZero())
	assert.Empty(t, st.Digest, "stat must not recompute the digest (port contract)")

	got, err := p.Get(ctx, k1)
	require.NoError(t, err)
	body, err := io.ReadAll(got)
	require.NoError(t, err)
	require.NoError(t, got.Close())
	assert.Equal(t, "payload-1", string(body))

	all, err := p.List(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, k1, all[0].Key) // 排序稳定（键字典序）
	assert.Equal(t, int64(9), all[0].Size)
	db1, err := p.List(ctx, "backups/shop/db1/")
	require.NoError(t, err)
	require.Len(t, db1, 1)
	assert.Equal(t, k1, db1[0].Key)

	require.NoError(t, p.Delete(ctx, k1))
	require.NoError(t, p.Delete(ctx, k1), "delete must be idempotent")
	all, err = p.List(ctx, "")
	require.NoError(t, err)
	assert.Len(t, all, 1)

	_, err = p.Get(ctx, k1)
	assert.ErrorIs(t, err, capability.ErrObjectNotFound)
	_, err = p.Stat(ctx, k1)
	assert.ErrorIs(t, err, capability.ErrObjectNotFound)
}

// Put 流式 digest 在大件上仍正确（分块读路径——tee 不漏字节）。
func TestPutDigestLargePayload(t *testing.T) {
	p, _ := newProvider(t)
	payload := strings.Repeat("fleetly-backup-chunk.", 100_000) // ~2.1MB，跨多读块
	info, err := p.Put(context.Background(), "backups/shop/db1/big.archive", strings.NewReader(payload))
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), info.Size)
	assert.Equal(t, sha256Hex(payload), info.Digest)
}

// 键钳制：路径穿越/盘符/反斜杠/空段一律拒绝（端口不信任调用方）。
func TestKeySanitizationRejectsTraversal(t *testing.T) {
	p, _ := newProvider(t)
	ctx := context.Background()
	for _, key := range []string{
		"../escape", "a/../../escape", "/absolute", "C:/win", `back\slash`,
		"a//b", "a/./b", "", ".",
	} {
		_, err := p.Put(ctx, key, strings.NewReader("x"))
		assert.Error(t, err, "key %q must be rejected", key)
		_, err = p.Get(ctx, key)
		assert.Error(t, err, "key %q must be rejected on Get", key)
		_, err = p.Stat(ctx, key)
		assert.Error(t, err, "key %q must be rejected on Stat", key)
		err = p.Delete(ctx, key)
		assert.Error(t, err, "key %q must be rejected on Delete", key)
	}
	// List 前缀同受钳制。
	_, err := p.List(ctx, "../escape")
	assert.Error(t, err)
}

// 入界取消：取消后 Put/Get 直接拒绝（不开无用请求）。
func TestCancelledContextRejectedAtEntry(t *testing.T) {
	p, _ := newProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Put(ctx, "backups/shop/db1/x.archive", strings.NewReader("x"))
	assert.ErrorIs(t, err, context.Canceled)
	_, err = p.Get(ctx, "backups/shop/db1/x.archive")
	assert.ErrorIs(t, err, context.Canceled)
}

// 健康：桶在场 = 健康；缺桶/不可达 = unhealthy + 处置面细节（英文）。
func TestHealthBucketStates(t *testing.T) {
	p, fc := newProvider(t)
	ctx := context.Background()

	rep := p.Health(ctx)
	assert.True(t, rep.Healthy)
	assert.Contains(t, rep.Details, "fleetly-backups")
	assert.Contains(t, rep.Details, "minio.example.com:9000")

	fc.exists = false
	rep = p.Health(ctx)
	assert.False(t, rep.Healthy)
	assert.Contains(t, rep.Details, "does not exist")
	assert.Contains(t, rep.Details, "platform_backup.s3.bucket")

	fc.exists, fc.reachOK = true, false
	rep = p.Health(ctx)
	assert.False(t, rep.Healthy)
	assert.Contains(t, rep.Details, "unreachable")
}

// Describe：能力面 + 凭证明文与存量对象归属的诚实边界。
func TestDescribeHonesty(t *testing.T) {
	p, _ := newProvider(t)
	desc := p.Describe()
	assert.Equal(t, "s3", desc.Name)
	assert.Equal(t, capability.KindObjectStore, desc.Capability)
	joined := strings.Join(desc.Notes, "\n")
	assert.Contains(t, joined, "external S3-compatible backup target")
	assert.Contains(t, joined, "plain text")
	assert.Contains(t, joined, "remain in the local backup directory")
}

// New 配置校验：五元组缺件 = 精确报错（点名缺失件，不提值）。
func TestNewRejectsIncompleteConfig(t *testing.T) {
	base := capability.ObjectStoreS3Config{
		Endpoint: "minio.example.com:9000", Bucket: "b",
		AccessKeyID: "ak", SecretAccessKey: "sk",
	}
	cases := []struct {
		name   string
		mutate func(*capability.ObjectStoreS3Config)
		want   string
	}{
		{"no endpoint", func(c *capability.ObjectStoreS3Config) { c.Endpoint = "" }, "endpoint"},
		{"no bucket", func(c *capability.ObjectStoreS3Config) { c.Bucket = "" }, "bucket"},
		{"no creds", func(c *capability.ObjectStoreS3Config) { c.SecretAccessKey = "" }, "access_key_id/secret_access_key"},
	}
	for _, c := range cases {
		cfg := base
		c.mutate(&cfg)
		_, err := New(&cfg)
		require.Error(t, err, c.name)
		assert.Contains(t, err.Error(), c.want, c.name)
		assert.Contains(t, err.Error(), "platform_backup.s3", c.name)
	}
	_, err := New(nil)
	assert.Error(t, err)
}

// 工厂：ctx 无配置 = 精确失败（装配期 fail-fast，不静默回退）。
func TestFactoryRequiresConfigInContext(t *testing.T) {
	_, err := factory(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform_backup.s3")

	cfg := &capability.ObjectStoreS3Config{
		Endpoint: "minio.example.com:9000", Bucket: "b",
		AccessKeyID: "ak", SecretAccessKey: "sk",
	}
	p, err := factory(capability.WithObjectStoreS3(context.Background(), cfg))
	require.NoError(t, err)
	assert.Equal(t, "s3", p.Describe().Name)
}

// 端点 scheme 语义与 restic 对齐：http:// 前缀 = 明文；无 scheme = TLS。
func TestEndpointSchemeSemantics(t *testing.T) {
	c, err := newMinioClient(&capability.ObjectStoreS3Config{
		Endpoint: "http://127.0.0.1:9000", Bucket: "b", AccessKeyID: "ak", SecretAccessKey: "sk",
	})
	require.NoError(t, err)
	assert.Equal(t, "http", c.cli.EndpointURL().Scheme)

	c, err = newMinioClient(&capability.ObjectStoreS3Config{
		Endpoint: "127.0.0.1:9000", Bucket: "b", AccessKeyID: "ak", SecretAccessKey: "sk",
	})
	require.NoError(t, err)
	assert.Equal(t, "https", c.cli.EndpointURL().Scheme)

	c, err = newMinioClient(&capability.ObjectStoreS3Config{
		Endpoint: "https://s3.example.com", Bucket: "b", AccessKeyID: "ak", SecretAccessKey: "sk",
	})
	require.NoError(t, err)
	assert.Equal(t, "https", c.cli.EndpointURL().Scheme)
}
