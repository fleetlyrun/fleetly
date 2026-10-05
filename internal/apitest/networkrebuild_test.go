package apitest_test

// 网络重建动词全链 e2e（ADR-0046，N2 评审批 P1-4 根修）：成功序（detach→
// rm→create(attachable)→attach 顺序与计数）、外来附着拒绝（E_CONFLICT 带
// 列表）、404 双面、幂等重放与快速路径、冻结窗拒绝、行级授权（他 Team
// 403）、事件与审计三链。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// wantRebuildCode 断言 gRPC 状态码与文案锚。
func wantRebuildCode(t *testing.T, err error, code codes.Code, msgPart string) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, code, st.Code())
	if msgPart != "" {
		assert.Contains(t, st.Message(), msgPart)
	}
}

// rebuildEventPayload 解码 network.rebuilt 的载荷。
func rebuildEventPayload(t *testing.T, ctx context.Context, h *apitest.Harness, networkID string) map[string]any {
	t.Helper()
	stream, err := telemetryv1.NewEventsServiceClient(h.Conn).StreamEvents(ctx, &telemetryv1.StreamEventsRequest{Follow: false})
	require.NoError(t, err)
	for {
		frame, err := stream.Recv()
		if err != nil {
			break
		}
		ev := frame.GetEvent()
		if ev.GetName() == "network.rebuilt" && ev.GetAggregateId() == networkID {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(ev.GetPayload()), &payload))
			return payload
		}
	}
	t.Fatalf("network.rebuilt event for %s not found in the stream", networkID)
	return nil
}

func TestNetworkRebuildLifecycle(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	databases := structurev1.NewDatabasesServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "torchwood"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()
	app, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projectID, Name: "server"})
	require.NoError(t, err)
	dbRow, err := databases.CreateDatabase(owner, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "pg", Engine: "postgres",
	})
	require.NoError(t, err)

	// legacy 形态：非 attachable 载体网 + 两枚附着（App 轴 / Database 轴——
	// 归属裁决的用户域通过面；受管域/Task 轴面由 engine 单测覆盖——apitest
	// 夹具的假 Registry 无 Managed 子面，受管 reconciler 不在册）。标记值按
	// 真形态小写化（载体标记是 sanitizeNamePart 产物，engine 比对大小写折叠）。
	ns := rebuildNS(t, h, projectID)
	h.Runtime.SeedNetworkCarrier(ns, "default", false,
		att("fleetly-app-server", capabilityNS(ns, app.GetApp().GetId(), "", "")),
		att("fleetly-managed-db", capabilityNS(ns, "", "", dbRow.GetDatabase().GetId())),
	)

	resp, err := networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.NoError(t, err)
	assert.Equal(t, "default", resp.GetNetwork().GetName())
	assert.Equal(t, int32(2), resp.GetDetached())
	assert.Equal(t, int32(2), resp.GetReattached())

	// 执行序断言：inspect → detach×2（载体名排序）→ remove → ensure →
	// inspect（终态核验）→ attach×2。
	key := carrierKey(ns, "default")
	assert.Equal(t, []string{
		"inspect " + key,
		"detach " + key + " fleetly-app-server",
		"detach " + key + " fleetly-managed-db",
		"remove " + key,
		"ensure " + key,
		"inspect " + key,
		"attach " + key + " fleetly-app-server",
		"attach " + key + " fleetly-managed-db",
	}, h.Runtime.MaintenanceOps())

	// 终态：attachable + 附件全部还原。
	carrier, ok := h.Runtime.NetworkCarrierState(ns, "default")
	require.True(t, ok)
	assert.True(t, carrier.Attachable)
	assert.Len(t, carrier.Attachments, 2)

	// 事件载荷（network.rebuilt，计数在载荷）。
	list, err := networks.ListNetworks(owner, &structurev1.ListNetworksRequest{ProjectId: projectID})
	require.NoError(t, err)
	networkID := list.GetNetworks()[0].GetId()
	payload := rebuildEventPayload(t, owner, h, networkID)
	assert.Equal(t, projectID, payload["project_id"])
	assert.Equal(t, "default", payload["network"])
	assert.Equal(t, float64(2), payload["detached"])
	assert.Equal(t, float64(2), payload["reattached"])

	// 审计行（source=api：gRPC 直连面）。
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)
	logs, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Resource: "network/default"})
	require.NoError(t, err)
	found := false
	for _, entry := range logs.GetEntries() {
		if entry.GetAction() == "network.rebuild" {
			found = true
			assert.Equal(t, "api", entry.GetSource())
		}
	}
	assert.True(t, found, "network.rebuild audit row missing")
}

func TestNetworkRebuildForeignAttachmentRefused(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "n0reg"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	// 外来附着（无平台标签）+ 失锚附着（App 轴指向不存在的行）。
	ns := rebuildNS(t, h, projectID)
	ghost := ulid.Make().String()
	h.Runtime.SeedNetworkCarrier(ns, "default", false,
		att("foreign-service", capability.NamespaceRef{}),
		att("fleetly-app-ghost", capabilityNS(ns, ghost, "", "")),
	)

	_, err = networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	// apperr 信封：errcode 在 details 文案里。
	assert.True(t, strings.Contains(st.Message(), "E_CONFLICT") ||
		strings.Contains(st.Message(), "foreign-service"), "conflict envelope should list foreign carriers: %s", st.Message())
	assert.Contains(t, st.Message(), "foreign-service")
	assert.Contains(t, st.Message(), "fleetly-app-ghost")

	// 零动作：只有 inspect 流水，网络形态原样（不碰不认识的载体）。
	assert.Equal(t, []string{"inspect " + carrierKey(ns, "default")}, h.Runtime.MaintenanceOps())
	carrier, ok := h.Runtime.NetworkCarrierState(ns, "default")
	require.True(t, ok)
	assert.False(t, carrier.Attachable)
	assert.Len(t, carrier.Attachments, 2)
}

func TestNetworkRebuildNotFound(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	networks := structurev1.NewNetworksServiceClient(h.Conn)

	_, err := networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{
		ProjectId: ulid.Make().String(), Name: "default",
	})
	wantRebuildCode(t, err, codes.NotFound, "not found")

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "n0probe"})
	require.NoError(t, err)
	_, err = networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{
		ProjectId: proj.GetProject().GetId(), Name: "does-not-exist",
	})
	wantRebuildCode(t, err, codes.NotFound, "not found")
}

func TestNetworkRebuildIdempotencyAndFastPath(t *testing.T) {
	h := apitest.New(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "messaging"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	// 幂等重放：同键同体重放拿回同一响应（执行只发生一次）。
	kctx := sdk.WithIdempotencyKey(owner, "rebuild-default-1")
	first, err := networks.RebuildNetwork(kctx, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.NoError(t, err)
	replay, err := networks.RebuildNetwork(kctx, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.NoError(t, err)
	assert.Equal(t, first.GetDetached(), replay.GetDetached())
	assert.Equal(t, first.GetNetwork().GetId(), replay.GetNetwork().GetId())
	ensureCalls := 0
	for _, op := range h.Runtime.MaintenanceOps() {
		if op == "ensure "+carrierKey(rebuildNS(t, h, projectID), "default") {
			ensureCalls++
		}
	}
	assert.Equal(t, 1, ensureCalls, "replayed call must not re-execute")

	// 无键重跑 = 快速路径（已 attachable 零扰动）。
	again, err := networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.NoError(t, err)
	assert.Equal(t, int32(0), again.GetDetached())
	assert.Equal(t, int32(0), again.GetReattached())
}

func TestNetworkRebuildFrozenAndAuthz(t *testing.T) {
	h := apitest.New(t)
	anon := context.Background()
	owner := sdk.WithToken(anon, h.Token)

	teams := identityv1.NewTeamsServiceClient(h.Conn)
	teamB, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "acme"})
	require.NoError(t, err)
	bAdmin := sdk.WithToken(anon, mintTeamToken(t, h, "acme-admin", teamB.GetTeam().GetId(), identity.RoleAdminID))

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	gov := systemv1.NewGovernanceServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "frozen-net"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	// 行级授权（ADR-0035）：他 Team admin 写面 403。
	_, err = networks.RebuildNetwork(bAdmin, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	wantRebuildCode(t, err, codes.PermissionDenied, "")

	// 冻结窗拒绝（变更型维护动词，ADR-0017）：带 reason 信封；lift 后放行。
	_, err = gov.SetChangeFreeze(owner, &systemv1.SetChangeFreezeRequest{TeamId: identity.DefaultTeamID, Reason: "year-end freeze"})
	require.NoError(t, err)
	_, err = networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "year-end freeze")
	freezes, err := gov.ListChangeFreezes(owner, &systemv1.ListChangeFreezesRequest{})
	require.NoError(t, err)
	for _, f := range freezes.GetFreezes() {
		if f.GetTeamId() == identity.DefaultTeamID {
			_, err = gov.LiftChangeFreeze(owner, &systemv1.LiftChangeFreezeRequest{Id: f.GetId()})
			require.NoError(t, err)
		}
	}
	_, err = networks.RebuildNetwork(owner, &structurev1.RebuildNetworkRequest{ProjectId: projectID, Name: "default"})
	require.NoError(t, err)
}

// ---- 夹具 helpers ----

// rebuildNS 取项目行团队构造隔离域（engine 侧 ns 同公式）。
func rebuildNS(t *testing.T, h *apitest.Harness, projectID string) capability.NamespaceRef {
	t.Helper()
	p, err := project.New(h.DB.Clock()).Get(context.Background(), h.DB.Runner(), projectID)
	require.NoError(t, err)
	return capability.NamespaceRef{Team: p.TeamID, Project: projectID}
}

// carrierKey 与 FakeRuntime 的假载体网命名同公式（断言面单源）。
func carrierKey(ns capability.NamespaceRef, network string) string {
	return ns.Team + "/" + ns.Project + "/" + network
}

// att 构造一枚附着投影（标记值按真形态小写化）。
func att(carrier string, domain capability.NamespaceRef) capability.NetworkAttachment {
	return capability.NetworkAttachment{Carrier: carrier, Domain: lowerNS(domain)}
}

// capabilityNS 填四轴（App/Task/Database 互斥由调用方保证）。
func capabilityNS(base capability.NamespaceRef, app, task, database string) capability.NamespaceRef {
	base.App, base.Task, base.Database = app, task, database
	return lowerNS(base)
}

// lowerNS 把域轴值小写化（模拟载体标记的 sanitizeNamePart 形态）。
func lowerNS(ns capability.NamespaceRef) capability.NamespaceRef {
	return capability.NamespaceRef{
		Team:     strings.ToLower(ns.Team),
		Project:  strings.ToLower(ns.Project),
		App:      strings.ToLower(ns.App),
		Task:     strings.ToLower(ns.Task),
		Database: strings.ToLower(ns.Database),
	}
}
