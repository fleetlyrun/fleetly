package engine

// SubmitSpec 咽喉白盒测试（P3 批 7 第 1 步）：行为自 api 层 freezeRevision
// + hook 合并块下沉，本文件是移植语义的行为锚——内容寻址复用（零写零
// 事实）、三级变量合成序、合并方向参数序、只冻结形态、admission 透传、
// 防御断言与 FrozenSpecError 文案形态。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
)

// submitImageSpec 构造 image 直投形态的受理 spec（env 可空）。
func submitImageSpec(appID, ref string, env map[string]string) *specv1.AppSpec {
	p := &specv1.ProcessSpec{
		Name:        "web",
		ImageOrigin: &specv1.ProcessSpec_Image{Image: ref},
		Replicas:    1,
	}
	if env != nil {
		p.Env = env
	}
	return &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: appID, Project: tProjectID},
		Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: ref}}},
		Processes:     []*specv1.ProcessSpec{p},
	}
}

// frozenSpecUnmarshal 反序列化冻结体（与合并读侧同参——DiscardUnknown）。
func frozenSpecUnmarshal(t *testing.T, body []byte) *specv1.AppSpec {
	t.Helper()
	s := &specv1.AppSpec{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, s))
	return s
}

// ① 同 Spec 两次 SubmitSpec → 内容寻址复用：同 rev、revisions 单行、
// revision.create 审计单行（复用零写零事实，不变式①④）。
func TestSubmitSpecReusesIdenticalSpec(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	submitted := submitImageSpec(tAppID, "nginx:1.27", nil)

	r1, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{AppID: tAppID, Spec: submitted})
	require.NoError(t, err)
	r2, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{AppID: tAppID, Spec: submitted})
	require.NoError(t, err)

	assert.Equal(t, r1.ID, r2.ID, "identical spec must reuse the frozen revision")
	revs, err := e.revisions.ListByApp(ctx, e.db.Runner(), tAppID, 0, 100)
	require.NoError(t, err)
	assert.Len(t, revs, 1, "reuse freezes exactly one row")
	assert.Equal(t, []string{"revision.create"}, auditActions(t, e, "revision/"),
		"reuse must not append a second audit row")
}

// ② 三级合成序（不变式②）：Shared 底 < 源声明 env 中 < AppVariable 顶
// ——同键由顶层覆盖，冻结体即最终生效集。
func TestSubmitSpecVariableLayering(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	require.NoError(t, e.sharedVariables.Upsert(ctx, e.db.Runner(),
		&sharedvariable.SharedVariable{ProjectID: tProjectID, Name: "LOG_LEVEL", Value: "from-shared"}))
	require.NoError(t, e.sharedVariables.Upsert(ctx, e.db.Runner(),
		&sharedvariable.SharedVariable{ProjectID: tProjectID, Name: "SHARED_ONLY", Value: "shared"}))
	require.NoError(t, e.appVariables.Upsert(ctx, e.db.Runner(),
		&appvariable.AppVariable{AppID: tAppID, Name: "LOG_LEVEL", Value: "from-app"}))

	submitted := submitImageSpec(tAppID, "nginx:1.27",
		map[string]string{"LOG_LEVEL": "from-source", "SOURCE_ONLY": "source"})
	rev, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{AppID: tAppID, Spec: submitted})
	require.NoError(t, err)

	env := frozenSpecUnmarshal(t, rev.Spec).GetProcesses()[0].GetEnv()
	assert.Equal(t, "from-app", env["LOG_LEVEL"], "AppVariable overrides shared and source layers")
	assert.Equal(t, "shared", env["SHARED_ONLY"])
	assert.Equal(t, "source", env["SOURCE_ONLY"])
}

// ③ 合并方向参数序（不变式③，ADR-0072）：MergeOverlay = MergeHookOwned(
// 冻结体, 提交件)——材料半边取冻结侧、运行时半边取提交件（提交件 source
// 忽略）；MergeMaterial = MergeHookOwned(提交件, 冻结体)——新材料 + 冻结
// 运行时。
func TestSubmitSpecMergeDirections(t *testing.T) {
	ctx := context.Background()

	t.Run("overlay: frozen material + submitted runtime", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		freezeSpecForApp(t, e, tAppID, 1, imageSpecForApp(tAppID, "nginx:1.26"))
		submitted := &specv1.AppSpec{
			SchemaVersion: 1,
			App:           &specv1.AppRef{Id: tAppID, Project: tProjectID},
			Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: "nginx:9.9"}}},
			Processes: []*specv1.ProcessSpec{{
				Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:9.9"}, Replicas: 3,
			}},
		}
		rev, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
			AppID: tAppID, Spec: submitted, HookOwned: true, Merge: MergeOverlay,
		})
		require.NoError(t, err)
		frozen := frozenSpecUnmarshal(t, rev.Spec)
		assert.Equal(t, "nginx:1.26", frozen.GetSource().GetImage().GetRef(),
			"overlay takes the material half from the frozen side (stale-ref build guard)")
		assert.Equal(t, int64(3), frozen.GetProcesses()[0].GetReplicas(),
			"overlay takes the runtime half from the submission")
		assert.Equal(t, "nginx:9.9", frozen.GetProcesses()[0].GetImage())
	})

	t.Run("material: submitted material + frozen runtime", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		freezeSpecForApp(t, e, tAppID, 1, imageSpecForApp(tAppID, "nginx:1.26"))
		submitted := &specv1.AppSpec{
			SchemaVersion: 1,
			App:           &specv1.AppRef{Id: tAppID, Project: tProjectID},
			Source: &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{
				Repo: "https://git.example.com/acme/web.git", Ref: "abc123",
			}}},
			Build: &specv1.BuildSpec{Builder: "dockerfile",
				Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: "Dockerfile"}},
			Processes: []*specv1.ProcessSpec{{
				Name: "web", ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "web"}, Replicas: 1,
			}},
		}
		rev, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
			AppID: tAppID, Spec: submitted, HookOwned: true, Merge: MergeMaterial,
		})
		require.NoError(t, err)
		frozen := frozenSpecUnmarshal(t, rev.Spec)
		assert.Equal(t, "https://git.example.com/acme/web.git", frozen.GetSource().GetGit().GetRepo(),
			"push takes the material half from the submission")
		assert.Equal(t, "Dockerfile", frozen.GetBuild().GetDockerfile())
		assert.Equal(t, "nginx:1.26", frozen.GetProcesses()[0].GetImage(),
			"push keeps the runtime half from the frozen side")
	})

	// 首部署形态：无冻结 Revision（hook 配置后从未部署）→ 提交件原样冻结。
	t.Run("no frozen revision: first deploy as-is", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		submitted := submitImageSpec(tAppID, "nginx:1.27", nil)
		rev, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
			AppID: tAppID, Spec: submitted, HookOwned: true, Merge: MergeOverlay,
		})
		require.NoError(t, err)
		frozen := frozenSpecUnmarshal(t, rev.Spec)
		assert.Equal(t, "nginx:1.27", frozen.GetSource().GetImage().GetRef(),
			"first deploy takes the submission as-is")
	})
}

// ③b 冻结体损坏：FrozenSpecError 类型化错误，文案逐字节保持 api 原形态
// （"<site>: frozen revision is not valid protojson (<err>)"——api 层映射
// E_INTERNAL 以 err.Error() 原文上信封）。
func TestSubmitSpecFrozenSpecError(t *testing.T) {
	ctx := context.Background()

	t.Run("overlay site", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		freezeSpecForApp(t, e, tAppID, 1, "{not-protojson")
		_, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
			AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil),
			HookOwned: true, Merge: MergeOverlay,
		})
		var fse *FrozenSpecError
		require.ErrorAs(t, err, &fse)
		assert.Equal(t, "hook-owned overlay", fse.Site)
		assert.Equal(t, "hook-owned overlay: frozen revision is not valid protojson ("+fse.Err.Error()+")", fse.Error())
		require.ErrorIs(t, err, fse.Err, "Unwrap must expose the cause")
	})

	t.Run("material site", func(t *testing.T) {
		e, _, _ := newTestEngine(t)
		freezeSpecForApp(t, e, tAppID, 1, "{not-protojson")
		_, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
			AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil),
			HookOwned: true, Merge: MergeMaterial,
		})
		var fse *FrozenSpecError
		require.ErrorAs(t, err, &fse)
		assert.Equal(t, "hook merge", fse.Site)
	})
}

// ④ 只冻结形态：Submit = nil → 不走 admission（零部署行、零事件），
// d/adm 返回 nil。
func TestSubmitSpecFreezeOnly(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev, d, adm, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil),
	})
	require.NoError(t, err)
	require.NotNil(t, rev)
	assert.Nil(t, d)
	assert.Nil(t, adm)
	assert.Empty(t, eventNames(t, e, ""), "freeze-only admits no deployment (LedgerOnly audit)")
}

// ⑤ admission 透传：Submit 非 nil → 独立事务受理（部署行 queued、判定
// 附注在册、ToRevision 锚冻结行）。queue-full 形态由既有 admission 测试
// 与第 3 步 apitest 承载，此处不重复造景。
func TestSubmitSpecAdmitsDeployment(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	rev, d, adm, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil),
		Submit: &SubmitOptions{IdempotencyKey: "k1"},
	})
	require.NoError(t, err)
	require.NotNil(t, d)
	require.NotNil(t, adm)
	assert.Equal(t, deployment.StateQueued, d.State)
	assert.Equal(t, AdmissionQueued, adm.Outcome)
	assert.Equal(t, rev.ID, d.ToRevision)
}

// ⑥ 防御断言（编程错误防线）：AppID/Spec 缺失、合并无所有权、未知合并
// 方向——api 受理位已前置执法，此处只拦装配错误。
func TestSubmitSpecDefensiveGuards(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	_, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{Spec: submitImageSpec(tAppID, "nginx:1.27", nil)})
	assert.EqualError(t, err, "engine: submit spec: app_id and spec are required")
	_, _, _, err = e.SubmitSpec(ctx, SubmitSpecRequest{AppID: tAppID})
	assert.EqualError(t, err, "engine: submit spec: app_id and spec are required")
	_, _, _, err = e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil), Merge: MergeOverlay,
	})
	assert.EqualError(t, err,
		"engine: submit spec: merge without hook ownership is a programming error (the api gate must set HookOwned for MergeMaterial/MergeOverlay)")
	_, _, _, err = e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil),
		HookOwned: true, Merge: MergeMode(99),
	})
	assert.EqualError(t, err, "engine: submit spec: unknown merge mode 99")
}
