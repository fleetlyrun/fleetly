package apperr

import (
	"errors"
	"net/http"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/errcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestNewPanicsOnUnregisteredCode：信封 code 只允许注册表内码（构造期
// fail-fast，保证不出现文档外码）。
func TestNewPanicsOnUnregisteredCode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New with unregistered code must panic")
		}
	}()
	_ = New("E_NOT_IN_REGISTRY", "boom")
}

// TestNewPanicsOnWarningCode：W_ 警告码不得构造为错误（警告不作为
// HTTP/gRPC 错误返回）。
func TestNewPanicsOnWarningCode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New with W_ warning code must panic")
		}
	}()
	_ = New("W_DEPLOY_INSTABILITY", "boom")
}

// TestDefaultsFromRegistry：suggestion/docs 取注册表默认；code/message 保真。
func TestDefaultsFromRegistry(t *testing.T) {
	c, _ := errcode.Get("E_VOLUME_NODE_MISMATCH")
	e := New("E_VOLUME_NODE_MISMATCH", "卷数据节点 ≠ 目标节点 (volume=%s)", "v_abc")
	if e.Code() != "E_VOLUME_NODE_MISMATCH" {
		t.Fatalf("code = %q", e.Code())
	}
	if e.Message() != "卷数据节点 ≠ 目标节点 (volume=v_abc)" {
		t.Fatalf("message = %q", e.Message())
	}
	if e.suggestion != c.Suggestion || e.docs != c.Docs() {
		t.Fatalf("defaults not applied: suggestion=%q docs=%q", e.suggestion, e.docs)
	}
	if e.HTTPStatus() != 409 { // 文档显式：前哨 409
		t.Fatalf("HTTPStatus = %d, want 409", e.HTTPStatus())
	}
	if e.Error() != "E_VOLUME_NODE_MISMATCH: 卷数据节点 ≠ 目标节点 (volume=v_abc)" {
		t.Fatalf("Error() = %q", e.Error())
	}
}

// TestFluentFields：phase/deployment_id/context/cause 链式附加。
func TestFluentFields(t *testing.T) {
	cause := errors.New("dial tcp 127.0.0.1:2377: connect refused")
	e := New("E_RUNTIME_UNAVAILABLE", "底座不可达").
		WithPhase("deploy").
		WithDeploymentID("d_01J").
		WithContext("node", "n_01J").
		WithCause(cause)
	env := e.Envelope()
	if env.GetPhase() != "deploy" || env.GetDeploymentId() != "d_01J" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.GetContext()["node"] != "n_01J" {
		t.Fatalf("context = %v", env.GetContext())
	}
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is must traverse WithCause chain")
	}
}

// TestToFromGRPCStatusRoundTrip：ToGRPCStatus → status detail → FromGRPCStatus
// 还原七字段。
func TestToFromGRPCStatusRoundTrip(t *testing.T) {
	e := New("E_HEALTH_TIMEOUT", "健康门超时").
		WithPhase("releasing").
		WithDeploymentID("d_42").
		WithContext("service", "web").
		WithContext("budget", "300s")
	st := e.ToGRPCStatus()
	if st.Code() != codes.Internal { // 文档未给定 HTTP → 缺省 500 → Internal
		t.Fatalf("grpc code = %s, want Internal", st.Code())
	}
	got, ok := FromGRPCStatus(st)
	if !ok {
		t.Fatal("FromGRPCStatus lost the envelope detail")
	}
	if got.Code() != "E_HEALTH_TIMEOUT" || got.Message() != "健康门超时" ||
		got.phase != "releasing" || got.deploymentID != "d_42" ||
		got.context["service"] != "web" || got.context["budget"] != "300s" ||
		got.suggestion == "" || got.docs == "" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if !errors.Is(e, got) || !errors.Is(got, e) {
		t.Fatal("errors.Is must match by stable code both ways")
	}
}

// TestFromGRPCStatusWithoutDetail：无 detail → (nil,false)，由调用方走退化信封。
func TestFromGRPCStatusWithoutDetail(t *testing.T) {
	st := status.New(codes.NotFound, "no route")
	if _, ok := FromGRPCStatus(st); ok {
		t.Fatal("plain status must not yield an apperr")
	}
	if _, ok := FromError(errors.New("plain")); ok {
		t.Fatal("non-status error must not yield an apperr")
	}
}

// TestEnvelopeFromGRPCStatusDegraded：兜底取舍——code 留空串、message 保底、
// HTTP 由 grpc code 机械映射。
func TestEnvelopeFromGRPCStatusDegraded(t *testing.T) {
	env, httpStatus := EnvelopeFromGRPCStatus(status.New(codes.NotFound, "missing"))
	if env.GetCode() != "" {
		t.Fatalf("degraded code = %q, want empty string（不得发明清单外码）", env.GetCode())
	}
	if env.GetMessage() != "missing" {
		t.Fatalf("degraded message = %q", env.GetMessage())
	}
	if httpStatus != http.StatusNotFound {
		t.Fatalf("degraded HTTP = %d, want 404", httpStatus)
	}
}

// TestEnvelopeFromGRPCStatusRegistryHTTP：有信封 detail 时 HTTP 按注册表。
func TestEnvelopeFromGRPCStatusRegistryHTTP(t *testing.T) {
	st := New("E_EVENT_CURSOR_EXPIRED", "游标早于保留期").ToGRPCStatus()
	env, httpStatus := EnvelopeFromGRPCStatus(st)
	if env.GetCode() != "E_EVENT_CURSOR_EXPIRED" || httpStatus != 410 {
		t.Fatalf("code=%q http=%d, want E_EVENT_CURSOR_EXPIRED/410（文档显式）", env.GetCode(), httpStatus)
	}
}

// TestCodeMappingsSanity：HTTP↔gRPC 机械映射抽检（逆向为多对一：
// 409/410 都映射 FailedPrecondition，再逆向回 409 属预期，细分由信封
// code 表达）。
func TestCodeMappingsSanity(t *testing.T) {
	pairs := []struct {
		httpStatus int
		code       codes.Code
	}{
		{400, codes.InvalidArgument},
		{422, codes.InvalidArgument},
		{409, codes.FailedPrecondition},
		{410, codes.FailedPrecondition},
		{503, codes.Unavailable},
		{500, codes.Internal},
	}
	for _, p := range pairs {
		if got := HTTPToGRPCCode(p.httpStatus); got != p.code {
			t.Errorf("HTTPToGRPCCode(%d) = %s, want %s", p.httpStatus, got, p.code)
		}
	}
	if GRPCCodeToHTTP(codes.Unavailable) != 503 || GRPCCodeToHTTP(codes.InvalidArgument) != 400 {
		t.Fatal("reverse mapping sanity failed")
	}
}
