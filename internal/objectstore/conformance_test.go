package objectstore

// FZ-7 conformance（E3-7，设计 docs/design/2026-09-20-object-storage.md
// §2.7）：封闭操作面（§2.1：EnsureBucket/Put/Get/Stat/Delete/List +
// TestConnection 探针协议 + path-style）对**真实 RustFS 实例**的全量断言。
// 语义：平台对「S3 兼容端点」的全部依赖面被 RustFS 证真；外部端点
// （AWS/MinIO/B2…）由用户侧 TestConnection 自测覆盖，CI 不 mock 外部云。
//
// 环境门控（本地默认零成本 skip；CI nightly 的 conformance-objectstore job
// 起 pinned RustFS 容器并注入 env）：
//
//	FLEETLY_CONFORMANCE_S3_ENDPOINT    必设（如 http://127.0.0.1:9000）；
//	                                   未设 = t.Skip（go test ./... 零影响）。
//	FLEETLY_CONFORMANCE_S3_ACCESS_KEY  必设。
//	FLEETLY_CONFORMANCE_S3_SECRET_KEY  必设。
//	FLEETLY_CONFORMANCE_S3_BUCKET      缺省 fleetly-conformance。
//	FLEETLY_CONFORMANCE_S3_REGION      缺省空（path-style 自建端点不带）。
//	FLEETLY_CONFORMANCE_S3_PATH_STYLE  缺省 true（RustFS/MinIO 类自建端点）。
//
// 测试内不起容器（容器由 CI 编排）、纯客户端面；键空间收敛在
// conformance/<run>/ 前缀下且 delete 收尾——重复执行幂等，不在被测端点留
// 除桶外的常驻物。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// conformanceEnv 是门控 env 的解析结果（Endpoint + 是否门开）。
type conformanceEnv struct {
	endpoint Endpoint
}

// conformanceEndpointFromEnv 从进程 env 组装被测端点；endpoint 未设返回
// nil（= 本地 skip 路径）。
func conformanceEndpointFromEnv() *conformanceEnv {
	ep := os.Getenv("FLEETLY_CONFORMANCE_S3_ENDPOINT")
	if strings.TrimSpace(ep) == "" {
		return nil
	}
	access := os.Getenv("FLEETLY_CONFORMANCE_S3_ACCESS_KEY")
	secret := os.Getenv("FLEETLY_CONFORMANCE_S3_SECRET_KEY")
	bucket := os.Getenv("FLEETLY_CONFORMANCE_S3_BUCKET")
	if bucket == "" {
		bucket = "fleetly-conformance"
	}
	pathStyle := true
	if raw := os.Getenv("FLEETLY_CONFORMANCE_S3_PATH_STYLE"); raw != "" {
		pathStyle, _ = strconv.ParseBool(raw)
	}
	return &conformanceEnv{endpoint: Endpoint{
		URL:       ep,
		Region:    os.Getenv("FLEETLY_CONFORMANCE_S3_REGION"),
		Bucket:    bucket,
		AccessKey: access,
		SecretKey: secret,
		PathStyle: pathStyle,
	}}
}

// conformanceRunID 是本轮运行的键前缀段（随机——CI 容器复用/重跑不互相
// 踩键；delete 收尾后除前缀列举可见物外零残留）。
func conformanceRunID(t *testing.T) string {
	t.Helper()
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate run id: %v", err)
	}
	return hex.EncodeToString(raw[:])
}

// newConformanceClient 构造被测客户端（门控 env → skip 或客户端）。
func newConformanceClient(t *testing.T) (*Client, *conformanceEnv) {
	t.Helper()
	env := conformanceEndpointFromEnv()
	if env == nil {
		t.Skip("FLEETLY_CONFORMANCE_S3_ENDPOINT not set (local default: skip; the nightly conformance-objectstore job injects it)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	cli, err := New(ctx, env.endpoint)
	if err != nil {
		t.Fatalf("construct client: %v", err)
	}
	if err := cli.EnsureBucket(ctx); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	return cli, env
}

// TestConformanceEnsureBucketIdempotent EnsureBucket 幂等：连续两次调用
// 不炸、第二次是 no-op（存在即不建）——启用面的自动建桶语义（§2.1）。
func TestConformanceEnsureBucketIdempotent(t *testing.T) {
	cli, _ := newConformanceClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cli.EnsureBucket(ctx); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := cli.EnsureBucket(ctx); err != nil {
		t.Fatalf("second ensure must be a no-op, got: %v", err)
	}
}

// TestConformancePutGetStatDeleteList 操作面主链：put→get 字节往返（含
// 0x00/多字节 UTF-8 的二进制安全）→ stat 元数据（size/etag）→ list 前缀
// （递归、越界前缀隔离）→ delete → get 负路径（删后必 404 类错误）。
func TestConformancePutGetStatDeleteList(t *testing.T) {
	cli, _ := newConformanceClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := conformanceRunID(t)
	prefix := "conformance/" + run + "/"

	// ① put/get 字节往返：二进制安全（0x00 段 + 0xFF 段 + 多字节 UTF-8）。
	payload := bytes.Join([][]byte{
		[]byte("fleetly-conformance \xe2\x9c\x93 "),
		{0x00, 0x01, 0xFE, 0xFF, 0x00},
		[]byte(fmt.Sprintf(" run=%s t=%d", run, time.Now().UnixNano())),
	}, nil)
	key := prefix + "roundtrip.bin"
	if err := cli.Put(ctx, key, payload, "application/octet-stream"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := cli.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip mismatch: wrote %d bytes, read %d bytes and content differs", len(payload), len(got))
	}

	// ② stat 元数据：size 精确、etag 非空、LastModified 可解析（零值 = 未回）。
	stat, err := cli.Stat(ctx, key)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if stat.Size != int64(len(payload)) {
		t.Fatalf("stat size = %d, want %d", stat.Size, len(payload))
	}
	if stat.ETag == "" {
		t.Fatal("stat etag is empty (metadata face incomplete)")
	}
	if stat.LastModified.IsZero() {
		t.Fatal("stat last_modified is zero (metadata face incomplete)")
	}

	// ③ list 前缀：三对象两前缀——本 run 前缀恰见 2 个（roundtrip.bin +
	// nested/inner.txt），兄弟前缀不串台；递归形态含 nested 层。
	if err := cli.Put(ctx, prefix+"nested/inner.txt", []byte("inner"), "text/plain"); err != nil {
		t.Fatalf("put nested: %v", err)
	}
	if err := cli.Put(ctx, "conformance/"+run+"-sibling/outside.txt", []byte("outside"), "text/plain"); err != nil {
		t.Fatalf("put sibling: %v", err)
	}
	list, err := cli.List(ctx, prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	keys := map[string]bool{}
	for _, info := range list {
		keys[info.Key] = true
	}
	if !keys[key] || !keys[prefix+"nested/inner.txt"] {
		t.Fatalf("list prefix %s = %v, want both seeded keys (recursive)", prefix, list)
	}
	if keys["conformance/"+run+"-sibling/outside.txt"] {
		t.Fatalf("list prefix %s leaked the sibling prefix (prefix isolation broken)", prefix)
	}

	// ④ delete → get 负路径：删除成功后读取必须显式失败（不存在被冒充为
	// 空读——诚实契约）。
	if err := cli.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cli.Get(ctx, key); err == nil {
		t.Fatal("get after delete must fail (missing object must surface as an error)")
	}
	if _, err := cli.Stat(ctx, key); err == nil {
		t.Fatal("stat after delete must fail")
	}

	// 收尾：清掉本 run 前缀（nested 对象）；sibling 前缀同清——重复执行
	// 后端点零残留（桶保留，桶是 env 声明的被测物）。
	for _, k := range []string{prefix + "nested/inner.txt", "conformance/" + run + "-sibling/outside.txt"} {
		if err := cli.Delete(ctx, k); err != nil {
			t.Fatalf("cleanup delete %s: %v", k, err)
		}
	}
}

// TestConformanceProbeProtocol 探针协议（诚实契约）：TestConnection 全绿
// = put→get→delete 三步 OK 且读回逐字节比对（探针对象键 fleetly-probe-*，
// 平台各消费面共用该协议）。
func TestConformanceProbeProtocol(t *testing.T) {
	_, env := newConformanceClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res := TestConnection(ctx, env.endpoint)
	if !res.OK {
		t.Fatalf("probe must pass against the conformance endpoint: failed step %q: %+v",
			res.FailedStep, res.Steps)
	}
	if n := len(res.Steps); n != 3 {
		t.Fatalf("probe steps = %d, want exactly put/get/delete", n)
	}
	for _, step := range res.Steps {
		if !step.OK {
			t.Fatalf("probe step %s reported not-ok on a green probe: %+v", step.Step, step)
		}
	}
	if res.EndpointURL != env.endpoint.URL || res.Bucket != env.endpoint.Bucket || !res.PathStyle {
		t.Fatalf("probe echo mismatch: %+v (endpoint %s bucket %s)", res, env.endpoint.URL, env.endpoint.Bucket)
	}
}

// TestConformancePathStyleBehavior path-style 行为：PathStyle=true 的客户端
// 以 <host>/<bucket>/<key> 形态访问（RustFS/MinIO 类自建端点的唯一可用寻
// 址式）。操作面在此形态下全链走通即证真（虚拟主机式寻址不在平台自建端
// 点消费面内）；负向对照：错误凭据必须 403 类显式失败（「能认证」是探测
// 语义的一部分——§2.1）。
func TestConformancePathStyleBehavior(t *testing.T) {
	env := conformanceEndpointFromEnv()
	if env == nil {
		t.Skip("FLEETLY_CONFORMANCE_S3_ENDPOINT not set (local default: skip; the nightly conformance-objectstore job injects it)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if !env.endpoint.PathStyle {
		t.Skip("conformance endpoint is not path-style (FLEETLY_CONFORMANCE_S3_PATH_STYLE=false); RustFS 代演恒 path-style")
	}
	run := conformanceRunID(t)
	cli, err := New(ctx, env.endpoint)
	if err != nil {
		t.Fatalf("construct client: %v", err)
	}
	key := "conformance/" + run + "/pathstyle.txt"
	if err := cli.Put(ctx, key, []byte("path-style"), "text/plain"); err != nil {
		t.Fatalf("path-style put: %v", err)
	}
	got, err := cli.Get(ctx, key)
	if err != nil || string(got) != "path-style" {
		t.Fatalf("path-style get = %q, %v; want \"path-style\", nil", got, err)
	}
	if err := cli.Delete(ctx, key); err != nil {
		t.Fatalf("path-style delete: %v", err)
	}

	// 403 负路径：错凭据客户端的 put 必须显式失败（服务端拒绝，不静默）。
	bad := env.endpoint
	bad.AccessKey = "fleetly-wrong-access-key"
	bad.SecretKey = "fleetly-wrong-secret-key"
	badCli, err := New(ctx, bad)
	if err != nil {
		t.Fatalf("construct bad-credential client: %v", err)
	}
	if err := badCli.Put(ctx, key, []byte("must-not-land"), "text/plain"); err == nil {
		t.Fatal("put with wrong credentials must fail (403 negative path)")
	}
	// 探针面同判：错凭据探针 = 首步（put）失败的结构化结果。
	probe := TestConnection(ctx, bad)
	if probe.OK {
		t.Fatal("probe with wrong credentials must not pass")
	}
	if probe.FailedStep != ProbeStepPut {
		t.Fatalf("probe with wrong credentials failed at %q, want the put step", probe.FailedStep)
	}
}
