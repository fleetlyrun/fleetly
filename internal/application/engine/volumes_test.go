package engine

// 卷附件边界解析与存量 adoption 契约测试白盒（ADR-0097）：受理咽喉 name→行
// id 解析进冻结体、未在册名受理即拒、存量冻结体（name 形）装载期双形
// 解析、GuardVolumeCarriers 两态（旧名载体在场且新键缺席即拒）。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// submitVolumeSpec 构造挂卷的 image 直投受理 spec（volumeId 可为 name 形
// 或 id 形——双形解析的两入口）。
func submitVolumeSpec(appID, volumeID string) *specv1.AppSpec {
	return &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: appID, Project: tProjectID},
		Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: "nginx:1.27"}}},
		Processes: []*specv1.ProcessSpec{{
			Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, Replicas: 1,
			Volumes: []*specv1.VolumeAttachment{{VolumeId: volumeID, Target: "/data"}},
		}},
	}
}

// volumeSpecJSON 铸同形态冻结体原文（legacy name 形的直落面）。
func volumeSpecJSON(appID, volumeID string) string {
	return fmt.Sprintf(`{"schema_version":1,"app":{"id":%q,"project":%q},`+
		`"source":{"image":{"ref":"nginx:1.27"}},"processes":[`+
		`{"name":"web","image":"nginx:1.27","replicas":1,`+
		`"volumes":[{"volume_id":%q,"target":"/data"}]}]}`, appID, tProjectID, volumeID)
}

// ① 受理咽喉解析：name 形附件在冻结前解析为卷行 id——冻结体恒 id 形
// （运行时零 name，ADR-0097 决策 2）；同 spec 复投幂等（id 形直通）。
func TestSubmitSpecResolvesVolumeNameToRowID(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	row := &volume.Volume{ID: "01jd0dataa0000000000000000", ProjectID: tProjectID, Name: "data"}
	require.NoError(t, e.repos.Volumes.Create(ctx, row))

	rev, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitVolumeSpec(tAppID, "data"),
	})
	require.NoError(t, err)
	frozen := frozenSpecUnmarshal(t, rev.Spec)
	got := frozen.GetProcesses()[0].GetVolumes()[0].GetVolumeId()
	assert.Equal(t, row.ID, got, "frozen spec carries the volume row id, not the name")
	assert.True(t, entityid.LooksMinted(got), "frozen volume references must be minted-id shaped (name backflow guard)")

	// 同 spec 复投：id 形直通（幂等重入零解析），内容寻址复用同 rev。
	again, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitVolumeSpec(tAppID, "data"),
	})
	require.NoError(t, err)
	assert.Equal(t, rev.ID, again.ID, "dual-form resolution is idempotent (content-addressed reuse holds)")
}

// ② 未在册卷名：受理即拒（ErrVolumeUnresolved——api 面 E_INVALID_ARGUMENT
// 带指向文案；不再留给部署期 pinVolumes 才炸）。
func TestSubmitSpecUnknownVolumeRejected(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	_, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitVolumeSpec(tAppID, "ghost"),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVolumeUnresolved)
	assert.Contains(t, err.Error(), `"web"`)
	assert.Contains(t, err.Error(), `"ghost"`)
}

// ③ 存量冻结体（ADR-0097 之前冻结、附件携带 name）：装载期双形解析为
// 行 id——内存态改写，冻结字节不动。
func TestLoadSpecResolvesLegacyVolumeNames(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	row := &volume.Volume{ID: "01jd0datab0000000000000000", ProjectID: tProjectID, Name: "data"}
	require.NoError(t, e.repos.Volumes.Create(ctx, row))

	revID := freezeSpec(t, e, 7, volumeSpecJSON(tAppID, "data"))
	spec, err := e.loadSpec(ctx, revID)
	require.NoError(t, err)
	assert.Equal(t, row.ID, spec.GetProcesses()[0].GetVolumes()[0].GetVolumeId(),
		"legacy name-form frozen specs resolve to the row id at load time")

	rev, err := e.repos.Revisions.Get(ctx, revID)
	require.NoError(t, err)
	assert.Contains(t, string(rev.Spec), `"data"`, "the frozen bytes stay untouched (in-memory rewrite only)")
}

// ④ 存量 adoption 契约测试两态（ADR-0097 决策 3）：新键（行 id）在场即过；
// 新键缺席而旧名键在场 = 显式拒绝（ErrLegacyVolumeCarrier 带迁移指引）；
// 双缺席（全新卷）通过。
func TestGuardVolumeCarriers(t *testing.T) {
	ctx := context.Background()
	ns := capability.NamespaceRef{Team: "default", Project: tProjectID, App: tAppID}
	// 每子测独立引擎：探测种子不跨子测泄漏（在场事实是累积态）。
	newCase := func(t *testing.T) (*Engine, *fakeRuntime, *volume.Volume, []capability.Workload) {
		t.Helper()
		e, rt, _ := newTestEngine(t)
		row := &volume.Volume{ID: "01jd0datac0000000000000000", ProjectID: tProjectID, Name: "data"}
		require.NoError(t, e.repos.Volumes.Create(ctx, row))
		ws := []capability.Workload{{
			ID: tAppID + "-web", Process: "web", Image: "nginx:1",
			Volumes: []capability.VolumeMount{{VolumeID: row.ID, Target: "/data"}},
		}}
		return e, rt, row, ws
	}

	t.Run("fresh volume passes", func(t *testing.T) {
		e, _, _, ws := newCase(t)
		require.NoError(t, e.GuardVolumeCarriers(ctx, ns, ws, tProjectID))
	})

	t.Run("current id-keyed carrier passes", func(t *testing.T) {
		e, rt, row, ws := newCase(t)
		rt.seedVolumeCarrier(row.ID)
		require.NoError(t, e.GuardVolumeCarriers(ctx, ns, ws, tProjectID))
	})

	t.Run("legacy name-keyed carrier is rejected", func(t *testing.T) {
		e, rt, row, ws := newCase(t)
		rt.seedVolumeCarrier(row.Name)
		err := e.GuardVolumeCarriers(ctx, ns, ws, tProjectID)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrLegacyVolumeCarrier)
		assert.Contains(t, err.Error(), "cannot be renamed", "the rejection carries migration guidance")
	})

	t.Run("unknown volume row fails closed", func(t *testing.T) {
		e, _, _, _ := newCase(t)
		ghost := []capability.Workload{{
			ID: tAppID + "-web", Process: "web", Image: "nginx:1",
			Volumes: []capability.VolumeMount{{VolumeID: "01jd0gh0st0000000000000000", Target: "/data"}},
		}}
		err := e.GuardVolumeCarriers(ctx, ns, ghost, tProjectID)
		require.Error(t, err, "a guard that cannot resolve the row must fail, not skip")
	})
}
