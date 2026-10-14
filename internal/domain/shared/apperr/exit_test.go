package apperr

// 出口纪律单点测试（P0-3）：E_INTERNAL 脱敏 + error_id 在场 + cause 只进
// 日志；非 E_INTERNAL 零影响；转发链幂等；机械内部错误同款收口。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errorIDRe 钉 error_id 形态：32 字符十六进制（128 位随机）。
var errorIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// captureLogger 返回缓冲 logger 与其内容读取器。
func captureLogger() (*slog.Logger, func() string) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), buf.String
}

func TestSanitizeExitInternalRedactsAndMints(t *testing.T) {
	logger, logText := captureLogger()
	cause := errors.New("sql: no rows in result set (rolling back tx 42)")
	err := New("E_INTERNAL", "internal error: %s", "state write failed").WithCause(cause)

	errorID, sanitized := SanitizeExit(context.Background(), logger, err, "grpc /fleetly.test/Echo")
	require.NotEmpty(t, errorID)
	assert.Regexp(t, errorIDRe, errorID)

	st := status.Convert(sanitized)
	assert.Equal(t, codes.Internal, st.Code())
	// message 脱敏：cause 复合文本不上线，形态与其余码一致（CODE: message）。
	assert.Equal(t, "E_INTERNAL: internal server error", st.Message())

	// 信封 detail：error_id 在场、message 脱敏、注册表 suggestion/docs 保留。
	restored, ok := FromGRPCStatus(st)
	require.True(t, ok)
	assert.Equal(t, "E_INTERNAL", restored.Code())
	assert.Equal(t, RedactedInternalMessage, restored.Message())
	assert.Equal(t, errorID, restored.ErrorID())

	// 服务端日志：cause 全文 + error_id 关联，一次落盘。
	logs := logText()
	assert.Contains(t, logs, errorID)
	assert.Contains(t, logs, "state write failed")
	assert.Contains(t, logs, "sql: no rows in result set")
	assert.Contains(t, logs, "grpc /fleetly.test/Echo")
}

func TestSanitizeExitNonInternalPassesThrough(t *testing.T) {
	logger, logText := captureLogger()
	err := New("E_NOT_FOUND", "app %s not found", "web").WithCause(errors.New("row miss"))

	errorID, sanitized := SanitizeExit(context.Background(), logger, err, "grpc /x/Y")
	assert.Empty(t, errorID)
	assert.Same(t, err, sanitized, "non-E_INTERNAL errors must pass through untouched")
	assert.NotContains(t, logText(), "row miss")
}

func TestSanitizeExitIdempotentOnForwardedStatus(t *testing.T) {
	// REST 面经 gRPC 段转发的常态：错误已在 gRPC 出口收口（信封带
	// error_id），二次收口必须复用同一 id、不重复落日志。
	logger, logText := captureLogger()
	id, first := SanitizeExit(context.Background(), nil, New("E_INTERNAL", "boom"), "grpc /x/Y")

	id2, second := SanitizeExit(context.Background(), logger, first, "http GET /v1/x")
	assert.Equal(t, id, id2)
	assert.Equal(t, status.Convert(first), status.Convert(second))
	assert.NotContains(t, logText(), "internal error sanitized")
}

func TestSanitizeExitMechanicalInternalWrapped(t *testing.T) {
	logger, logText := captureLogger()

	// 裸错误（grpc-go 上线兜底形态 = Unknown 全文）：收口为在册 E_INTERNAL。
	errorID, sanitized := SanitizeExit(context.Background(), logger, errors.New("secret dsn postgres://..."), "grpc /x/Y")
	require.NotEmpty(t, errorID)
	st := status.Convert(sanitized)
	assert.Equal(t, codes.Internal, st.Code())
	assert.NotContains(t, st.Message(), "postgres")
	restored, ok := FromGRPCStatus(st)
	require.True(t, ok)
	assert.Equal(t, "E_INTERNAL", restored.Code())
	assert.Equal(t, errorID, restored.ErrorID())
	assert.Contains(t, logText(), "postgres")

	// 显式 Internal status（无信封）：同款。
	errorID2, sanitized := SanitizeExit(context.Background(), logger, status.Error(codes.Internal, "raw panic text"), "http POST /v1/y")
	require.NotEmpty(t, errorID2)
	assert.Equal(t, "E_INTERNAL: internal server error", status.Convert(sanitized).Message())
}

func TestSanitizeExitMechanicalNonInternalUntouched(t *testing.T) {
	logger, _ := captureLogger()
	// ctx 取消/超时是诚实传输语义，不吞成 E_INTERNAL。
	for _, mech := range []error{context.Canceled, context.DeadlineExceeded} {
		errorID, sanitized := SanitizeExit(context.Background(), logger, mech, "grpc /x/Y")
		assert.Empty(t, errorID)
		assert.Equal(t, mech, sanitized)
	}
	// 其余机械 status（无信封的 NotFound）：原样放行。
	nf := status.Error(codes.NotFound, "nope")
	errorID, sanitized := SanitizeExit(context.Background(), logger, nf, "grpc /x/Y")
	assert.Empty(t, errorID)
	assert.Equal(t, nf, sanitized)
}

func TestSanitizeExitNil(t *testing.T) {
	errorID, sanitized := SanitizeExit(context.Background(), nil, nil, "grpc /x/Y")
	assert.Nil(t, sanitized)
	assert.Empty(t, errorID)
}

func TestNewErrorIDShape(t *testing.T) {
	assert.Regexp(t, errorIDRe, NewErrorID())
	assert.NotEqual(t, NewErrorID(), NewErrorID(), "ids must be per-exit unique")
}
