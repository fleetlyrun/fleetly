package fleetly

// errors_test.go 是 sdk module 的首个测试文件（P4-4 批 4）：信封投影
// FromError 的全字段投影/Error() 形态/error_id 提升/三类 false 负例。
// 测试自足构造 wire 形态（status.WithDetails + ErrorResponse），不经
// internal 面——与公开模块零 internal 依赖的纪律同构。std testing 直写
//（公开 SDK 不为测试引入 testify 依赖）。

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
)

// envelopeError 手工构造携带信封 detail 的 status error（服务端出口
// ToGRPCStatus/SanitizeExit 的上线 wire 形态同款：status message 为
// "CODE: message"，信封在 detail）。
func envelopeError(t *testing.T, env *sharedv1.ErrorResponse) error {
	t.Helper()
	st, err := status.New(codes.NotFound, env.GetCode()+": "+env.GetMessage()).WithDetails(env)
	if err != nil {
		t.Fatalf("with details: %v", err)
	}
	return st.Err()
}

// TestFromErrorProjectsEnvelope：全字段投影 + Error() 形态 + error_id 提升
// （context 其余对保留，error_id 键不重复落入 Context）。
func TestFromErrorProjectsEnvelope(t *testing.T) {
	err := envelopeError(t, &sharedv1.ErrorResponse{
		Code:       "E_NOT_FOUND",
		Message:    "app web not found",
		Suggestion: "check the app id",
		Docs:       "https://fleetly.dev/errors/E_NOT_FOUND",
		Context:    map[string]string{"error_id": "abc123", "project_id": "p1"},
	})

	got, ok := FromError(err)
	if !ok {
		t.Fatal("FromError must project an envelope detail")
	}
	if got.Code != "E_NOT_FOUND" || got.Message != "app web not found" ||
		got.Suggestion != "check the app id" || got.Docs != "https://fleetly.dev/errors/E_NOT_FOUND" {
		t.Fatalf("envelope fields drifted: %+v", got)
	}
	if got.ErrorID != "abc123" {
		t.Fatalf("error_id must be promoted from context, got %q", got.ErrorID)
	}
	// error_id 键提升后不重复；其余上下文对原样保留。
	if len(got.Context) != 1 || got.Context["project_id"] != "p1" {
		t.Fatalf("remaining context pairs must survive, got %v", got.Context)
	}
	// Error()：与服务端 status message 同款 "CODE: message" 单行。
	if want := "E_NOT_FOUND: app web not found"; got.Error() != want {
		t.Fatalf("Error() = %q, want %q", got.Error(), want)
	}
}

// TestErrorStringDegenerateShape：code 缺席（信封退化投影）时 Error()
// 诚实回落 message 原文，不拼空码前缀。
func TestErrorStringDegenerateShape(t *testing.T) {
	e := &Error{Message: "upstream unavailable"}
	if got := e.Error(); got != "upstream unavailable" {
		t.Fatalf("Error() = %q, want the bare message", got)
	}
}

// TestFromErrorFalseFaces：无信封 detail 的 status / nil / 非 status 三类
// 输入一律 (nil, false)。
func TestFromErrorFalseFaces(t *testing.T) {
	if e, ok := FromError(status.Error(codes.NotFound, "bare status, no detail")); e != nil || ok {
		t.Fatalf("detail-less status must be (nil, false), got (%v, %v)", e, ok)
	}
	if e, ok := FromError(nil); e != nil || ok {
		t.Fatalf("nil must be (nil, false), got (%v, %v)", e, ok)
	}
	if e, ok := FromError(errors.New("plain local error")); e != nil || ok {
		t.Fatalf("non-status error must be (nil, false), got (%v, %v)", e, ok)
	}
}

// TestFromErrorPassthrough：*Error 直传原样返回（errors.As 风格）。
func TestFromErrorPassthrough(t *testing.T) {
	sentinel := &Error{Code: "E_CONFLICT", Message: "state clash"}
	got, ok := FromError(sentinel)
	if !ok || got != sentinel {
		t.Fatalf("*Error must pass through unchanged, got (%v, %v)", got, ok)
	}
}
