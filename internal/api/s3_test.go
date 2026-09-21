package api

// S3 设置面集成测试（E3-2）：走与生产同构的鉴权链（bufconn + seed token
//），断言 admin scope 把门、secret 只写不读（读面指纹）、envelope 密文落
// 库、审计/事件脱敏（明文零出现）、互斥校验四负路径与公网开关门禁、探针
// 正/负路径（httptest 假 S3，E_S3_TEST_FAILED 信封 context 带失败步）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newS3TestEnv 起一个带鉴权链 + SystemService（注入 box 与 base_domain）
// 的 bufconn server，返回 client 与 admin/read token。
func newS3TestEnv(t *testing.T, baseDomain string) (serverv1.SystemServiceClient, *state.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), dir+"/s3.db")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(dir + "/s3.key")
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	admin := seedTokenPlain(t, st, "admin")
	read := seedTokenPlain(t, st, "read")

	svc := NewSystemService("dev", st, nil, nil).
		WithJoinGuide(baseDomain, nil).
		WithSecretsBox(box)
	srv := newAuthServer(NewAuthenticator(st))
	serverv1.RegisterSystemServiceServer(srv, svc)
	conn := serveBufconn(t, srv)
	return serverv1.NewSystemServiceClient(conn), st, admin, read
}

// TestS3SettingsFace 设置面主链：scope 把门 → 保存（secret 加密落库）→
// 读面只出指纹 → 审计/事件落库且明文零出现 → PUT 语义切 rustfs 清空外部
// 凭证。
func TestS3SettingsFace(t *testing.T) {
	cl, st, admin, read := newS3TestEnv(t, "")
	ctx := context.Background()
	const plaintext = "s3cr3t-PLAINTEXT-MARKER"

	// scope 把门：read token → PermissionDenied。
	_, err := cl.UpdateS3Settings(authCtx(ctx, read), &serverv1.UpdateS3SettingsRequest{Mode: "unset"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token on update = %v, want PermissionDenied", status.Code(err))
	}
	_, err = cl.GetS3Settings(authCtx(ctx, read), &serverv1.GetS3SettingsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token on get = %v, want PermissionDenied", status.Code(err))
	}

	// 未配置缺省态：mode=unset、无指纹。
	got, err := cl.GetS3Settings(authCtx(ctx, admin), &serverv1.GetS3SettingsRequest{})
	if err != nil {
		t.Fatalf("GetS3Settings (empty): %v", err)
	}
	if got.GetSettings().GetMode() != state.S3ModeUnset || got.GetSettings().GetSecretFingerprint() != "" {
		t.Fatalf("empty settings = %+v, want unset/no-fingerprint", got.GetSettings())
	}

	// 保存 external 全量配置。
	up, err := cl.UpdateS3Settings(authCtx(ctx, admin), &serverv1.UpdateS3SettingsRequest{
		Mode:            state.S3ModeExternal,
		EndpointUrl:     "https://s3.example.com",
		Region:          "us-east-1",
		Bucket:          "fleetly",
		AccessKeyId:     "AKIDEXAMPLE",
		SecretAccessKey: plaintext,
		PathStyle:       true,
	})
	if err != nil {
		t.Fatalf("UpdateS3Settings: %v", err)
	}
	sum := sha256.Sum256([]byte(plaintext))
	wantFP := hex.EncodeToString(sum[:8])
	if up.GetSettings().GetSecretFingerprint() != wantFP {
		t.Fatalf("fingerprint = %s, want %s", up.GetSettings().GetSecretFingerprint(), wantFP)
	}
	// 响应不含明文。
	if strings.Contains(up.String(), plaintext) {
		t.Fatal("update response must not contain the secret plaintext")
	}

	// 读面：同指纹、不含明文。
	got, err = cl.GetS3Settings(authCtx(ctx, admin), &serverv1.GetS3SettingsRequest{})
	if err != nil {
		t.Fatalf("GetS3Settings: %v", err)
	}
	if got.GetSettings().GetSecretFingerprint() != wantFP || got.GetSettings().GetMode() != state.S3ModeExternal {
		t.Fatalf("settings view = %+v, want mode=external fingerprint=%s", got.GetSettings(), wantFP)
	}
	if strings.Contains(got.String(), plaintext) {
		t.Fatal("get response must not contain the secret plaintext")
	}

	// 持久层：密文落库（age 信封），明文不入库。
	var stored string
	if err := st.InTx(ctx, func(tx *state.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT value FROM platform_settings WHERE key = ?`, state.S3KeySecretAccessKey).Scan(&stored)
	}); err != nil {
		t.Fatalf("read stored secret: %v", err)
	}
	if stored == plaintext || !strings.HasPrefix(stored, "age-encryption.org/v1") {
		t.Fatalf("stored secret must be age ciphertext, got prefix %.40q", stored)
	}

	// 审计 + 事件：动作/事件名在案，摘要与 payload 无明文。
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	auditFound := false
	for _, a := range audits {
		if a.Action == "s3.updated" {
			auditFound = true
			if strings.Contains(a.DiffSummary, plaintext) {
				t.Fatalf("audit leaks plaintext: %s", a.DiffSummary)
			}
		}
	}
	if !auditFound {
		t.Fatal("audit action s3.updated missing")
	}
	events, err := st.EventsSince(ctx, 0, 10)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	eventFound := false
	for _, ev := range events {
		if ev.Name == "s3.updated" {
			eventFound = true
			if strings.Contains(ev.Payload, plaintext) {
				t.Fatalf("event payload leaks plaintext: %s", ev.Payload)
			}
			if !strings.Contains(ev.Payload, state.S3ModeExternal) {
				t.Fatalf("event payload should carry mode: %s", ev.Payload)
			}
		}
	}
	if !eventFound {
		t.Fatal("event s3.updated missing")
	}

	// PUT 语义：切 rustfs → 外部四项全清（含密文），指纹消失。
	if _, err := cl.UpdateS3Settings(authCtx(ctx, admin), &serverv1.UpdateS3SettingsRequest{
		Mode: state.S3ModeRustfs,
	}); err != nil {
		t.Fatalf("UpdateS3Settings (rustfs): %v", err)
	}
	got, err = cl.GetS3Settings(authCtx(ctx, admin), &serverv1.GetS3SettingsRequest{})
	if err != nil {
		t.Fatalf("GetS3Settings (rustfs): %v", err)
	}
	s := got.GetSettings()
	if s.GetMode() != state.S3ModeRustfs || s.GetEndpointUrl() != "" || s.GetBucket() != "" ||
		s.GetAccessKeyId() != "" || s.GetSecretFingerprint() != "" || s.GetPathStyle() {
		t.Fatalf("rustfs settings = %+v, want external fields cleared", s)
	}
}

// TestS3SettingsValidationRPC 互斥校验与公网开关门禁经 RPC 面 fail-fast
// （信封码 E_S3_CONFIG_CONFLICT / E_S3_PUBLIC_REQUIRES_BASE_DOMAIN）。
func TestS3SettingsValidationRPC(t *testing.T) {
	cl, _, admin, _ := newS3TestEnv(t, "") // base_domain 空 = 单节点形态
	ctx := authCtx(context.Background(), admin)

	cases := []struct {
		name     string
		req      *serverv1.UpdateS3SettingsRequest
		wantCode string
	}{
		{
			name:     "external missing secret",
			req:      &serverv1.UpdateS3SettingsRequest{Mode: state.S3ModeExternal, EndpointUrl: "https://s3.example.com", Bucket: "b", AccessKeyId: "a"},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "rustfs with external fields",
			req:      &serverv1.UpdateS3SettingsRequest{Mode: state.S3ModeRustfs, EndpointUrl: "https://s3.example.com"},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "public_exposed on external",
			req:      &serverv1.UpdateS3SettingsRequest{Mode: state.S3ModeExternal, EndpointUrl: "https://s3.example.com", Bucket: "b", AccessKeyId: "a", SecretAccessKey: "s", PublicExposed: true},
			wantCode: "E_S3_CONFIG_CONFLICT",
		},
		{
			name:     "public_exposed rustfs without base_domain",
			req:      &serverv1.UpdateS3SettingsRequest{Mode: state.S3ModeRustfs, PublicExposed: true},
			wantCode: "E_S3_PUBLIC_REQUIRES_BASE_DOMAIN",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cl.UpdateS3Settings(ctx, tc.req)
			if err == nil {
				t.Fatal("update should fail")
			}
			ae, ok := apperr.FromGRPCStatus(status.Convert(err))
			if !ok || ae.Code() != tc.wantCode {
				t.Fatalf("envelope = %v (ok=%v), want %s", err, ok, tc.wantCode)
			}
		})
	}

	// 对照：有 base_domain 时 rustfs+public_exposed 放行。
	cl2, _, admin2, _ := newS3TestEnv(t, "example.test")
	if _, err := cl2.UpdateS3Settings(authCtx(context.Background(), admin2),
		&serverv1.UpdateS3SettingsRequest{Mode: state.S3ModeRustfs, PublicExposed: true}); err != nil {
		t.Fatalf("rustfs+public with base_domain should pass: %v", err)
	}
}

// probeFakeS3 是探针测试用的最小假 S3（只实现探针路径：建桶前置 +
// 对象 PUT/GET/DELETE + region 解析；sigv4 不验签，按 AccessKey 比对出
// 403）。与 internal/objectstore 的测试假服务器同构但最小化。
type probeFakeS3 struct {
	srv       *httptest.Server
	bucket    string
	objects   map[string][]byte
	accessKey string
}

func newProbeFakeS3(t *testing.T, bucket, accessKey string) *probeFakeS3 {
	t.Helper()
	f := &probeFakeS3{bucket: bucket, objects: map[string][]byte{}, accessKey: accessKey}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *probeFakeS3) s3Error(w http.ResponseWriter, code int, c, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, c, msg)
}

func (f *probeFakeS3) handle(w http.ResponseWriter, r *http.Request) {
	if f.accessKey != "" && !strings.Contains(r.Header.Get("Authorization"), f.accessKey) {
		f.s3Error(w, http.StatusForbidden, "AccessDenied", "Access Denied (probe fake)")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	prefix := "/" + f.bucket
	switch {
	case r.Method == http.MethodGet && path == prefix && r.URL.Query().Has("location"):
		_, _ = io.WriteString(w, `<?xml version="1.0"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
	case r.Method == http.MethodPut && path == prefix:
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(path, prefix+"/") && len(path) > len(prefix)+1:
		f.handleObject(w, r, objectKey(prefix, path))
	default:
		f.s3Error(w, http.StatusNotFound, "NoSuchKey", "unmatched route")
	}
}

// objectKey 取对象键（path 已知形态 /<bucket>/<key>）。
func objectKey(prefix, path string) string {
	return strings.TrimPrefix(path, prefix+"/")
}

// handleObject 处理对象级 PUT/GET/DELETE（探针面所需的最小集）。
func (f *probeFakeS3) handleObject(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			f.s3Error(w, http.StatusBadRequest, "IncompleteBody", err.Error())
			return
		}
		f.objects[key] = decodeChunkedBody(r, body)
		w.Header().Set("ETag", `"fake"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		body, ok := f.objects[key]
		if !ok {
			f.s3Error(w, http.StatusNotFound, "NoSuchKey", "object not found")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		f.s3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "unsupported")
	}
}

// decodeChunkedBody 在 aws-chunked 形态（Content-Encoding 头声明）下剥掉
// 分块框架，还原对象字节（minio-go 对非 TLS 端点使用流式签名分块）。
func decodeChunkedBody(r *http.Request, raw []byte) []byte {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return raw
	}
	var out []byte
	for _, line := range strings.Split(string(raw), "\r\n") {
		size := line
		if i := strings.IndexByte(line, ';'); i >= 0 {
			size = line[:i]
		}
		if isAllHex(size) {
			continue
		}
		out = append(out, line...)
	}
	return out
}

// isAllHex 判定整串是否全为 hex 数字（分块尺寸行特征）。
func isAllHex(s string) bool {
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

// TestS3ConnectionProbe 探针面：候选配置正路径（steps 全 ok）、错凭据负
// 路径（E_S3_TEST_FAILED + context 失败步）、已存配置路径、无配置拒绝。
func TestS3ConnectionProbe(t *testing.T) {
	cl, _, admin, read := newS3TestEnv(t, "")
	ctx := context.Background()

	// scope 把门：read token → PermissionDenied。
	fake := newProbeFakeS3(t, "fleetly", "good-access-key")
	_, err := cl.TestS3Connection(authCtx(ctx, read), &serverv1.TestS3ConnectionRequest{
		EndpointUrl: fake.srv.URL, Bucket: "fleetly", AccessKeyId: "good-access-key",
		SecretAccessKey: "secret", PathStyle: true,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token on test = %v, want PermissionDenied", status.Code(err))
	}

	// 正路径：候选配置（未保存也能测）→ ok + 三步全过。
	res, err := cl.TestS3Connection(authCtx(ctx, admin), &serverv1.TestS3ConnectionRequest{
		EndpointUrl: fake.srv.URL, Bucket: "fleetly", AccessKeyId: "good-access-key",
		SecretAccessKey: "good-secret", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("TestS3Connection (candidate): %v", err)
	}
	r := res.GetResult()
	if !r.GetOk() || r.GetFailedStep() != "" || len(r.GetSteps()) != 3 {
		t.Fatalf("probe result = %+v, want ok with 3 steps", r)
	}
	for _, st := range r.GetSteps() {
		if !st.GetOk() {
			t.Fatalf("step %s failed: %s", st.GetStep(), st.GetError())
		}
	}
	if strings.Contains(r.String(), "good-secret") {
		t.Fatal("probe result must not echo the secret")
	}

	// 负路径：错凭据 → E_S3_TEST_FAILED，context 带失败步。
	_, err = cl.TestS3Connection(authCtx(ctx, admin), &serverv1.TestS3ConnectionRequest{
		EndpointUrl: fake.srv.URL, Bucket: "fleetly", AccessKeyId: "wrong-key",
		SecretAccessKey: "x", PathStyle: true,
	})
	if err == nil {
		t.Fatal("probe with wrong credentials should fail")
	}
	ae, ok := apperr.FromGRPCStatus(status.Convert(err))
	if !ok || ae.Code() != "E_S3_TEST_FAILED" {
		t.Fatalf("envelope = %v (ok=%v), want E_S3_TEST_FAILED", err, ok)
	}
	if ae.Context()["failed_step"] != "put" {
		t.Fatalf("context failed_step = %s, want put", ae.Context()["failed_step"])
	}
	if ae.Context()["endpoint"] != fake.srv.URL {
		t.Fatalf("context endpoint = %s, want %s", ae.Context()["endpoint"], fake.srv.URL)
	}

	// 无候选配置且无已存配置 → 拒绝（E_S3_NOT_CONFIGURED 是 E3-4 的消费
	// 码，此处为输入形状拒绝）。
	_, err = cl.TestS3Connection(authCtx(ctx, admin), &serverv1.TestS3ConnectionRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("probe with nothing stored = %v, want InvalidArgument", status.Code(err))
	}

	// 已存配置路径：保存 external 指向假端点 → 空候选参数即测已存配置。
	if _, err := cl.UpdateS3Settings(authCtx(ctx, admin), &serverv1.UpdateS3SettingsRequest{
		Mode: state.S3ModeExternal, EndpointUrl: fake.srv.URL, Bucket: "fleetly",
		AccessKeyId: "good-access-key", SecretAccessKey: "good-secret", PathStyle: true,
	}); err != nil {
		t.Fatalf("UpdateS3Settings: %v", err)
	}
	res, err = cl.TestS3Connection(authCtx(ctx, admin), &serverv1.TestS3ConnectionRequest{})
	if err != nil {
		t.Fatalf("TestS3Connection (stored): %v", err)
	}
	if !res.GetResult().GetOk() {
		t.Fatalf("stored-config probe = %+v, want ok", res.GetResult())
	}
}
