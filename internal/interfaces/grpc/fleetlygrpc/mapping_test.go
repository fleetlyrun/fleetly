package fleetlygrpc

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// mapStateError 的哨兵映射面（staging 真机实证补漏，2026-10-02：未批准的
// 跨 Project 网络引用此前落 E_INTERNAL——受理预检的 strict 拒绝是可编程
// 分支，不是内部错误）。
func TestMapStateErrorCrossProjectRefNotApproved(t *testing.T) {
	err := mapStateError(fmt.Errorf("wrap: %w", engine.ErrCrossProjectRefNotApproved), "deploy")
	if !strings.Contains(err.Error(), "E_INVALID_ARGUMENT") || !strings.Contains(err.Error(), "declare and approve the network peer") {
		t.Errorf("unapproved peer ref must map to an actionable E_INVALID_ARGUMENT, got: %v", err)
	}
	// 哨兵链上再包一层同样命中（errors.Is 语义）。
	err = mapStateError(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", engine.ErrCrossProjectRefNotApproved)), "deploy")
	if !strings.Contains(err.Error(), "E_INVALID_ARGUMENT") {
		t.Errorf("wrapped sentinel must still map via errors.Is, got: %v", err)
	}
}

// TestMapMetricsErrors 钉指标查询错误族的分流（H8 阶段 B，设计文档
// §5.3.3/§5.6）：上游 4xx → E_INVALID_ARGUMENT（raw 查询错）；结果超体/
// 超序列 → E_METRICS_RESULT_TOO_LARGE；provider 超时 → E_METRICS_QUERY_TIMEOUT。
func TestMapMetricsErrors(t *testing.T) {
	codeOf := func(err error) string {
		t.Helper()
		var ae *apperr.Error
		if !errors.As(err, &ae) {
			t.Fatalf("mapped error is not *apperr.Error: %v", err)
		}
		return ae.Code()
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "upstream 4xx is caller error",
			err:  &capability.MetricsQueryError{Status: http.StatusUnprocessableEntity, Message: "invalid PromQL"},
			want: "E_INVALID_ARGUMENT",
		},
		{
			name: "result too large",
			err:  fmt.Errorf("victoriametrics query: %w: response exceeds 8 MiB", capability.ErrMetricsResultTooLarge),
			want: "E_METRICS_RESULT_TOO_LARGE",
		},
		{
			name: "provider timeout",
			err:  fmt.Errorf("victoriametrics query: %w: did not answer", capability.ErrMetricsQueryTimeout),
			want: "E_METRICS_QUERY_TIMEOUT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codeOf(mapStateError(tt.err, "metrics")); got != tt.want {
				t.Errorf("mapped code = %s, want %s", got, tt.want)
			}
		})
	}
}
