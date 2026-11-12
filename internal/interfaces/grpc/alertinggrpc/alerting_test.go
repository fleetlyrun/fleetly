package alertinggrpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// TestCipherNilRefusedPrecise 钉通道配置面 cipher 缺席的精确拒绝：
// kind 形校先过（webhook 带 url），随后 E_MASTER_KEY_UNAVAILABLE（503 族）。
func TestCipherNilRefusedPrecise(t *testing.T) {
	_, err := New(Deps{}).CreateNotificationChannel(context.Background(),
		&telemetryv1.CreateNotificationChannelRequest{Name: "ops", Kind: "webhook", Url: "http://127.0.0.1:9/x"})
	require.Error(t, err)
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "E_MASTER_KEY_UNAVAILABLE", ae.Code())
	assert.Equal(t, "Unavailable", apperrstatus.ToGRPCStatus(ae).Code().String())
}
