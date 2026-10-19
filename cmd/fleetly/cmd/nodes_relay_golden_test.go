package cmd

// nodes relay / revoke-relay 的 CLI 契约（H10 §4.10）：定向材料双形态、
// 缺参 64、服务端拒绝（manager 节点不可取节点侧材料）、吊销幂等双形态。
// 夹具：nodes 是观测缓存——manager node-1 行与 worker 行手动种入（不经
// reconcile 环：手动夹具无节拍，且 node.left 清理会删掉测试用凭证）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
)

// goldenWorkerNodeID 是 relay 定向材料的 worker 平台节点 ID（假 ID，非
// ULID——不进归一占位，golden 逐字确定）。
const goldenWorkerNodeID = "node-relay-golden"

// seedGoldenNode 落一行节点观测缓存（manager 拒绝负例与 worker 正例）。
func seedGoldenNode(t *testing.T, repo node.Repo, platformID, carrierID, role string) {
	t.Helper()
	require.NoError(t, repo.Upsert(context.Background(), &node.Node{
		PlatformID: platformID, CarrierID: carrierID, Hostname: platformID,
		Role: role, Available: true,
	}))
}

func TestGoldenNodesRelay(t *testing.T) {
	h := newGoldenHarness(t)
	seedGoldenNode(t, h.Services.Nodes, goldenNodeID, "apimanager", "manager")
	seedGoldenNode(t, h.Services.Nodes, goldenWorkerNodeID, "carrier-golden", "worker")

	// 人类形态：敏感提示行 + 脚本（脚本凭证随机——normalizeGolden 占位）。
	code, out, stderr := runCLI(t, "nodes", "relay", "--node", goldenWorkerNodeID)
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "nodes-relay", normalizeGolden(out))

	// --json 形态：EnrollNodeResponse protojson（relay_command 在场）。
	code, out, stderr = runCLI(t, "nodes", "relay", "--node", goldenWorkerNodeID, "--json")
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "nodes-relay-json", normalizeGolden(out))

	// 吊销双形态（第二次 --json 轮是幂等空删——输出同形）。
	for _, tc := range []struct {
		name string
		json bool
	}{
		{"nodes-revoke-relay", false},
		{"nodes-revoke-relay-json", true},
	} {
		args := []string{"nodes", "revoke-relay", "--node", goldenWorkerNodeID}
		if tc.json {
			args = append(args, "--json")
		}
		code, out, stderr = runCLI(t, args...)
		require.Equal(t, 0, code, "%s: stderr=%q", tc.name, stderr)
		require.Empty(t, stderr)
		compareGolden(t, tc.name, normalizeGolden(out))
	}

	// 缺参：执行前拒绝（exit 64，stdout 空）。
	code, out, stderr = runCLI(t, "nodes", "relay")
	require.Equal(t, exitUsage, code)
	require.Empty(t, out)
	compareGolden(t, "nodes-relay-missing-node", normalizeGolden(stderr))

	// 服务端拒绝：manager 节点的 relay 由进程内回环承载（E_INVALID_ARGUMENT）。
	code, out, stderr = runCLI(t, "nodes", "relay", "--node", goldenNodeID)
	require.Equal(t, 1, code)
	require.Empty(t, out)
	compareGolden(t, "nodes-relay-manager-node", normalizeGolden(stderr))

	// 吊销缺参同款 64。
	code, out, stderr = runCLI(t, "nodes", "revoke-relay")
	require.Equal(t, exitUsage, code)
	require.Empty(t, out)
	compareGolden(t, "nodes-revoke-relay-missing-node", normalizeGolden(stderr))
}
