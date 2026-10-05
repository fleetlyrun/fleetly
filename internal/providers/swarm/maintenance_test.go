package swarm

// 网络维护子面单测（ADR-0046）：重建原语在 fakeDaemon 上的行为钉死——
// 附着枚举（平台/外来双形态 + 域标记还原）、detach/attach 的 spec 网络
// 面改动与断路器账本作废、rm 的 in-use 排水重试、ensure 的 attachable +
// 标签同源创建。

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// maintenanceFixture 构造带 fakeDaemon 传输层的 Provider 与既有服务/网络。
func maintenanceFixture(t *testing.T) (*Provider, *fakeDaemon) {
	t.Helper()
	d := newFakeDaemon()
	p := &Provider{cli: d.newClient(t)}
	return p, d
}

// rebuildCarrierName 是测试内一致的载体网络名（公式真源在 network.go）。
func rebuildCarrierName(ns capability.NamespaceRef, network string) string {
	return carrierNetworkName(ns, network)
}

// seedAttachedService 预置一枚附着网络的平台服务（标签还原归属）。
func seedAttachedService(t *testing.T, d *fakeDaemon, ns capability.NamespaceRef, network, name string, labels map[string]string) {
	t.Helper()
	carrier := rebuildCarrierName(ns, network)
	spec := swarm.ServiceSpec{Annotations: swarm.Annotations{
		Name:   name,
		Labels: labels,
	}}
	spec.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{Target: "net-" + carrier, Aliases: []string{"web"}}}
	d.addService(spec)
}

// platformLabels 构造 App 域载体标记（workloadLabels 的最小形态）。
func platformLabels(ns capability.NamespaceRef) map[string]string {
	return map[string]string{
		labelManaged:  "true",
		labelTeam:     ns.Team,
		labelProject:  ns.Project,
		labelApp:      ns.App,
		labelWorkload: "wl-1",
	}
}

// InspectNetwork：平台/外来附着双形态 + 域标记还原 + attachable/managed 面。
func TestInspectNetworkAttachments(t *testing.T) {
	p, d := maintenanceFixture(t)
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	carrier := rebuildCarrierName(ns, "default")
	d.nets[carrier] = "net-" + carrier
	d.netDetails[carrier] = networkInspectOf(carrier, false, true)

	seedAttachedService(t, d, ns, "default", "fleetly-acme-shop-web-web", platformLabels(ns))
	// 外来服务（无平台标记）也附着。
	foreign := swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "foreign-svc"}}
	foreign.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{Target: "net-" + carrier}}
	d.addService(foreign)

	state, err := p.InspectNetwork(context.Background(), ns, "default")
	require.NoError(t, err)
	require.True(t, state.Exists)
	assert.False(t, state.Attachable, "legacy carrier is not attachable")
	assert.True(t, state.Managed)
	require.Len(t, state.Attachments, 2)
	// 排序稳定（载体名序）：fleetly-acme-shop-web-web < foreign-svc。
	assert.Equal(t, "fleetly-acme-shop-web-web", state.Attachments[0].Carrier)
	assert.Equal(t, "wl-1", state.Attachments[0].Workload)
	assert.Equal(t, capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}, state.Attachments[0].Domain)
	assert.Empty(t, state.Attachments[1].Workload, "foreign carrier carries no platform workload id")

	// 缺席形态：Exists=false 非错误。
	state, err = p.InspectNetwork(context.Background(), ns, "missing")
	require.NoError(t, err)
	assert.False(t, state.Exists)
}

// Detach/Attach：spec 网络面摘除与还原（附件形状含 Aliases 快照还原）、
// 断路器账本作废（下一拍 Ensure 不再被 no-op 短路）。
func TestDetachAttachNetworkCarrier(t *testing.T) {
	p, d := maintenanceFixture(t)
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	carrier := rebuildCarrierName(ns, "default")
	d.nets[carrier] = "net-" + carrier
	d.netDetails[carrier] = networkInspectOf(carrier, false, true)
	seedAttachedService(t, d, ns, "default", "fleetly-acme-shop-web-web", platformLabels(ns))

	// 账本预热：服务 spec 已被 Ensure 确认持有的形态。
	svc, ok := d.byRef("fleetly-acme-shop-web-web")
	require.True(t, ok)
	p.recordLastIssued(svc.Spec.Name, canonicalSpecJSON(svc.Spec))
	require.NotEmpty(t, p.lastIssuedOf(svc.Spec.Name))

	require.NoError(t, p.DetachNetwork(context.Background(), ns, "default", "fleetly-acme-shop-web-web"))
	after, ok := d.byRef("fleetly-acme-shop-web-web")
	require.True(t, ok)
	assert.Empty(t, after.Spec.TaskTemplate.Networks, "detach removes the network target from the service spec")
	assert.Empty(t, p.lastIssuedOf(after.Spec.Name), "detach invalidates the no-op breaker ledger entry")

	// 幂等：再摘一次 no-op。
	require.NoError(t, p.DetachNetwork(context.Background(), ns, "default", "fleetly-acme-shop-web-web"))

	// rm + ensure（attachable 同源创建）。
	require.NoError(t, p.RemoveNetwork(context.Background(), ns, "default"))
	_, still := d.nets[carrier]
	assert.False(t, still)
	require.NoError(t, p.EnsureNetwork(context.Background(), ns, "default"))
	creates := d.networkCreates()
	require.Len(t, creates, 1)
	assert.Equal(t, carrier, creates[0].name)
	assert.True(t, creates[0].opts.Attachable, "recreate is attachable (ensureNetworks same source)")
	assert.Equal(t, "overlay", creates[0].opts.Driver)
	assert.Equal(t, map[string]string{
		labelNetManaged:  "true",
		labelNetProject:  ns.Project,
		labelNetPlatform: "default",
	}, creates[0].opts.Labels)

	// attach：附件形状还原（Aliases 保留）+ Target 换新网络 ID。
	require.NoError(t, p.AttachNetwork(context.Background(), ns, "default", "fleetly-acme-shop-web-web"))
	after, ok = d.byRef("fleetly-acme-shop-web-web")
	require.True(t, ok)
	require.Len(t, after.Spec.TaskTemplate.Networks, 1)
	assert.Equal(t, "net-"+carrier, after.Spec.TaskTemplate.Networks[0].Target)
	assert.Equal(t, []string{"web"}, after.Spec.TaskTemplate.Networks[0].Aliases,
		"the detached attachment shape (aliases) is restored on re-attach")

	// 幂等：已附着再 attach no-op（不追加第二附件）。
	require.NoError(t, p.AttachNetwork(context.Background(), ns, "default", "fleetly-acme-shop-web-web"))
	after, _ = d.byRef("fleetly-acme-shop-web-web")
	assert.Len(t, after.Spec.TaskTemplate.Networks, 1)
}

// RemoveNetwork 的排水重试：in-use（附着端点未清空）按退避重试至成功；
// 其余错误如实上抛（不重试）。rm 成功后轮询等异步退役落地（swarm
// overlay 删除的成功返回先于网络真消失——dind e2e 实证形态）。
func TestRemoveNetworkDrainRetry(t *testing.T) {
	p, d := maintenanceFixture(t)
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	carrier := rebuildCarrierName(ns, "default")
	d.nets[carrier] = "net-" + carrier
	d.netDetails[carrier] = networkInspectOf(carrier, false, true)

	// 前两次 in-use（detach 触发的滚动替换在排水），第三次成功；删除后
	// inspect 残留两次（异步退役窗）——RemoveNetwork 必须等 NotFound 才返回。
	d.netInUse[carrier] = 2
	d.netLinger[carrier] = 2
	start := time.Now()
	require.NoError(t, p.RemoveNetwork(context.Background(), ns, "default"))
	assert.GreaterOrEqual(t, time.Since(start), 2*networkDrainRetry, "drain retries back off between attempts")
	_, still := d.nets[carrier]
	assert.False(t, still)
	assert.Zero(t, d.netLinger[carrier], "retirement confirmation consumed the lingering window")

	// 非的 in-use 错误（注入 500）不重试：上抛。
	d.nets[carrier] = "net-" + carrier
	d.failNext("DELETE /networks/"+carrier, 3)
	err := p.RemoveNetwork(context.Background(), ns, "default")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "did not drain")
}

// networkInspectOf 构造富形态 inspect（标签/attachable 断言面）。
func networkInspectOf(carrier string, attachable, managed bool) network.Inspect {
	labels := map[string]string{}
	if managed {
		labels[labelNetManaged] = "true"
	}
	return network.Inspect{Network: network.Network{Name: carrier, ID: "net-" + carrier, Attachable: attachable, Labels: labels}}
}
