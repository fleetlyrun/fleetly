package assembly

import (
	"net/http"
	"testing"

	"github.com/lynx-go/grpcapi/gateway"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/model/errcode"
)

// TestErrcodeToHTTPCoversRegistry（批 0 复核回归守卫）：REST 面错误码级
// HTTP 状态映射必须全量覆盖在册注册表——新码入册时强制显式决策 HTTP
// 语义，不得靠 gRPC code 机械映射隐式兜底（E_CONFLICT 族曾被机械映射
// 静默打成 400，破坏 REST/SDK 按 409 写的重试逻辑）。
func TestErrcodeToHTTPCoversRegistry(t *testing.T) {
	for _, c := range errcode.All() {
		s, ok := errcodeToHTTP[c.ID]
		if !ok {
			t.Errorf("errcode %s registered but missing from errcodeToHTTP (declare its REST status explicitly)", c.ID)
			continue
		}
		// 信封在册码的 HTTP 状态必须落在 4xx/5xx（错误码不当映射 2xx/3xx）。
		if s < 400 || s >= 600 {
			t.Errorf("errcode %s maps to HTTP %d (want 4xx/5xx)", c.ID, s)
		}
	}
	// 反向保鲜：映射表不得收录注册表外的码（拼错/退役码即红）。
	for id := range errcodeToHTTP {
		if _, ok := errcode.Get(id); !ok {
			t.Errorf("errcodeToHTTP contains %q which is not registered (registry is the single source)", id)
		}
	}
}

// TestResolveHTTPStatusOverridesMechanicalMapping：还原出在册信封时以
// errcodeToHTTP 为准（E_CONFLICT 的 gRPC 形态 FailedPrecondition 机械映射
// 是 400，语义真源在错误码 → 409）；无信封/注册表外码回落 gRPC 机械映射。
func TestResolveHTTPStatusOverridesMechanicalMapping(t *testing.T) {
	envelopeOf := func(code string) *sharedv1.ErrorResponse {
		return &sharedv1.ErrorResponse{Code: code}
	}
	// 状态冲突族：FailedPrecondition 载体、REST 面 409（本回归的钉子）。
	fp := status.New(codes.FailedPrecondition, "conflict")
	if got := resolveHTTPStatus(fp, envelopeOf("E_CONFLICT")); got != http.StatusConflict {
		t.Errorf("E_CONFLICT (FailedPrecondition) must map to 409 on REST, got %d", got)
	}
	// 唯一键冲突：AlreadyExists 载体、REST 409（双保险：机械映射同为 409）。
	ae := status.New(codes.AlreadyExists, "exists")
	if got := resolveHTTPStatus(ae, envelopeOf("E_ALREADY_EXISTS")); got != http.StatusConflict {
		t.Errorf("E_ALREADY_EXISTS must map to 409 on REST, got %d", got)
	}
	// 无信封机械错误回落默认表（Canceled→499 是 lynx 约定）。
	if got := resolveHTTPStatus(status.New(codes.Canceled, "canceled"), nil); got != gateway.DefaultCodeToHTTP(codes.Canceled) {
		t.Errorf("bare status must fall back to DefaultCodeToHTTP, got %d", got)
	}
	// 退化信封 code 为空：回落机械映射（Internal→500）。
	if got := resolveHTTPStatus(status.New(codes.Internal, "boom"), &sharedv1.ErrorResponse{}); got != http.StatusInternalServerError {
		t.Errorf("empty-code envelope must fall back to mechanical mapping, got %d", got)
	}
}
