package apitest_test

import (
	"context"
	"database/sql"
	"regexp"
	"sync"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	edgev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/edge/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// appErrCode 从错误串提取应用码（E_XXX；gRPC 信封内嵌形态）。
func appErrCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	m := regexp.MustCompile(`(E_[A-Z_]+)`).FindStringSubmatch(err.Error())
	require.NotEmpty(t, m, "error must carry an app code: %v", err)
	return m[1]
}

// routeListed 报告 routeID 是否在 ListRoutes 结果可见（List 是活跃行
// 口径——可见即未软删）。
func routeListed(t *testing.T, list *edgev1.ListRoutesResponse, routeID string) bool {
	t.Helper()
	for _, r := range list.GetRoutes() {
		if r.GetId() == routeID {
			return true
		}
	}
	return false
}

// ADR-0023 回归（N0 修复批 C2）：DeleteApp 收口语义——活跃部署拒绝
// （E_CONFLICT）、终态后删除成功、引用路由随删、tombstone 生效（同项目
// 同名可重建、旧 ID 不复用）。
func TestDeleteAppSemantics(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	routes := edgev1.NewRoutesServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "teardown"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	// 引用该 App 的路由（撤路由面的前置）。
	route, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: proj.GetProject().GetId(), Host: "gone.teardown.test",
		AppId: appID, Process: "web", Port: 8000,
	})
	require.NoError(t, err)

	// 活跃部署在 → E_CONFLICT（先 cancel/等终态的提示在 suggestion）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_CONFLICT")
	require.Contains(t, err.Error(), "active deployment")

	// 拒删零副作用（ADR-0023 修订）：E_CONFLICT 路径不拆载体、不碰路由
	// （不变式"App 存活 ⇒ 路由不得消失"的常规面）。
	require.Empty(t, h.Runtime.Removed(), "rejected delete must not tear down carriers")
	preRouteList, err := routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	require.True(t, routeListed(t, preRouteList, route.GetRoute().GetId()),
		"rejected delete must not touch routes")

	// 取消到终态 → 删除成功。
	list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	_, err = deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: list.GetDeployments()[0].GetId()})
	require.NoError(t, err)

	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.NoError(t, err)

	// Runtime.Remove 已被触发（收口）。
	require.NotEmpty(t, h.Runtime.Removed(), "delete must tear down runtime carriers")

	// 引用路由随删：列表不再可见。
	routeList, err := routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	for _, r := range routeList.GetRoutes() {
		require.NotEqual(t, route.GetRoute().GetId(), r.GetId(), "routes referencing the app must be withdrawn on delete")
	}

	// tombstone：列表无此 App；同项目同名可重建（新 ULID，ID 永不复用）。
	listed, err := apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	for _, a := range listed.GetApps() {
		require.NotEqual(t, appID, a.GetId(), "deleted app must not be listed")
	}
	// 读面统一口径（N0.1 P1-1）：GetApp 已删 → 404（与 List/Delete 同形）。
	_, err = apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
	// 删后 deploy 拒绝：不重建载体。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")

	again, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	require.NotEqual(t, appID, again.GetApp().GetId(), "a new app gets a new id (ids are never reused)")

	// 旧 ID 再删 → E_NOT_FOUND。
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
}

// Q-15 回归：DeleteProject 活跃 App 守卫——有未 tombstone 的 App 即拒删
// （E_CONFLICT，提示先删 App）；删净 App 后可删（tombstone 生效，再删 404）。
// Project 级材料不随删（各自生命周期，守卫不级联——代码面注释即边界）。
func TestDeleteProjectSemantics(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "guarded"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projectID, Name: "web"})
	require.NoError(t, err)

	// 有活 App → E_CONFLICT（先删 App 的处置提示在文案）。
	_, err = projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_CONFLICT")
	require.Contains(t, err.Error(), "delete them before deleting the project")

	// 拒删零副作用：Project 存活。
	_, err = projects.GetProject(ctx, &structurev1.GetProjectRequest{Id: projectID})
	require.NoError(t, err)

	// 删净 App 后可删 Project。Project 读面语义（Get 含已删行、再删幂等）
	// 是既有设计（tombstone 是事实不是秘密）——活跃面以 List 断言。
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: app.GetApp().GetId()})
	require.NoError(t, err)
	_, err = projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.NoError(t, err)
	list, err := projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)
	for _, p := range list.GetProjects() {
		require.NotEqual(t, projectID, p.GetId(), "deleted project must not be listed")
	}
}

// Q-12 回归：state.ErrConflict 的通用映射是 E_CONFLICT 中性文案（唯一约束
// 命中形态：同名 Project 再建），不再一律 "already exists"。
func TestErrConflictMapsToConflictCode(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)

	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "dupe"})
	require.NoError(t, err)
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "dupe"})
	require.Error(t, err)
	require.Equal(t, "E_CONFLICT", appErrCode(t, err), "unique-violation conflicts must surface E_CONFLICT")
	require.Contains(t, err.Error(), "conflict:")
	require.Contains(t, err.Error(), "refresh and retry")
}

// 并发删+部署竞态回归（N0.1 P1-1）：预检通过到 tombstone 落账之间受理
// 部署的 TOCTOU 已由最终事务内 ActiveByApp 复查收口（与 Submit 的存活
// 判定互为对偶；单连接事务串行）。任一交错下不变式成立：tombstone 与
// 活跃部署不共存，且不出 E_INTERNAL。
func TestDeleteDeployRaceInvariant(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "race"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	const rounds = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var deployErrs, deleteErrs []error
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
			mu.Lock()
			deployErrs = append(deployErrs, err)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
			mu.Lock()
			deleteErrs = append(deleteErrs, err)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range deployErrs {
		if err == nil {
			continue
		}
		require.NotContains(t, err.Error(), "E_INTERNAL", "deploy must fail cleanly (not-found/queue-full), never internal")
		require.Contains(t, []string{"E_NOT_FOUND", "E_QUEUE_FULL"}, appErrCode(t, err),
			"deploy failures must be clean and classified")
	}
	for _, err := range deleteErrs {
		if err == nil {
			continue
		}
		// 合法失败形态：E_CONFLICT（活跃部署在）/ E_NOT_FOUND（并发双删，
		// 对手先落 tombstone——TeardownApp 的活跃行 Get 撞已删）。
		require.Contains(t, []string{"E_CONFLICT", "E_NOT_FOUND"}, appErrCode(t, err),
			"delete must only fail on active deployments or a concurrent winner")
	}

	// 不变式：删成（GetApp 404）⇒ 该 App 不可能再有活跃部署。
	_, getErr := apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	if getErr == nil {
		return // 删除被拒（活跃部署在）：App 保持可操作，不变式无涉。
	}
	require.Contains(t, getErr.Error(), "E_NOT_FOUND")
	list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	terminal := map[string]bool{
		"succeeded": true, "failed": true, "cancelled": true, "superseded": true, "rolled-back": true,
	}
	for _, d := range list.GetDeployments() {
		require.True(t, terminal[d.GetState()],
			"tombstoned app must hold no active deployment (state=%s)", d.GetState())
	}
}

// teardown-aborted 残余面的确定性回归（ADR-0023 修订）：收口停在 Remove
// 时落一条 queued 部署行——等价于"收口后、落账前受理"的竞态结局（Submit
// 与 teardown 共享 appMu，此处以注入替代调度骰子）。落账复查必须整单回滚：
// App 存活、路由未动、载体已拆的事实以 teardown-aborted 事件 + 审计留痕
// （不静默），在途部署重放自愈。缺陷形态（复审实证）：路由先于复查软删，
// 同交错下 App 存活而路由已撤——活 App 零流量、无事件。
func TestDeleteAppTeardownAbortKeepsRoute(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	routes := edgev1.NewRoutesServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "abort"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()
	route, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: proj.GetProject().GetId(), Host: "abort.teardown.test",
		AppId: appID, Process: "web", Port: 8000,
	})
	require.NoError(t, err)
	routeID := route.GetRoute().GetId()

	// 冻结一个真 Revision（deploy 后即取消；行留终态）——planted 行指向
	// 它可在引擎拾取后停在被 L1 门钉住的 releasing 态（假时钟不走，
	// ReleaseTimeout 永不到期），避免 planted 行在复查前被驱动成终态的
	// 残余竞态。
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	revID := dep.GetDeployment().GetToRevision()
	require.NotEmpty(t, revID)
	_, err = deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: dep.GetDeployment().GetId()})
	require.NoError(t, err)

	// 收口停在 Remove（锁内预检已通过、appMu 仍被持有）；窗口内落 queued
	// 行（行形态与 admission 产物一致）。
	entered, release := h.Runtime.ArmRemoveBlock()
	errCh := make(chan error, 1)
	go func() {
		_, err := apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
		errCh <- err
	}()
	<-entered
	planted := &deployment.Deployment{
		ID: ulid.Make().String(), AppID: appID,
		ToRevision: revID, State: deployment.StateQueued, Generation: 2,
	}
	require.NoError(t, h.DB.Tx(ctx, func(tx *sql.Tx) error {
		return deployment.New(h.Clock).Create(ctx, tx, planted)
	}))
	release()

	// 落账复查命中活跃部署 → 整单回滚 → E_CONFLICT；App 保持可操作。
	err = <-errCh
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_CONFLICT")
	require.Contains(t, err.Error(), "active deployment")

	// 不变式：App 存活 ⇒ 路由仍在（拒绝路径不碰路由；可见即未软删）。
	_, err = apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	require.NoError(t, err)
	routeList, err := routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	require.True(t, routeListed(t, routeList, routeID),
		"app alive ⇒ routes referencing it must survive an aborted delete")

	// 收口确实拆了载体（残余面成立的前提）；tombstone 未落（app.deleted
	// 不在），已拆事实以 teardown-aborted 事件 + 审计留痕。
	require.Len(t, h.Runtime.Removed(), 1, "teardown must have removed carriers before the abort")
	evs, err := events.ListEvents(ctx, &telemetryv1.ListEventsRequest{Limit: 200})
	require.NoError(t, err)
	var abortedPayload string
	deletedSeen := false
	for _, ev := range evs.GetEvents() {
		switch ev.GetName() {
		case "app.teardown_aborted":
			abortedPayload = ev.GetPayload()
		case "app.deleted":
			deletedSeen = true
		}
	}
	require.NotEmpty(t, abortedPayload, "aborted teardown must be recorded as an event (never silent)")
	require.Contains(t, abortedPayload, `"active_deployments":1`, "event must carry the diagnostic count")
	require.False(t, deletedSeen, "aborted delete must not tombstone the app")
	entries, err := auditq.ListAudit(ctx, &identityv1.ListAuditRequest{Action: "app.", Limit: 100})
	require.NoError(t, err)
	actions := map[string]bool{}
	for _, e := range entries.GetEntries() {
		actions[e.GetAction()] = true
	}
	require.True(t, actions["app.teardown_abort"], "aborted teardown must be audited")
	require.False(t, actions["app.delete"], "aborted delete must not audit a deletion")
}

// 删+部署并发风暴下"App 存活 ⇒ 路由不得消失"（ADR-0023 修订不变式的
// 路由面扩展——TestDeleteDeployRaceInvariant 不建路由，覆盖不到"拒绝
// 路径已撤路由"的静默丢流量缺陷）。
func TestDeleteAppRoutesRaceInvariant(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	routes := edgev1.NewRoutesServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "route-race"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()
	route, err := routes.CreateRoute(ctx, &edgev1.CreateRouteRequest{
		ProjectId: proj.GetProject().GetId(), Host: "race.teardown.test",
		AppId: appID, Process: "web", Port: 8000,
	})
	require.NoError(t, err)

	const rounds = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var deployErrs, deleteErrs []error
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
			mu.Lock()
			deployErrs = append(deployErrs, err)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
			mu.Lock()
			deleteErrs = append(deleteErrs, err)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range deployErrs {
		if err == nil {
			continue
		}
		require.NotContains(t, err.Error(), "E_INTERNAL", "deploy must fail cleanly (not-found/queue-full), never internal")
		require.Contains(t, []string{"E_NOT_FOUND", "E_QUEUE_FULL"}, appErrCode(t, err),
			"deploy failures must be clean and classified")
	}
	for _, err := range deleteErrs {
		if err == nil {
			continue
		}
		// 合法失败形态：E_CONFLICT（活跃部署在，含 teardown-aborted 残余
		// 面）/ E_NOT_FOUND（并发双删，对手先落 tombstone）。
		require.Contains(t, []string{"E_CONFLICT", "E_NOT_FOUND"}, appErrCode(t, err),
			"delete must only fail on active deployments or a concurrent winner")
	}

	// 终局不变式：App 存活 ⇔ 路由可见（ListRoutes 活跃行口径——可见即
	// 未软删）。App 被删则引用路由必同逝；任一交错下"活 App 零路由"
	// （缺陷形态）都不可出现。
	_, getErr := apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	routeList, err := routes.ListRoutes(ctx, &edgev1.ListRoutesRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	listed := routeListed(t, routeList, route.GetRoute().GetId())
	if getErr == nil {
		require.True(t, listed, "app alive ⇒ routes referencing it must never be withdrawn by rejected deletes")
		return
	}
	require.Contains(t, getErr.Error(), "E_NOT_FOUND")
	require.False(t, listed, "deleted app ⇒ referencing routes withdrawn atomically with the tombstone")
}
