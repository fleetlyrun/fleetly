package grpcapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

func TestCallerIsPlatformAdmin(t *testing.T) {
	assert.False(t, CallerIsPlatformAdmin(nil))
	assert.False(t, CallerIsPlatformAdmin(&authn.Identity{RoleID: identity.RoleOwnerID, TeamID: "other"}))
	assert.False(t, CallerIsPlatformAdmin(&authn.Identity{RoleID: "builtin-admin", TeamID: identity.DefaultTeamID}))
	assert.True(t, CallerIsPlatformAdmin(&authn.Identity{RoleID: identity.RoleOwnerID, TeamID: identity.DefaultTeamID}))
}

func TestRequirePlatformAdmin(t *testing.T) {
	ctx := context.Background()
	_, _, err := CallerTeam(ctx)
	require.Error(t, err)
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "E_UNAUTHENTICATED", ae.Code())

	owner := &authn.Identity{RoleID: identity.RoleOwnerID, TeamID: identity.DefaultTeamID}
	require.NoError(t, RequirePlatformAdmin(authn.WithIdentity(ctx, owner)))

	otherTeamOwner := &authn.Identity{RoleID: identity.RoleOwnerID, TeamID: "team-a"}
	err = RequirePlatformAdmin(authn.WithIdentity(ctx, otherTeamOwner))
	require.Error(t, err)
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "E_FORBIDDEN", ae.Code(), "a non-default-team owner is not a platform admin")
}

func TestTeamAuthorized(t *testing.T) {
	ctx := context.Background()
	owner := &authn.Identity{RoleID: identity.RoleOwnerID, TeamID: identity.DefaultTeamID}
	require.NoError(t, TeamAuthorized(authn.WithIdentity(ctx, owner), "team-a"), "platform admin bypasses row-level team checks")

	member := &authn.Identity{TeamID: "team-a"}
	require.NoError(t, TeamAuthorized(authn.WithIdentity(ctx, member), "team-a"))

	err := TeamAuthorized(authn.WithIdentity(ctx, member), "team-b")
	require.Error(t, err)
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "E_FORBIDDEN", ae.Code())
}
