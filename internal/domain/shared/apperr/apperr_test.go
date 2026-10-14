package apperr

// 纯数据面测试（P1a-3a）：构造链、按码判等、Restore 往返与访问器。传输
// 投影（status/信封）与出口纪律的测试在
// internal/interfaces/grpc/apperrstatus。

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUnregisteredPanics(t *testing.T) {
	assert.Panics(t, func() { _ = New("E_NOT_REGISTERED", "boom") })
}

func TestErrorShape(t *testing.T) {
	e := New("E_INTERNAL", "disk on fire at %s", "/data")
	assert.Equal(t, "E_INTERNAL: disk on fire at /data", e.Error())
	assert.Equal(t, "E_INTERNAL", e.Code())
	assert.Equal(t, "disk on fire at /data", e.Message())

	// 覆盖 suggestion 优先；未覆盖时回落注册表默认。
	assert.NotEmpty(t, e.Suggestion())
	assert.Equal(t, "free up disk space", e.WithSuggestion("free up disk space").Suggestion())

	// cause 链保持连通。
	cause := errors.New(" underneath")
	assert.ErrorIs(t, e.WithCause(cause), cause)
}

func TestIsByCode(t *testing.T) {
	assert.True(t, errors.Is(New("E_INTERNAL", "a"), New("E_INTERNAL", "b")))
	assert.False(t, errors.Is(New("E_INTERNAL", "a"), New("E_NOT_FOUND", "b")))
}

// ContextPairs 保序 + RetryAfter 开关。
func TestContextPairsAndRetryAfter(t *testing.T) {
	e := New("E_RATE_LIMITED", "slow down")
	_ = e.WithContext("window", "60s").WithContext("scope", "token")
	assert.Equal(t, []Pair{{Key: "window", Value: "60s"}, {Key: "scope", Value: "token"}}, e.ContextPairs())

	_, ok := e.RetryAfter()
	assert.False(t, ok)
	d := 30 * time.Second
	_ = e.WithRetryAfter(d)
	got, ok := e.RetryAfter()
	require.True(t, ok)
	assert.Equal(t, d, got)
}

// Restore 在册码往返：字段齐还原；未注册码返回 false（文档外码不采信）。
func TestRestore(t *testing.T) {
	e, ok := Restore("E_NOT_FOUND", "app web not found", "check the id",
		[]Pair{{Key: "app", Value: "web"}}, "err-123")
	require.True(t, ok)
	assert.Equal(t, "E_NOT_FOUND", e.Code())
	assert.Equal(t, "app web not found", e.Message())
	assert.Equal(t, "check the id", e.Suggestion())
	assert.Equal(t, []Pair{{Key: "app", Value: "web"}}, e.ContextPairs())
	assert.Equal(t, "err-123", e.ErrorID())

	// 空 suggestion 保留注册表默认回落语义；空 errorID 不发明锚。
	e2, ok := Restore("E_NOT_FOUND", "x", "", nil, "")
	require.True(t, ok)
	assert.Equal(t, e2.Suggestion(), New("E_NOT_FOUND", "x").Suggestion())
	assert.Empty(t, e2.ErrorID())

	_, ok = Restore("E_NOT_REGISTERED", "x", "", nil, "")
	assert.False(t, ok)
}
