package nodesgrpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// TestRuntimeFaceAbsentRefusedPrecise 钉节点可选子面缺席的精确拒绝：
// Runtime 零值（FacesOf 全空）→ E_RUNTIME_FACE_UNSUPPORTED / Unimplemented。
func TestRuntimeFaceAbsentRefusedPrecise(t *testing.T) {
	ctx := authn.WithIdentity(context.Background(), &authn.Identity{
		TeamID: identity.DefaultTeamID, RoleID: identity.RoleOwnerID,
	})
	svc := New(Deps{})

	_, err := svc.DrainNode(ctx, &runtimev1.DrainNodeRequest{NodeId: "01JD0NODE00000000000000000"})
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "E_RUNTIME_FACE_UNSUPPORTED", ae.Code())
	assert.Equal(t, "Unimplemented", apperrstatus.ToGRPCStatus(ae).Code().String())
	assert.Contains(t, ae.Message(), "does not expose node administration")

	_, err = svc.ListNodeContainers(ctx, &runtimev1.ListNodeContainersRequest{NodeId: "01JD0NODE00000000000000000"})
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "E_RUNTIME_FACE_UNSUPPORTED", ae.Code())
	assert.Equal(t, "Unimplemented", apperrstatus.ToGRPCStatus(ae).Code().String())
	assert.Contains(t, ae.Message(), "does not expose node inspection")
}
