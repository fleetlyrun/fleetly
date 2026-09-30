package apperr

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/fleetlyrun/fleetly/internal/model/errcode"
)

func TestNewUnregisteredPanics(t *testing.T) {
	assert.Panics(t, func() { _ = New("E_NOT_REGISTERED", "boom") })
}

func TestEnvelopeRoundTrip(t *testing.T) {
	e := New("E_INTERNAL", "disk on fire at %s", "/data")
	_ = e.WithContext("path", "/data").WithSuggestion("free up disk space")

	st := e.ToGRPCStatus()
	assert.Equal(t, codes.Internal, st.Code())
	assert.Equal(t, "E_INTERNAL: disk on fire at /data", st.Message())

	env := e.Envelope("err-123")
	assert.Equal(t, "E_INTERNAL", env.GetCode())
	assert.Equal(t, "disk on fire at /data", env.GetMessage())
	assert.Equal(t, "free up disk space", env.GetSuggestion())
	assert.Equal(t, errcode.DocsURL("E_INTERNAL"), env.GetDocs())
	assert.Equal(t, "err-123", env.GetContext()["error_id"])
	assert.Equal(t, "/data", env.GetContext()["path"])

	// detail 还原：注册表默认 suggestion 在覆盖后保持覆盖值。
	back, ok := FromGRPCStatus(st)
	require.True(t, ok)
	assert.Equal(t, "E_INTERNAL", back.Code())
	assert.Equal(t, "free up disk space", back.Suggestion())
	assert.True(t, errors.Is(back, New("E_INTERNAL", "other message")))
}

func TestGRPCStatusHook(t *testing.T) {
	// handler 裸返回路径：grpc.FromError 对实现 GRPCStatus 的 error 采信。
	st, ok := status.FromError(New("E_INTERNAL", "x"))
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	restored, found := FromError(st.Err())
	require.True(t, found)
	assert.Equal(t, "E_INTERNAL", restored.Code())
}

func TestEnvelopeFromGRPCStatusDegenerate(t *testing.T) {
	// 无 detail 的机械 NotFound：code 诚实留空；Internal 回落在册 E_INTERNAL。
	env := EnvelopeFromGRPCStatus(status.New(codes.NotFound, "nope"), "id-1")
	assert.Empty(t, env.GetCode())
	assert.Equal(t, "id-1", env.GetContext()["error_id"])

	env = EnvelopeFromGRPCStatus(status.New(codes.Internal, "secret"), "id-2")
	assert.Equal(t, "E_INTERNAL", env.GetCode())
	assert.Equal(t, errcode.DocsURL("E_INTERNAL"), env.GetDocs())
}
