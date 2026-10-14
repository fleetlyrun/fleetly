package apitest_test

// exec 会话 gRPC 消费径的行级授权边界（P0-1 S1，2026-10-14）：会话 ID 是
// 进程内单调序号可枚举，他队持 exec:write 抢附着必须 E_FORBIDDEN——他队
// owner 亦不豁免（与 authz.go 判定同源；Console 票据径在签发时已鉴权）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestExecSessionAttachTeamBoundary(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	appID := execSeed(t, h, ctx)
	detach := apitest.AttachFakeRelay(context.Background(), h)
	defer detach()
	h.Runtime.SetExecBehavior(0, "hello\n", false)

	exec := runtimev1.NewExecServiceClient(h.Conn)
	resp, err := exec.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: "web", Command: []string{"echo", "hi"},
	})
	require.NoError(t, err)

	// 第二队 owner（scope 面全通行级；行级不豁免）抢附着。
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	team, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "intruders"})
	require.NoError(t, err)
	other := sdk.WithToken(context.Background(),
		mintTeamToken(t, h, "implant", team.GetTeam().GetId(), identity.RoleOwnerID))

	stream, err := exec.StreamExecSession(other)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&runtimev1.StreamExecSessionRequest{
		Frame: &runtimev1.StreamExecSessionRequest_AttachSessionId{
			AttachSessionId: resp.GetSession().GetId(),
		},
	}))
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_FORBIDDEN")

	// 属主队消费径不受影响（同会话仍可正常附着——收 meta 首帧即证）。
	ownerStream, err := exec.StreamExecSession(ctx)
	require.NoError(t, err)
	require.NoError(t, ownerStream.Send(&runtimev1.StreamExecSessionRequest{
		Frame: &runtimev1.StreamExecSessionRequest_AttachSessionId{
			AttachSessionId: resp.GetSession().GetId(),
		},
	}))
	frame, err := ownerStream.Recv()
	require.NoError(t, err)
	assert.NotNil(t, frame.GetMeta(), "owner-team attach must receive the meta first frame")
}
