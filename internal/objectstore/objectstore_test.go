package objectstore

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

// httptest 假 S3（hermetic，无容器依赖）：实现 minio-go 走通所需的最小
// 面——CreateBucket（PUT /<bucket>/）、BucketExists（HEAD /<bucket>）、
// GetBucketLocation（GET /<bucket>/?location）、对象 PUT/GET/HEAD/DELETE、
// ListObjectsV2（GET?list-type=2 最简 XML）。sigv4 头不校验签名本身，仅按
// AccessKey 比对实现 403 负路径；小对象（KB 级）单 PUT，不实现 multipart。

// fakeS3 是假 S3 服务器。
type fakeS3 struct {
	srv       *httptest.Server
	bucket    string
	objects   map[string][]byte
	created   bool
	accessKey string
}

// newFakeS3 起一个假 S3（bucket 预置为已存在=false；created 记录建桶调用）。
func newFakeS3(t *testing.T, accessKey string) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: map[string][]byte{}, accessKey: accessKey}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// endpoint 返回指向假服务器的 path-style 端点（127.0.0.1 主机名只能
// path-style，与 RustFS/MinIO 自建形态一致）。
func (f *fakeS3) endpoint(bucket string) Endpoint {
	return Endpoint{
		URL:       f.srv.URL,
		Bucket:    bucket,
		AccessKey: f.accessKey,
		SecretKey: "fake-secret",
		PathStyle: true,
	}
}

// s3Error 写一个 S3 错误 XML 响应（minio-go 据此构造 ErrorResponse）。
func s3Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: code, Message: message})
}

// checkAuth 按 Authorization 头里的 AccessKey 比对（不校验签名——假服务
// 只需要 403 负路径可达）。AccessKey 不符 → 403 AccessDenied。
func (f *fakeS3) checkAuth(r *http.Request) bool {
	return f.accessKey == "" || strings.Contains(r.Header.Get("Authorization"), f.accessKey)
}

// decodeBody 读请求体；aws-chunked 形态（Content-Encoding 头声明）剥掉
// 分块框架，还原对象字节。
func decodeBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return raw, nil
	}
	var out []byte
	for _, line := range strings.Split(string(raw), "\r\n") {
		if i := strings.IndexByte(line, ';'); i >= 0 && isHexSize(line[:i]) {
			continue // 分块尺寸行：<hex-size>;chunk-signature=...
		}
		if isHexSize(line) {
			continue // 分块尺寸行（无签名）
		}
		out = append(out, line...)
	}
	return out, nil
}

// isHexSize 判定整串是否全为 hex 数字（分块尺寸行特征）。
func isHexSize(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// handle 是假 S3 的路由（方法 + 路径形态分发）。
func (f *fakeS3) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-amz-request-id", "fake-request-id")
	if !f.checkAuth(r) {
		s3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied (fake: bad access key)")
		return
	}
	// minio-go 的桶级请求路径带尾斜杠（GET /<bucket>/?location 等）——
	// 统一剥掉一层尾斜杠再按桶名路由（对象键不以 / 结尾）。
	path := strings.TrimSuffix(r.URL.Path, "/")
	prefix := "/" + f.bucket
	switch {
	case r.Method == http.MethodGet && path == prefix && r.URL.Query().Has("location"):
		// GetBucketLocation：空 LocationConstraint = us-east-1（minio-go
		// 首次触达桶时的 region 解析）。
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w,
			`<?xml version="1.0" encoding="UTF-8"?>`+
				`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)

	case r.Method == http.MethodPut && path == prefix:
		// CreateBucket：PUT /<bucket>；已存在 → 409。
		if f.created {
			s3Error(w, http.StatusConflict, "BucketAlreadyOwnedByYou", "bucket already exists")
			return
		}
		f.created = true
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodHead && path == prefix:
		// BucketExists：HEAD /<bucket>。
		if !f.created {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && path == prefix && r.URL.Query().Get("list-type") == "2":
		// ListObjectsV2 最简 XML（全量一页，IsTruncated=false）。
		f.handleList(w, r)

	case strings.HasPrefix(path, prefix+"/") && len(path) > len(prefix)+1:
		f.handleObject(w, r, path)

	default:
		s3Error(w, http.StatusNotFound, "NoSuchKey", "fake: unmatched route "+r.Method+" "+r.URL.Path)
	}
}

// handleList 输出 ListObjectsV2 结果（prefix 过滤 + encoding-type=url 时
// 键做 URL 转义——minio-go 按 encoding 参数解码）。
func (f *fakeS3) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	encodeKey := func(k string) string { return k }
	if q.Get("encoding-type") == "url" {
		encodeKey = url.QueryEscape
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&b, "<Name>%s</Name>", f.bucket)
	fmt.Fprintf(&b, "<Prefix>%s</Prefix>", encodeKey(prefix))
	fmt.Fprintf(&b, "<KeyCount>%d</KeyCount>", len(keys))
	b.WriteString("<MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>")
	// EncodingType 必须随 encoding-type=url 请求回显——minio-go 按它决定
	// 是否对键做 URL 解码（decodeS3Name）。
	if q.Get("encoding-type") == "url" {
		b.WriteString("<EncodingType>url</EncodingType>")
	}
	for _, k := range keys {
		fmt.Fprintf(&b,
			"<Contents><Key>%s</Key><LastModified>2026-01-01T00:00:00.000Z</LastModified>"+
				"<ETag>&#34;fake-etag&#34;</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>",
			encodeKey(k), len(f.objects[k]))
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, b.String())
}

// handleObject 处理对象级 PUT/GET/HEAD/DELETE。
func (f *fakeS3) handleObject(w http.ResponseWriter, r *http.Request, path string) {
	key := strings.TrimPrefix(path, "/"+f.bucket+"/")
	if !f.created {
		s3Error(w, http.StatusNotFound, "NoSuchBucket", "bucket does not exist")
		return
	}
	switch r.Method {
	case http.MethodPut:
		data, err := decodeBody(r)
		if err != nil {
			s3Error(w, http.StatusBadRequest, "IncompleteBody", err.Error())
			return
		}
		f.objects[key] = data
		w.Header().Set("ETag", `"fake-etag"`)
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			s3Error(w, http.StatusNotFound, "NoSuchKey", "object not found")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("ETag", `"fake-etag"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case http.MethodHead:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("ETag", `"fake-etag"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "fake: unsupported method")
	}
}

// TestOperationSurface 全操作面往返：EnsureBucket 幂等、Put/Get/Stat/
// Delete/List 逐一走真实 HTTP 往返（操作面即 conformance 面，设计 §2.7）。
func TestOperationSurface(t *testing.T) {
	f := newFakeS3(t, "test-access-key")
	f.bucket = "conformance"
	ctx := context.Background()
	cl, err := New(ctx, f.endpoint("conformance"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// EnsureBucket 幂等：首建 + 重复调用都成功。
	if err := cl.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket (create): %v", err)
	}
	if !f.created {
		t.Fatal("bucket was not created by EnsureBucket")
	}
	if err := cl.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket (idempotent): %v", err)
	}

	// Put → Get 逐字节一致。
	payload := []byte("fleetly conformance payload \x00\x01 bytes")
	if err := cl.Put(ctx, "statebackups/a/b.bin", payload, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := cl.Get(ctx, "statebackups/a/b.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("Get roundtrip mismatch: wrote %d bytes, read %d bytes", len(payload), len(got))
	}

	// Stat 元数据。
	info, err := cl.Stat(ctx, "statebackups/a/b.bin")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Key != "statebackups/a/b.bin" || info.Size != int64(len(payload)) {
		t.Fatalf("Stat = %+v, want key=statebackups/a/b.bin size=%d", info, len(payload))
	}

	// List 前缀（递归）：两条命中前缀，一条不命中。
	if err := cl.Put(ctx, "statebackups/a/c.bin", []byte("second"), ""); err != nil {
		t.Fatalf("Put (second): %v", err)
	}
	if err := cl.Put(ctx, "other/ignored.bin", []byte("nope"), ""); err != nil {
		t.Fatalf("Put (other): %v", err)
	}
	rows, err := cl.List(ctx, "statebackups/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List prefix statebackups/ returned %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Key != "statebackups/a/b.bin" || rows[1].Key != "statebackups/a/c.bin" {
		t.Fatalf("List keys = [%s, %s], want sorted statebackups keys", rows[0].Key, rows[1].Key)
	}

	// Delete 后 Get 404。
	if err := cl.Delete(ctx, "statebackups/a/b.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cl.Get(ctx, "statebackups/a/b.bin"); err == nil {
		t.Fatal("Get after Delete should fail (NoSuchKey)")
	}
}

// TestEndpointValidate 端点形状校验负路径（scheme/host/bucket 缺失）。
func TestEndpointValidate(t *testing.T) {
	cases := []struct {
		name string
		ep   Endpoint
	}{
		{"empty url", Endpoint{Bucket: "b"}},
		{"no scheme", Endpoint{URL: "s3.example.com", Bucket: "b"}},
		{"bad scheme", Endpoint{URL: "ftp://s3.example.com", Bucket: "b"}},
		{"no host", Endpoint{URL: "http:///path", Bucket: "b"}},
		{"empty bucket", Endpoint{URL: "http://127.0.0.1:9000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.ep.Validate(); err == nil {
				t.Fatalf("Validate on %+v should fail", tc.ep)
			}
			if _, err := New(context.Background(), tc.ep); err == nil {
				t.Fatalf("New on %+v should fail", tc.ep)
			}
		})
	}
}

// TestTestConnectionSuccess 探针正路径：put→get→delete 全 OK，读回字节
// 与写入一致，且探针对象已删除（List 无残留）。
func TestTestConnectionSuccess(t *testing.T) {
	f := newFakeS3(t, "test-access-key")
	f.bucket = "probe-bucket"
	f.created = true
	res := TestConnection(context.Background(), f.endpoint("probe-bucket"))
	if !res.OK {
		t.Fatalf("probe should pass: %+v", res)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("probe steps = %d, want 3 (put/get/delete)", len(res.Steps))
	}
	for _, st := range res.Steps {
		if !st.OK {
			t.Fatalf("step %s failed: %s", st.Step, st.Err)
		}
	}
	// 探针对象不残留。
	cl, err := New(context.Background(), f.endpoint("probe-bucket"))
	if err != nil {
		t.Fatalf("New (residual check): %v", err)
	}
	rows, err := cl.List(context.Background(), "fleetly-probe-")
	if err != nil {
		t.Fatalf("List (residual check): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("probe object left behind: %+v", rows)
	}
}

// TestTestConnectionBadCredentials 403 负路径：错凭据在 put 步如实失败，
// 失败步与底层错误摘要进结构化结果（能认证不成立 → 后续步短路）。
func TestTestConnectionBadCredentials(t *testing.T) {
	f := newFakeS3(t, "correct-access-key")
	f.bucket = "denied"
	f.created = true
	ep := f.endpoint("denied")
	ep.AccessKey = "wrong-access-key" // 与假服务器期待的 AccessKey 不符 → 403
	res := TestConnection(context.Background(), ep)
	if res.OK {
		t.Fatal("probe with wrong credentials must fail")
	}
	if res.FailedStep != ProbeStepPut {
		t.Fatalf("failed step = %s, want put", res.FailedStep)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("steps = %d, want 1 (short-circuit after put failure)", len(res.Steps))
	}
	if !strings.Contains(res.Steps[0].Err, "Access Denied") {
		t.Fatalf("underlying error should surface the access denial, got: %s", res.Steps[0].Err)
	}
	// endpoint 回显、secret 不回显（结构化结果脱敏契约）。
	if res.EndpointURL != ep.URL || res.Bucket != "denied" {
		t.Fatalf("probe echo mismatch: %+v", res)
	}
	for _, st := range res.Steps {
		if strings.Contains(st.Err, ep.SecretKey) {
			t.Fatal("probe error text must not contain the secret")
		}
	}
}

// TestTestConnectionRefused 连接拒绝负路径：端点不可达在 put 步如实失败。
func TestTestConnectionRefused(t *testing.T) {
	// 占住端口后立即关闭——得到一个确定「连接拒绝」的地址。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	ep := Endpoint{URL: "http://" + addr, Bucket: "any", AccessKey: "k", SecretKey: "s", PathStyle: true}
	res := TestConnection(context.Background(), ep)
	if res.OK {
		t.Fatal("probe against a closed endpoint must fail")
	}
	if res.FailedStep != ProbeStepPut {
		t.Fatalf("failed step = %s, want put", res.FailedStep)
	}
	if !strings.Contains(res.Steps[0].Err, "connect") {
		t.Fatalf("underlying error should mention connect, got: %s", res.Steps[0].Err)
	}
}

// TestTestConnectionReadbackMismatch 读回不一致负路径：探针在 get 步失败
// （诚实契约的「能读回」不是「有响应就行」——逐字节比对）。
func TestTestConnectionReadbackMismatch(t *testing.T) {
	f := newFakeS3(t, "test-access-key")
	f.bucket = "corrupt"
	f.created = true
	// 埋一个会篡改 PUT 字节的假服务器：探针 PUT 静默损坏存储结果（模拟
	// 「有响应但内容错」的端点），其余请求走原 handler。
	inner := http.HandlerFunc(f.handle)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/fleetly-probe-") {
			data, err := decodeBody(r)
			if err == nil && len(data) > 4 {
				data = append([]byte("XXXX"), data[4:]...)
			}
			f.objects[strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")] = data
			w.Header().Set("ETag", `"tampered"`)
			w.WriteHeader(http.StatusOK)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	ep := f.endpoint("corrupt")
	ep.URL = srv.URL
	res := TestConnection(context.Background(), ep)
	if res.OK {
		t.Fatal("probe must fail when readback does not match")
	}
	if res.FailedStep != ProbeStepGet {
		t.Fatalf("failed step = %s, want get", res.FailedStep)
	}
	if !strings.Contains(res.Steps[1].Err, "readback mismatch") {
		t.Fatalf("underlying error should mention readback mismatch, got: %s", res.Steps[1].Err)
	}
}
