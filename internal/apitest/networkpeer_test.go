package apitest_test

// 跨 Project peer 声明面 e2e（F1.8，ADR-0013 附录 A）：declare → approve →
// revoke 全流程、自挂拒绝、重复声明 409、批准/撤销幂等、双方审计、事件
// 三拍、双侧过滤视图。

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/identity"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// peerEventNames 重放事件流，过滤 (aggregate, id) 的事件名序列。
func peerEventNames(t *testing.T, ctx context.Context, h *apitest.Harness, aggregate, id string) []string {
	t.Helper()
	stream, err := telemetryv1.NewEventsServiceClient(h.Conn).StreamEvents(ctx, &telemetryv1.StreamEventsRequest{Follow: false})
	require.NoError(t, err)
	var names []string
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if ev := frame.GetEvent(); ev.GetAggregate() == aggregate && ev.GetAggregateId() == id {
			names = append(names, ev.GetName())
		}
	}
	return names
}

func TestNetworkPeerLifecycle(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	// 接收方项目 + 网络；挂靠方项目。
	receiver, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "messaging"})
	require.NoError(t, err)
	consumer, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "torchwood"})
	require.NoError(t, err)
	net, err := networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
		ProjectId: receiver.GetProject().GetId(), Name: "bus",
	})
	require.NoError(t, err)

	// 自挂拒绝：项目内挂靠走 project-local 名。
	_, err = networks.DeclareNetworkPeer(ctx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: receiver.GetProject().GetId(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	// 双向声明第一拍：挂靠方 declare（pending）。
	declared, err := networks.DeclareNetworkPeer(ctx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: consumer.GetProject().GetId(),
	})
	require.NoError(t, err)
	peerID := declared.GetPeer().GetId()
	assert.Equal(t, "pending", declared.GetPeer().GetState())
	assert.Equal(t, "bus", declared.GetPeer().GetNetworkName())
	assert.Equal(t, receiver.GetProject().GetId(), declared.GetPeer().GetNetworkProjectId())

	// 同 (network, peer) 活跃声明重复 → AlreadyExists（幂等键重放不豁免
	// 活跃唯一——409 先于重放语义）。
	_, err = networks.DeclareNetworkPeer(ctx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: consumer.GetProject().GetId(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	// 第二拍：接收方 approve（approved + approved_at）。
	approved, err := networks.ApproveNetworkPeer(ctx, &structurev1.ApproveNetworkPeerRequest{Id: peerID})
	require.NoError(t, err)
	assert.Equal(t, "approved", approved.GetPeer().GetState())
	assert.NotEmpty(t, approved.GetPeer().GetApprovedAt())

	// 批准幂等：再批返回行现状。
	again, err := networks.ApproveNetworkPeer(ctx, &structurev1.ApproveNetworkPeerRequest{Id: peerID})
	require.NoError(t, err)
	assert.Equal(t, "approved", again.GetPeer().GetState())

	// 事件三拍（aggregate=network）。
	events := peerEventNames(t, ctx, h, "network", net.GetNetwork().GetId())
	assert.Equal(t, []string{"network.peer_declared", "network.peer_approved"}, events)

	// 双侧过滤视图。
	byNet, err := networks.ListNetworkPeers(ctx, &structurev1.ListNetworkPeersRequest{NetworkId: net.GetNetwork().GetId()})
	require.NoError(t, err)
	require.Len(t, byNet.GetPeers(), 1)
	byPeer, err := networks.ListNetworkPeers(ctx, &structurev1.ListNetworkPeersRequest{PeerProjectId: consumer.GetProject().GetId()})
	require.NoError(t, err)
	require.Len(t, byPeer.GetPeers(), 1)
	assert.Equal(t, peerID, byPeer.GetPeers()[0].GetId())

	// 撤销：revoked；幂等（再撤返回行现状）。
	revoked, err := networks.RevokeNetworkPeer(ctx, &structurev1.RevokeNetworkPeerRequest{Id: peerID})
	require.NoError(t, err)
	assert.Equal(t, "revoked", revoked.GetPeer().GetState())
	revokedAgain, err := networks.RevokeNetworkPeer(ctx, &structurev1.RevokeNetworkPeerRequest{Id: peerID})
	require.NoError(t, err)
	assert.Equal(t, "revoked", revokedAgain.GetPeer().GetState())

	// 已撤销的声明不可复活。
	_, err = networks.ApproveNetworkPeer(ctx, &structurev1.ApproveNetworkPeerRequest{Id: peerID})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	// 事件四拍收官。
	events = peerEventNames(t, ctx, h, "network", net.GetNetwork().GetId())
	assert.Equal(t, []string{"network.peer_declared", "network.peer_approved", "network.peer_revoked"}, events)

	// 双方审计：网络侧与挂靠项目侧各留痕（撤销拍）。
	netSide, err := auditq.ListAudit(ctx, &identityv1.ListAuditRequest{Action: "network.peer_revoke", Resource: "network/bus/peers/" + peerID})
	require.NoError(t, err)
	require.Len(t, netSide.GetEntries(), 1)
	peerSide, err := auditq.ListAudit(ctx, &identityv1.ListAuditRequest{Action: "network.peer_revoke", Resource: "project/" + consumer.GetProject().GetId() + "/peers/" + peerID})
	require.NoError(t, err)
	require.Len(t, peerSide.GetEntries(), 1)

	// 撤销后重新声明：新审批环（新行）+ 幂等键重放返回同一声明。
	idemCtx := sdk.WithIdempotencyKey(ctx, "peer-fresh")
	redeclared, err := networks.DeclareNetworkPeer(idemCtx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: consumer.GetProject().GetId(),
	})
	require.NoError(t, err)
	assert.NotEqual(t, peerID, redeclared.GetPeer().GetId())
	assert.Equal(t, "pending", redeclared.GetPeer().GetState())
	replay, err := networks.DeclareNetworkPeer(idemCtx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: consumer.GetProject().GetId(),
	})
	require.NoError(t, err)
	assert.Equal(t, redeclared.GetPeer().GetId(), replay.GetPeer().GetId(), "same key + body must replay the same declaration")
}

// TestApproveNetworkPeerReceiverTeamOnly（安全批 P1，ADR-0013 语义执法）：
// 批准是接收方（网络归属项目所属团队）的动作——另一 team 的 token 直批
// E_FORBIDDEN（PermissionDenied），同 team token 照常批准。
func TestApproveNetworkPeerReceiverTeamOnly(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	tokens := identityv1.NewTokensServiceClient(h.Conn)

	// 接收方项目（default team）+ 网络；挂靠方项目（default team，声明
	// 面不校验发起方归属——挂靠方本就任意项目发起）。
	receiver, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "receiver"})
	require.NoError(t, err)
	consumer, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "consumer"})
	require.NoError(t, err)
	net, err := networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
		ProjectId: receiver.GetProject().GetId(), Name: "bus",
	})
	require.NoError(t, err)
	declared, err := networks.DeclareNetworkPeer(ctx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: consumer.GetProject().GetId(),
	})
	require.NoError(t, err)
	peerID := declared.GetPeer().GetId()

	// 另一 team 的全权 token：scope 面全通过，归属面被拒（PermissionDenied）。
	other, err := teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: "outsiders"})
	require.NoError(t, err)
	foreignTok, err := tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "outsider-cli", TeamId: other.GetTeam().GetId(), RoleId: identity.RoleOwnerID,
	})
	require.NoError(t, err)
	foreignCtx := sdk.WithToken(context.Background(), foreignTok.GetSecret())

	_, err = networks.ApproveNetworkPeer(foreignCtx, &structurev1.ApproveNetworkPeerRequest{Id: peerID})
	require.Error(t, err, "a foreign-team token must not be able to self-approve a peer declaration")
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "E_FORBIDDEN")

	// 批准未发生：default team（接收方）随后照常批准成功。
	approved, err := networks.ApproveNetworkPeer(ctx, &structurev1.ApproveNetworkPeerRequest{Id: peerID})
	require.NoError(t, err)
	assert.Equal(t, "approved", approved.GetPeer().GetState())

	// 同 team 的第二个 token 也能走幂等批准（接收方团队内互信）。
	sameTeamTok, err := tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
		Name: "receiver-cli", TeamId: identity.DefaultTeamID, RoleId: identity.RoleOwnerID,
	})
	require.NoError(t, err)
	again, err := networks.ApproveNetworkPeer(sdk.WithToken(context.Background(), sameTeamTok.GetSecret()),
		&structurev1.ApproveNetworkPeerRequest{Id: peerID})
	require.NoError(t, err)
	assert.Equal(t, "approved", again.GetPeer().GetState())
}
