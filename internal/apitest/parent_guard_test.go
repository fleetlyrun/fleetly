package apitest_test

// 父资源存活守卫回归（批 0 复核）：apps/routes/volumes/networks/secrets/
// configs 均无 FK，"创建时引用父资源"的 RPC 若不校验，对不存在/已删的
// 父资源直接成功——活 App 落已删项目、活路由指向 tombstone App。修复
// 后全部创建面在事务内校验（requireActiveProject / requireProjectApp，
// E_NOT_FOUND）。同族面逐 RPC 排查结论（已校验的既有证据）：
//
//	Deploy→App、SetGitHook→App、CreateInvitation→Team+Role、
//	CreateRole→Team、CreateToken→Team+Role(+User)：事务内既有校验；
//	AcceptInvitation 的 user/membership 建在已验证的邀请锚上；
//	CreateUser/CreateTeam/CreateProject：根资源，无父引用
//	（CreateProject.team_id 是 P1-10 灰色面，审计 §8.B 另账，不在此修）。

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// parentFixtures 造三类父资源状态：不存在（随机 id）、已删（tombstone 在）、
// 存活（对照组）。
type parentFixtures struct {
	missingID  string // 从未存在的 project id
	deletedID  string // 已删 project id
	liveID     string // 存活 project id
	deletedApp string // 已删 App id（挂在 liveID 下）
	liveApp    string // 存活 App id（挂在 liveID 下）
}

func setupParentFixtures(t *testing.T, h *apitest.Harness, ctx context.Context, tag string) *parentFixtures {
	t.Helper()
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	fx := &parentFixtures{missingID: ulid.Make().String()}

	live, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: tag + "-live"})
	require.NoError(t, err)
	fx.liveID = live.GetProject().GetId()
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: fx.liveID, Name: "web"})
	require.NoError(t, err)
	fx.liveApp = app.GetApp().GetId()

	gone, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: tag + "-gone"})
	require.NoError(t, err)
	fx.deletedID = gone.GetProject().GetId()
	goneApp, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: fx.deletedID, Name: "web"})
	require.NoError(t, err)
	require.NoError(t, func() error {
		_, err := apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: goneApp.GetApp().GetId()})
		return err
	}())
	require.NoError(t, func() error {
		_, err := projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: fx.deletedID})
		return err
	}())

	// 已删 App 形态（挂在存活项目下）：建→删，行成 tombstone。
	appGone, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: fx.liveID, Name: "gone"})
	require.NoError(t, err)
	fx.deletedApp = appGone.GetApp().GetId()
	require.NoError(t, func() error {
		_, err := apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: fx.deletedApp})
		return err
	}())
	return fx
}

// TestCreateRequiresLiveParent：六类"创建时引用父资源"的写面逐面断言
// "父资源不存在→拒绝"与"父资源已删→拒绝"（E_NOT_FOUND）；Route 额外
// 钉"父 App 已删"与"跨项目 App"两形态。对照面：存活父资源可建。
func TestCreateRequiresLiveParent(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	fx := setupParentFixtures(t, h, ctx, "guard")

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	routes := edgev1.NewRoutesServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	configs := structurev1.NewConfigsServiceClient(h.Conn)

	// createAgainst 按 kind 构造一次"引用 projectID/appID"的创建调用。
	type kindCase struct {
		name   string
		create func(projectID, appID string) error
	}
	kinds := []kindCase{
		{"app", func(projectID, _ string) error {
			_, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: ulid.Make().String()})
			return err
		}},
		{"volume", func(projectID, _ string) error {
			_, err := volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: projectID, Name: ulid.Make().String()})
			return err
		}},
		{"network", func(projectID, _ string) error {
			_, err := networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{ProjectId: projectID, Name: ulid.Make().String()})
			return err
		}},
		{"secret", func(projectID, _ string) error {
			_, err := secrets.PutSecret(ctx, &structurev1.PutSecretRequest{ProjectId: projectID, Name: ulid.Make().String(), Value: "v"})
			return err
		}},
		{"config", func(projectID, _ string) error {
			_, err := configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: projectID, Name: ulid.Make().String(), Content: "k=v"})
			return err
		}},
		{"route", func(projectID, appID string) error {
			_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
				ProjectId: projectID, Host: fmt.Sprintf("%s.guard.test", ulid.Make().String()),
				AppId: appID, Process: "web", Port: 8000,
			})
			return err
		}},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			// 父资源不存在 → 拒绝（E_NOT_FOUND；gRPC NotFound 同步钉一次）。
			err := k.create(fx.missingID, fx.liveApp)
			require.Error(t, err, "create against a nonexistent project must be rejected")
			require.Equal(t, "E_NOT_FOUND", appErrCode(t, err))
			require.Equal(t, codes.NotFound, status.Code(err))

			// 父资源已删（tombstone 在）→ 拒绝：不是"行不在"的 404，而是
			// 明确读到了已删行并按活跃行口径拒绝。
			err = k.create(fx.deletedID, fx.liveApp)
			require.Error(t, err, "create against a deleted project must be rejected")
			require.Equal(t, "E_NOT_FOUND", appErrCode(t, err))
		})
	}

	// Route 的父 App 形态：已删 App 与跨项目 App 均拒绝。
	t.Run("route app deleted", func(t *testing.T) {
		_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
			ProjectId: fx.liveID, Host: "gone-app.guard.test",
			AppId: fx.deletedApp, Process: "web", Port: 8000,
		})
		require.Error(t, err, "route to a tombstoned app must be rejected")
		require.Equal(t, "E_NOT_FOUND", appErrCode(t, err))
	})
	t.Run("route app cross-project", func(t *testing.T) {
		other, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "guard-other"})
		require.NoError(t, err)
		_, err = routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
			ProjectId: other.GetProject().GetId(), Host: "cross.guard.test",
			AppId: fx.liveApp, Process: "web", Port: 8000,
		})
		require.Error(t, err, "route claiming an app of another project must be rejected")
		require.Equal(t, "E_NOT_FOUND", appErrCode(t, err))
	})

	// 对照组：全部写面对存活父资源可建（守卫不误伤）。
	require.NoError(t, func() error {
		_, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: fx.liveID, Name: "control"})
		return err
	}())
	require.NoError(t, func() error {
		_, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
			ProjectId: fx.liveID, Host: "control.guard.test",
			AppId: fx.liveApp, Process: "web", Port: 8000,
		})
		return err
	}())
	require.NoError(t, func() error {
		_, err := volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: fx.liveID, Name: "control"})
		return err
	}())
}

// TestCreateAfterProjectDeleteRejected：create-after-delete 顺序形态——
// 删项目后向其建 App 拒绝（E_NOT_FOUND），且 GetProject 的"已删行可读"
// 设计（tombstone 是事实不是秘密）不构成创建通路。
func TestCreateAfterProjectDeleteRejected(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "create-after-del"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()
	require.NoError(t, func() error {
		_, err := projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
		return err
	}())

	// 已删行仍可直读（既有读面设计），但创建面必须拒绝。
	_, err = projects.GetProject(ctx, &structurev1.GetProjectRequest{Id: projectID})
	require.NoError(t, err, "tombstoned rows stay readable by design (Get is not the active-row face)")
	_, err = apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: "late"})
	require.Error(t, err)
	require.Equal(t, "E_NOT_FOUND", appErrCode(t, err), "deleted project must not accept new apps")
}

// TestDeleteProjectCreateAppRaceInvariant（仿 TestDeleteDeployRaceInvariant
// 形态）：删项目与建 App 的对偶守卫竞态口径——任一交错下不变式成立：
// 项目存活 ⇔ App 可建（删除成功 ⇒ 本测试全程无 App 建成；有 App 建成 ⇒
// 删除必被活跃 App 守卫拒绝）。失败只允许干净分类，不出 E_INTERNAL。
func TestDeleteProjectCreateAppRaceInvariant(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "proj-race"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	const rounds = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var createErrs, deleteErrs []error
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			<-start
			// 每轮唯一名：排除同名冲突噪声，竞态面只剩父资源守卫。
			_, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{
				ProjectId: projectID, Name: fmt.Sprintf("web-%d", i),
			})
			mu.Lock()
			createErrs = append(createErrs, err)
			mu.Unlock()
		}(i)
		go func() {
			defer wg.Done()
			<-start
			_, err := projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
			mu.Lock()
			deleteErrs = append(deleteErrs, err)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range createErrs {
		if err == nil {
			continue
		}
		require.NotContains(t, err.Error(), "E_INTERNAL", "create must fail cleanly, never internal")
		require.Contains(t, []string{"E_NOT_FOUND"}, appErrCode(t, err),
			"create against a lost race must fail as not-found (parent guard)")
	}
	for _, err := range deleteErrs {
		if err == nil {
			continue
		}
		require.NotContains(t, err.Error(), "E_INTERNAL", "delete must fail cleanly, never internal")
		// 合法失败形态：E_CONFLICT（活跃 App 守卫）。SoftDelete 对已删行
		// 幂等，故不出现 E_NOT_FOUND。
		require.Equal(t, "E_CONFLICT", appErrCode(t, err),
			"delete must only fail on live apps held by the project")
	}

	// 终局不变式：项目已删 ⇔ 全程无 App 建成（App 无删除路径参与本测试，
	// 一旦建成即活跃，活跃 App 必挡住删除）。
	_, getErr := projects.GetProject(ctx, &structurev1.GetProjectRequest{Id: projectID})
	if getErr != nil {
		require.Contains(t, getErr.Error(), "E_NOT_FOUND")
		for i, err := range createErrs {
			require.Error(t, err, "project deleted ⇒ no create may have succeeded (round %d)", i)
		}
		return
	}
	list, err := apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: projectID})
	require.NoError(t, err)
	created := 0
	for _, err := range createErrs {
		if err == nil {
			created++
		}
	}
	require.Len(t, list.GetApps(), created,
		"project alive ⇒ every successful create is listed (and the delete was rejected)")
}
