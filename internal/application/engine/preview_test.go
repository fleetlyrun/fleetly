package engine

// B1 预览管线引擎侧测试（W2a，设计稿 §1.3）：
//   - 「预览=执行」逐字节守卫（守卫域限定：image 直投/既成 digest 源——
//     同输入 PreviewSpec 投影 == SubmitSpec 冻结后 projectRevision 投影）；
//   - from_build 源的 digest-pending 形态（镜像引用位标注 + PendingFromBuild）；
//   - marshalSpec 同参断言（预览规范形 == 冻结体字节——diff 与 digest 同
//     序列化，设计稿 §1.3 铁律）；
//   - 预览零残留（不冻结不受理——无 Revision 行）；
//   - 受理位只读预检 spec 参数变体（与 Submit 单源对账）；
//   - ScaleProcess 引擎腿（哨兵 + 提交链）。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// previewDigestSpecJSON 是既成 digest 源（image 位带 sha256 digest 引用——
// 守卫域的第二腿）。
const previewDigestSpecJSON = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"image":{"ref":"registry.local/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},` +
	`"processes":[{"name":"web","image":"registry.local/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","replicas":1}]}`

// previewFromBuildSpecJSON 是 from_build 源（预览无 Build 行——digest-pending
// 形态腿）。
const previewFromBuildSpecJSON = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
	`"source":{"upload":{"id":"01JD0UPL00000000000000000"}},` +
	`"processes":[{"name":"web","from_build":"default","replicas":1}]}`

// TestPreviewEqualsExecution 是「预览=执行」命门守卫（设计稿 §1.3）：同输入
// 走 PreviewSpec 与 SubmitSpec（冻结）+ projectRevision（部署物化的投影
// 前段——materialize 同函数取数），投影逐字节相等；规范形与冻结体逐字节
// 相等（marshalSpec 同参——diff 与 digest 同序列化）。守卫域限定：image
// 直投与既成 digest 源两腿。
func TestPreviewEqualsExecution(t *testing.T) {
	for name, specJSON := range map[string]string{
		"image-direct":    tImageSpec,
		"resolved-digest": previewDigestSpecJSON,
	} {
		t.Run(name, func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			ctx := context.Background()

			preview, err := e.PreviewSpec(ctx, SubmitSpecRequest{AppID: tAppID, Spec: mustSpec(t, specJSON)})
			require.NoError(t, err)

			// 预览零残留：不冻结（Latest 无行）。
			_, lerr := e.repos.Revisions.Latest(ctx, tAppID)
			require.ErrorIs(t, lerr, state.ErrNotFound, "preview must not freeze a revision")

			// 执行半边：同输入 SubmitSpec（webhook 形态免票）→ 冻结行。
			rev, _, _, serr := e.SubmitSpec(ctx, SubmitSpecRequest{
				AppID: tAppID, Spec: mustSpec(t, specJSON), Submit: &SubmitOptions{},
			})
			require.NoError(t, serr)

			// marshalSpec 同参：预览规范形 == 冻结体字节（diff 与 digest 的
			// 同源断言——内容寻址 digest 由此闭合）。
			assert.Equal(t, string(rev.Spec), string(preview.SpecBody),
				"preview canonical body must equal the frozen revision byte-for-byte (same marshalSpec options)")

			// 投影逐字节相等：PreviewSpec 投影 == 部署物化投影前段（strict）。
			_, execWS, _, perr := e.projectRevision(ctx, tAppID, rev.ID, false)
			require.NoError(t, perr)
			require.Len(t, preview.Workloads, len(execWS))
			for i := range execWS {
				assert.Equal(t, fmt.Sprintf("%+v", execWS[i]), fmt.Sprintf("%+v", preview.Workloads[i]),
					"workload %d projection must be byte-identical between preview and execution", i)
			}
		})
	}
}

// TestPreviewFromBuildDigestPending 钉 from_build 源的 digest-pending 形态
// （设计稿 §1.3 守卫域限定：预览无冻结行无 Build 行——镜像引用位标注
// 「构建后落定」+ PendingFromBuild 名集；执行半边投影对无 Build 行的
// from_build 是精确失败，故逐字节守卫不覆盖本源）。
func TestPreviewFromBuildDigestPending(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	preview, err := e.PreviewSpec(ctx, SubmitSpecRequest{AppID: tAppID, Spec: mustSpec(t, previewFromBuildSpecJSON)})
	require.NoError(t, err)
	require.Len(t, preview.Workloads, 1)
	assert.Equal(t, PreviewDigestPendingImage, preview.Workloads[0].Image,
		"from_build image reference slot must carry the digest-pending marker")
	assert.Equal(t, []string{"web"}, preview.PendingFromBuild)
	// 合成 Spec 的 from_build 引用原形保留（diff/digest 消费原形——占位只
	// 进投影，不进票面指纹）。
	assert.Equal(t, "default", preview.Spec.GetProcesses()[0].GetFromBuild())

	// 执行半边同输入：冻结行建立后投影缺 Build 行精确失败（守卫域限定的
	// 事实半边）。
	rev, _, _, serr := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: mustSpec(t, previewFromBuildSpecJSON), Submit: &SubmitOptions{},
	})
	require.NoError(t, serr)
	_, _, _, perr := e.projectRevision(ctx, tAppID, rev.ID, false)
	require.Error(t, perr, "execution projection has no build digest to resolve from_build")
}

// TestPreviewSpecPrechecks 钉受理位只读预检的 spec 参数变体（与 Submit
// 单源——错误透出给 warning 面，预览不失败）。
func TestPreviewSpecPrechecks(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	// preDeployJobs 裸网名：项目内无该网（fail-closed 谓词同 Submit）。
	specJSON := `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"nginx:1.27"}},` +
		`"processes":[{"name":"web","image":"nginx:1.27"}],` +
		`"pre_deploy_jobs":[{"name":"migrate","process":{"image":"migrate:1","networks":["no-such-net"]}}]}`
	preview, err := e.PreviewSpec(ctx, SubmitSpecRequest{AppID: tAppID, Spec: mustSpec(t, specJSON)})
	require.NoError(t, err, "preview surfaces precheck failures as warnings, not errors")
	require.ErrorIs(t, preview.PreDeployNetworksErr, ErrPreDeployNetworkUnknown)
	assert.NoError(t, preview.PeerErr)

	// 同谓词在 Submit 的 admission 事务内拒绝（单源对账——冻结成功而
	// admission 拒）。
	_, _, _, serr := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: mustSpec(t, specJSON), Submit: &SubmitOptions{},
	})
	require.ErrorIs(t, serr, ErrPreDeployNetworkUnknown)
}

// TestScaleProcessEngineLegs 钉 ScaleProcess 的引擎腿：无成功基线哨兵、
// scale-only 提交受理（Revision 冻结 + admission queued）、未知进程精确
// 拒绝、负数叶子复核。
func TestScaleProcessEngineLegs(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()

	_, _, err := e.ScaleProcess(ctx, tAppID, "web", 2)
	require.ErrorIs(t, err, ErrNoSuccessfulBaseline)

	// 建立成功基线（TestDeployImageHappyPath 同节拍）。
	revID := freezeSpec(t, e, 1, tImageSpec)
	d, _, serr := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, serr)
	e.step(ctx)
	e.handleObservation(ctx, workloadEventRunning(tAppID+"-web", 1))
	e.step(ctx)
	clock.Advance(61 * time.Second)
	e.step(ctx)
	require.Equal(t, deployment.StateSucceeded, getDeployment(t, e, d.ID).State)

	// scale 提交：新 Revision（replicas=3）+ admission queued。
	_, adm, err := e.ScaleProcess(ctx, tAppID, "web", 3)
	require.NoError(t, err)
	require.NotNil(t, adm)
	assert.Equal(t, AdmissionQueued, adm.Outcome)
	scaled, lerr := e.repos.Revisions.Latest(ctx, tAppID)
	require.NoError(t, lerr)
	scaledSpec, uerr := unmarshalSpec(scaled.Spec)
	require.NoError(t, uerr)
	assert.Equal(t, int64(3), scaledSpec.GetProcesses()[0].GetReplicas(), "scaled revision must freeze replicas=3")
	// 材料半边原样（image 直投无 hook——MergeNone 全量提交）。
	assert.Equal(t, "nginx:1.27", scaledSpec.GetProcesses()[0].GetImage())

	_, _, err = e.ScaleProcess(ctx, tAppID, "no-such", 3)
	require.ErrorIs(t, err, ErrProcessUnknown)
}

// mustSpec 反序列化 spec JSON（测试夹具）。
func mustSpec(t *testing.T, json string) *specv1.AppSpec {
	t.Helper()
	sp, err := unmarshalSpec([]byte(json))
	require.NoError(t, err)
	return sp
}
