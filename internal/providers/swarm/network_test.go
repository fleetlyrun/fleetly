package swarm

import (
	"context"
	"errors"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Q-20 回归：材料 create-or-get 的 inspect 半边必须分诊错误——非
// NotFound（权限/连接类）上抛带原因，不得伪装 404 触发 create（只会撞
// 既有载体名，得到误导性的"already exists"）。夹具 cli 为 nil：若实现
// 在非 NotFound 错误下仍走 create 会立即 panic，即天然断言 create 未被
// 触碰。

func TestEnsureNetworksInspectErrorPropagates(t *testing.T) {
	cause := errors.New("daemon connection refused")
	p := &Provider{networkInspect: func(context.Context, string) error { return cause }}
	ws := []capability.Workload{{Networks: []string{"default", "internal"}}}

	err := p.ensureNetworks(context.Background(), capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}, ws)

	require.Error(t, err)
	assert.ErrorIs(t, err, cause, "inspect failure must surface its cause")
	assert.Contains(t, err.Error(), "inspect", "must fail at inspect, not masquerade as a create failure")
	// 载体名带出（可诊断）：错误消息能定位到具体网络载体。
	assert.Contains(t, err.Error(), "fleetly-net-shop-")
}

func TestEnsureSecretsInspectErrorPropagates(t *testing.T) {
	cause := errors.New("secret inspect: unauthorized")
	p := &Provider{secretInspect: func(context.Context, string) error { return cause }}
	m := capability.Materials{SecretFiles: map[string][]byte{ //nolint:gosec // 测试样本值（非凭据）
		"api-token": []byte("sample"),
	}}

	_, err := p.ensureSecrets(context.Background(), m)

	require.Error(t, err)
	assert.ErrorIs(t, err, cause, "inspect failure must surface its cause")
	assert.Contains(t, err.Error(), "inspect")
	assert.Contains(t, err.Error(), "fleetly-sec-api-token-")
}

// TestInspectTriageClassifiesNotFound：分诊的判定面——NotFound 哨兵
// 必须被识别为"走创建"，其余错误不伪装（isNotFound 的契约对账，
// 对照 service 路径先例）。
func TestInspectTriageClassifiesNotFound(t *testing.T) {
	assert.True(t, isNotFound(errdefs.ErrNotFound), "NotFound sentinel must route to create")
	assert.False(t, isNotFound(errors.New("daemon connection refused")), "other errors must not masquerade as NotFound")
	assert.False(t, isNotFound(nil))
}
