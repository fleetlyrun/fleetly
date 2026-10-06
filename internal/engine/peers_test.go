package engine

// 跨 Project 网络引用（F1.8，ADR-0013 附录 A）：strict 受理拒（未批准）、
// approved 投影为 NetworkRefs（目标团队从 Project 行实取）、撤销即时隔离
//（isolate 重收敛剥离附件）与漂移扫描拍自愈。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/project"
)

// seedCrossProjectPeer 跨 Project 夹具：接收方项目（platform 团队）+
// 其网络 + peer 声明（approve=true 时已批准）。返回接收方项目 ID。
func seedCrossProjectPeer(t *testing.T, e *Engine, approve bool) string {
	t.Helper()
	ctx := context.Background()
	const providerID = "01JD0PROJ00000000000000007"
	require.NoError(t, project.New(e.clock).Create(ctx, e.db.Runner(), &project.Project{
		ID: providerID, Name: "messaging", TeamID: "platform",
	}))
	require.NoError(t, e.networks.Create(ctx, e.db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000007", ProjectID: providerID, Name: "bus",
	}))
	peer := &networkpeer.Peer{ID: "01JD0PEER00000000000000007", NetworkID: "01JD0NET000000000000000007", PeerProjectID: tProjectID}
	require.NoError(t, e.peerDecls.Create(ctx, e.db.Runner(), peer))
	if approve {
		require.NoError(t, e.peerDecls.Approve(ctx, e.db.Runner(), peer.ID))
	}
	return providerID
}

// peerSpec 构造引用跨 Project 网络的 AppSpec 冻结体。
func peerSpec(providerID string) string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},` +
		`"processes":[{"name":"web","image":"nginx:1.27","replicas":1,` +
		`"networks":["project:` + providerID + `/bus","default"]}]}`
}

// strict fail-closed：未批准引用在受理位被拒（不入队）；批准后可部署且
// 投影携带跨域附件（目标团队从接收方 Project 行实取）。
func TestCrossProjectPeerStrictAdmission(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	providerID := seedCrossProjectPeer(t, e, false)

	revID := freezeSpec(t, e, 1, peerSpec(providerID))
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.ErrorIs(t, err, ErrCrossProjectRefNotApproved, "unapproved reference must be rejected at admission")
	assert.Contains(t, err.Error(), "project:"+providerID+"/bus")

	// 批准后同一 Revision 可受理，投影携带跨域附件。
	require.NoError(t, e.peerDecls.Approve(ctx, e.db.Runner(), "01JD0PEER00000000000000007"))
	d := deployToSucceeded(t, e, revID)
	require.Equal(t, deployment.StateSucceeded, d.State)

	w := lastEnsure(rt).Spec["web"]
	require.Len(t, w.NetworkRefs, 1, "approved reference must project as a cross-domain attachment")
	assert.Equal(t, capability.NetworkRef{
		Namespace: capability.NamespaceRef{Team: "platform", Project: providerID},
		Name:      "bus",
	}, w.NetworkRefs[0], "attachment namespace carries the receiving project's real team")
	assert.Equal(t, []string{"default"}, w.Networks, "same-domain networks must not mix with cross-domain refs")
}

// 撤销即时隔离：撤销落账后 IsolateNetworkPeer 以 isolate 重收敛——附件
// 从下一次 Ensure 消失（断存量：service update 全量替换即断连接）。
func TestCrossProjectPeerRevokeIsolates(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	providerID := seedCrossProjectPeer(t, e, true)

	revID := freezeSpec(t, e, 1, peerSpec(providerID))
	deployToSucceeded(t, e, revID)
	require.Len(t, lastEnsure(rt).Spec["web"].NetworkRefs, 1)

	// 撤销（行落账由 API 层承载；此处直接打 repo）→ 即时隔离。
	require.NoError(t, e.peerDecls.Revoke(ctx, e.db.Runner(), "01JD0PEER00000000000000007"))
	require.NoError(t, e.IsolateNetworkPeer(ctx, "01JD0NET000000000000000007", tProjectID))

	w := lastEnsure(rt).Spec["web"]
	assert.Empty(t, w.NetworkRefs, "revoked attachment must be stripped by the isolation reconverge")
	assert.Equal(t, []string{"default"}, w.Networks, "same-domain networks survive the strip")

	// 撤销后再部署同引用：受理位重新拒绝（fail-closed 重声明）。
	_, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.ErrorIs(t, err, ErrCrossProjectRefNotApproved)
}

// 自愈兜底：撤销后未走显式 Isolate（模拟剥离 Ensure 失败/竞态窗口），
// 下一拍漂移扫描复核批准态并 isolate 重收敛。
func TestCrossProjectPeerIsolationSelfHeals(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	providerID := seedCrossProjectPeer(t, e, true)

	revID := freezeSpec(t, e, 1, peerSpec(providerID))
	deployToSucceeded(t, e, revID)

	// 只撤销行（跳过显式隔离入口）→ 漂移扫描拍收敛。
	require.NoError(t, e.peerDecls.Revoke(ctx, e.db.Runner(), "01JD0PEER00000000000000007"))
	e.driftScan(ctx)
	assert.Empty(t, lastEnsure(rt).Spec["web"].NetworkRefs, "the isolation invariant must re-converge on the drift tick")
}

// B13 回归：IsolateNetworkPeer 的 LatestSucceeded 非 NotFound 错误（存储
// 故障）不吞——吞掉 = 剥离静默跳过 = 假隔离。NotFound 仍是唯一合法的
// "无基线可剥离"形态（锚定 App 无成功基线时隔离照常走完，不报错）。
func TestIsolateNetworkPeerStorageFaultPropagates(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	providerID := seedCrossProjectPeer(t, e, true)

	revID := freezeSpec(t, e, 1, peerSpec(providerID))
	deployToSucceeded(t, e, revID)

	// 存储故障注入（builder_test 同款形态）：基线读面不可用。
	_, err := e.db.Runner().ExecContext(ctx, `DROP TABLE deployments`)
	require.NoError(t, err)

	err = e.IsolateNetworkPeer(ctx, "01JD0NET000000000000000007", tProjectID)
	require.Error(t, err, "a storage fault in the baseline lookup must not be swallowed")
	assert.Contains(t, err.Error(), tAppID, "the error names the app whose baseline lookup failed")

	// NotFound（无成功基线）仍视为"无可剥离"：另一 App 无基线 → 走完不报错。
	e2, _, _ := newTestEngine(t)
	seedCrossProjectPeer(t, e2, true)
	require.NoError(t, e2.IsolateNetworkPeer(ctx, "01JD0NET000000000000000007", tProjectID))
}

// 引用目标缺失（项目/网络不存在）在 strict 与 isolate 两侧的口径：
// strict 拒；isolate 剥离。
func TestCrossProjectPeerMissingTarget(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	ghost := "01JD0PROJ000000000000000ZZ"
	spec := &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: tAppID, Project: tProjectID},
		Processes: []*specv1.ProcessSpec{{
			Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"},
			Networks: []string{"project:" + ghost + "/bus"},
		}},
	}
	_, err := e.resolvePeerRefs(ctx, e.db.Runner(), tProjectID, spec, false)
	require.ErrorIs(t, err, ErrCrossProjectRefNotApproved)
	refs, err := e.resolvePeerRefs(ctx, e.db.Runner(), tProjectID, spec, true)
	require.NoError(t, err)
	assert.Empty(t, refs.Refs, "isolate mode omits unresolvable references")
}

// 投影前缀词面回归：Project 对 taskGroup/project 前缀各自翻译，互不吞；
// strict 缺席 fail-closed、isolate 缺席剥离。
func TestProjectTranslatesCrossProjectRef(t *testing.T) {
	spec := &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: "01JAPP", Project: "shop"},
		Processes: []*specv1.ProcessSpec{{
			Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"},
			Networks: []string{"default", "taskGroup:dispatcher", "project:01JD0PROJ00000000000000007/bus"},
		}},
	}
	approved := PeerRefs{Refs: map[string]capability.NetworkRef{
		"project:01JD0PROJ00000000000000007/bus": {Namespace: capability.NamespaceRef{Team: "platform", Project: "01JD0PROJ00000000000000007"}, Name: "bus"},
	}}
	ws, _, err := Project(spec, "acme", "torchwood", nil, approved)
	require.NoError(t, err)
	require.Len(t, ws, 1)
	assert.Equal(t, []string{"default", "taskgrp-dispatcher"}, ws[0].Networks, "only same-domain and taskGroup names stay in Networks")
	require.Len(t, ws[0].NetworkRefs, 1)

	_, _, err = Project(spec, "acme", "torchwood", nil, PeerRefs{})
	require.ErrorContains(t, err, "not approved by the receiving project")

	ws, _, err = Project(spec, "acme", "torchwood", nil, PeerRefs{Isolate: true})
	require.NoError(t, err)
	require.Len(t, ws, 1)
	assert.Equal(t, []string{"default", "taskgrp-dispatcher"}, ws[0].Networks)
	assert.Empty(t, ws[0].NetworkRefs)
}
