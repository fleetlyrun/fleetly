package apitest_test

// 节点个体 relay 凭证的 API 契约（H10 §4.4/§6 验收锚）：EnrollNode 三分支
//（初始 join-only / rotate 全量作废 + 断连 / 定向铸造与吊销）、定向材料只
// 影响目标节点、审计 Detail 只带 carrier/prefix（绝不落明文或完整 sha）、
// k3s（无 RuntimeRelayMaterial 面）501、manager 节点与组合参数拒绝。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

const (
	relayNodeA    = "node-relay-a"
	relayCarrierA = "carrier-relay-a"
	relayNodeB    = "node-relay-b"
	relayCarrierB = "carrier-relay-b"
)

// seedRelayNode 落一行节点观测缓存（定向材料的锚定前提——nodes 是观测
// 缓存，凭证只在节点已锚定后铸造）。
func seedRelayNode(t *testing.T, h *apitest.Harness, platformID, carrierID, role string) {
	t.Helper()
	require.NoError(t, h.Services.Nodes.Upsert(context.Background(), &node.Node{
		PlatformID: platformID, CarrierID: carrierID, Role: role, Available: true,
	}))
}

// issueRelayCredential 调 EnrollNode{node_id} 铸一枚凭证并返回明文（假
// Runtime 的 NodeRelayCommand 记录每次铸造的明文）。
func issueRelayCredential(t *testing.T, h *apitest.Harness, nodeID string) string {
	t.Helper()
	resp, err := runtimev1.NewNodesServiceClient(h.Conn).EnrollNode(h.Ctx(),
		&runtimev1.EnrollNodeRequest{NodeId: nodeID})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetRelayCommand())
	creds := h.Runtime.RelayCommands()
	require.NotEmpty(t, creds)
	return creds[len(creds)-1]
}

// relayCredentialActive 报告凭证是否仍是在册的中继凭证（engine 鉴权面
// 单源）；非"不在册"的错误（存储故障）直接失败——不得把故障当拒绝。
func relayCredentialActive(t *testing.T, h *apitest.Harness, secret string) bool {
	t.Helper()
	err := h.Engine.ValidateRelayCredential(context.Background(), secret)
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, engine.ErrExecRelayUnauthorized, "unexpected relay credential validation failure")
	return false
}

// relayAuditRow 是审计取证面的行投影（Detail 只在库里落账；ListAudit
// 读面尚未暴露 detail 字段——本文件直读 audit 表以钉住 Detail 契约）。
type relayAuditRow struct {
	Resource string
	Detail   string
}

// relayAuditEntries 读指定 action 的审计行（取证面真相 = audit 表）。
func relayAuditEntries(t *testing.T, h *apitest.Harness, action string) []relayAuditRow {
	t.Helper()
	rows, err := h.DB.Runner().QueryContext(context.Background(),
		`SELECT resource, detail FROM audit WHERE action = ? ORDER BY id DESC`, action)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []relayAuditRow
	for rows.Next() {
		var row relayAuditRow
		require.NoError(t, rows.Scan(&row.Resource, &row.Detail))
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestEnrollNodeInitialReturnsJoinOnly：初始 enroll 只回 join 命令
// （relay_command 空），且不触碰定向材料面（H10：join token 退出 relay 鉴权）。
func TestEnrollNodeInitialReturnsJoinOnly(t *testing.T) {
	h := apitest.New(t)
	resp, err := runtimev1.NewNodesServiceClient(h.Conn).EnrollNode(h.Ctx(), &runtimev1.EnrollNodeRequest{})
	require.NoError(t, err)
	assert.Contains(t, resp.GetJoinCommand(), "docker swarm join")
	assert.Empty(t, resp.GetRelayCommand(), "initial enrollment must not carry relay material")
	assert.Empty(t, h.Runtime.RelayCommands(), "the relay material face must not be called")
}

// TestEnrollNodeRelayMaterialTargetsNode：A/B 各一凭证；对 A 重铸只影响 A
// ——A 旧凭证即刻失效、A 新凭证可用、B 旧凭证仍可用；审计
// node.relay_credential_issued 的 Detail 含 A 的 carrier/prefix，不含明文
// 或完整 sha。
func TestEnrollNodeRelayMaterialTargetsNode(t *testing.T) {
	h := apitest.New(t)
	seedRelayNode(t, h, relayNodeA, relayCarrierA, "worker")
	seedRelayNode(t, h, relayNodeB, relayCarrierB, "worker")

	secretA := issueRelayCredential(t, h, relayNodeA)
	secretB := issueRelayCredential(t, h, relayNodeB)
	require.True(t, relayCredentialActive(t, h, secretA))
	require.True(t, relayCredentialActive(t, h, secretB))

	rotatedA := issueRelayCredential(t, h, relayNodeA)
	assert.NotEqual(t, secretA, rotatedA)
	assert.False(t, relayCredentialActive(t, h, secretA), "reissuing A must invalidate A's previous credential")
	assert.True(t, relayCredentialActive(t, h, rotatedA))
	assert.True(t, relayCredentialActive(t, h, secretB), "reissuing A must not touch B")

	// 审计：只记 carrier 与明文前缀（12 字符），绝不落明文/完整 sha。
	entries := relayAuditEntries(t, h, "node.relay_credential_issued")
	require.NotEmpty(t, entries)
	var detail string
	for _, e := range entries { // 新→旧：首条命中即最新一次铸造
		if e.Resource == "node/"+relayNodeA {
			detail = e.Detail
			break
		}
	}
	require.NotEmpty(t, detail)
	prefix := rotatedA[:12]
	assert.Contains(t, detail, relayCarrierA)
	assert.Contains(t, detail, prefix)
	assert.NotContains(t, detail, rotatedA, "audit detail must never carry the plaintext")
	assert.NotContains(t, detail, secretA)
	assert.NotContains(t, detail, node.HashRelayCredential(rotatedA), "audit detail must never carry the full sha")
}

// TestEnrollNodeRevokeRelayCredential：单节点吊销——A 行删除（旧凭证拒）、
// B 不受影响、响应两命令空；审计 node.relay_credential_revoked（Detail:
// carrier_id, reason=manual）。重复吊销幂等且不重复写审计。
func TestEnrollNodeRevokeRelayCredential(t *testing.T) {
	h := apitest.New(t)
	seedRelayNode(t, h, relayNodeA, relayCarrierA, "worker")
	seedRelayNode(t, h, relayNodeB, relayCarrierB, "worker")
	secretA := issueRelayCredential(t, h, relayNodeA)
	secretB := issueRelayCredential(t, h, relayNodeB)

	revoke := func() *runtimev1.EnrollNodeResponse {
		resp, err := runtimev1.NewNodesServiceClient(h.Conn).EnrollNode(h.Ctx(),
			&runtimev1.EnrollNodeRequest{NodeId: relayNodeA, Revoke: true})
		require.NoError(t, err)
		return resp
	}
	resp := revoke()
	assert.Empty(t, resp.GetJoinCommand())
	assert.Empty(t, resp.GetRelayCommand())
	assert.False(t, relayCredentialActive(t, h, secretA))
	assert.True(t, relayCredentialActive(t, h, secretB), "revoking A must not touch B")

	entries := relayAuditEntries(t, h, "node.relay_credential_revoked")
	require.Len(t, entries, 1, "exactly the deleting revoke leaves an audit row")
	assert.Equal(t, "node/"+relayNodeA, entries[0].Resource)
	assert.Contains(t, entries[0].Detail, relayCarrierA)
	assert.Contains(t, entries[0].Detail, "manual")
	assert.NotContains(t, entries[0].Detail, secretA)

	// 幂等空删：成功、无新审计。
	resp = revoke()
	assert.Empty(t, resp.GetRelayCommand())
	entries = relayAuditEntries(t, h, "node.relay_credential_revoked")
	assert.Len(t, entries, 1, "an idempotent no-op revoke must not append a second audit row")
}

// TestEnrollNodeRotateRevokesAllRelayCredentials：rotate 作废全部节点凭证 +
// 断开全部在连中继；响应只含 join；join token 仍不被 relay 接受。
func TestEnrollNodeRotateRevokesAllRelayCredentials(t *testing.T) {
	h := apitest.New(t)
	seedRelayNode(t, h, relayNodeA, relayCarrierA, "worker")
	seedRelayNode(t, h, relayNodeB, relayCarrierB, "worker")
	secretA := issueRelayCredential(t, h, relayNodeA)
	secretB := issueRelayCredential(t, h, relayNodeB)

	// 附着 A 的中继连接（rotate 必须即时断开它，而非只影响下次重连）。
	writer := &relayWriterStub{}
	_, detach, err := h.Engine.RelayAttach(context.Background(), secretA, relayCarrierA, "v1", writer)
	require.NoError(t, err)
	t.Cleanup(detach)
	require.Len(t, h.Engine.RelayStatuses(), 1)

	resp, err := runtimev1.NewNodesServiceClient(h.Conn).EnrollNode(h.Ctx(),
		&runtimev1.EnrollNodeRequest{Rotate: true})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetJoinCommand())
	assert.Empty(t, resp.GetRelayCommand(), "rotate returns join material only")

	assert.False(t, relayCredentialActive(t, h, secretA))
	assert.False(t, relayCredentialActive(t, h, secretB))
	assert.Empty(t, h.Engine.RelayStatuses(), "rotate must drop every connected relay")

	// 旧 join token 也不再被 relay 接受（H10：join token 退出鉴权面）。
	assert.False(t, relayCredentialActive(t, h, "TESTTOKEN"))
	_, _, err = h.Engine.RelayAttach(context.Background(), secretA, relayCarrierA, "v1", writer)
	require.ErrorIs(t, err, engine.ErrExecRelayUnauthorized)

	// rotate 审计 Detail 标记 relay 凭证全量作废。
	entries := relayAuditEntries(t, h, "node.rotate_join_tokens")
	require.NotEmpty(t, entries)
	assert.Contains(t, entries[0].Detail, "relay_credentials_revoked")
}

// TestEnrollNodeRelayMaterialRequiresAnchoredNode：未知 node_id → E_NOT_FOUND
// （节点尚未锚定/ID 写错——重试即可）。
func TestEnrollNodeRelayMaterialRequiresAnchoredNode(t *testing.T) {
	h := apitest.New(t)
	_, err := runtimev1.NewNodesServiceClient(h.Conn).EnrollNode(h.Ctx(),
		&runtimev1.EnrollNodeRequest{NodeId: "01MISSING0000000000000000"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Empty(t, h.Runtime.RelayCommands())
}

// TestEnrollNodeRelayMaterialRejectsCombos：rotate 与定向动作互斥；revoke
// 必须带 node_id（E_INVALID_ARGUMENT，拒绝零副作用）。
func TestEnrollNodeRelayMaterialRejectsCombos(t *testing.T) {
	h := apitest.New(t)
	nodes := runtimev1.NewNodesServiceClient(h.Conn)

	for _, req := range []*runtimev1.EnrollNodeRequest{
		{Rotate: true, NodeId: relayNodeA},
		{Rotate: true, Revoke: true},
		{Revoke: true},
	} {
		_, err := nodes.EnrollNode(h.Ctx(), req)
		require.Error(t, err, "request %+v must be rejected", req)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	assert.Empty(t, h.Runtime.EnrollCalls(), "rejected combinations must have zero side effects")
	assert.Empty(t, h.Runtime.RelayCommands())
}

// TestEnrollNodeRelayMaterialK3sUnsupported：集中形态无节点侧中继材料面
// （FacesOf.RelayMaterial == nil）→ E_RUNTIME_FACE_UNSUPPORTED（501）。
func TestEnrollNodeRelayMaterialK3sUnsupported(t *testing.T) {
	h := apitest.New(t)
	seedRelayNode(t, h, relayNodeA, relayCarrierA, "worker")
	h.Services.Runtime = apitest.NoRelayMaterial(h.Runtime)

	_, err := runtimev1.NewNodesServiceClient(h.Conn).EnrollNode(h.Ctx(),
		&runtimev1.EnrollNodeRequest{NodeId: relayNodeA})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_RUNTIME_FACE_UNSUPPORTED")
	assert.Equal(t, codes.Unimplemented, status.Code(err))
	assert.Empty(t, h.Runtime.RelayCommands())
}

// TestEnrollNodeRelayMaterialRejectsManagerNode：manager 节点的 relay 由
// 进程内回环承载（节点侧脚本会造成双 relay 抖动）→ E_INVALID_ARGUMENT；
// 空 carrier 无法绑定 → 同拒。
func TestEnrollNodeRelayMaterialRejectsManagerNode(t *testing.T) {
	h := apitest.New(t)
	seedRelayNode(t, h, "node-relay-manager", "carrier-relay-manager", "manager")
	seedRelayNode(t, h, "node-relay-nocarrier", "", "worker")
	nodes := runtimev1.NewNodesServiceClient(h.Conn)

	for _, nodeID := range []string{"node-relay-manager", "node-relay-nocarrier"} {
		_, err := nodes.EnrollNode(h.Ctx(), &runtimev1.EnrollNodeRequest{NodeId: nodeID})
		require.Error(t, err, "node %s must not receive relay material", nodeID)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	assert.Empty(t, h.Runtime.RelayCommands())
}

// relayWriterStub 是最小 RelayWriter（本文件只验连接注册/断开；无会话帧面）。
type relayWriterStub struct{ wrote int }

func (s *relayWriterStub) WriteRelayFrame([]byte) error {
	s.wrote++
	return nil
}
