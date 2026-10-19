package apitest_test

// H8 阶段 B 验收（设计文档 docs/design/2026-10-19-security-backlog-design.md
// §5.2/§5.7/§7）：QueryScopedMetrics 的行级授权、服务端固定 scope 构造、
// protovalidate 受理面与响应截断映射。
//
// 夹具经 apitest.NewWithMetrics 注入 fake Metrics（生产装配不变）；断言
// fake 收到的 ScopedQuery 由服务端从已授权行解析（调用方没有 project_id
// 或 PromQL 字段可传）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// scopedFixture 铸一队一 Project 的图表面夹具（owner 建 Team/Project，
// member 会话由调用方按需铸）。
type scopedFixture struct {
	teamID    string
	projectID string
}

func newScopedFixture(t *testing.T, h *apitest.Harness, teamName, projectName string) scopedFixture {
	t.Helper()
	team, err := identityv1.NewTeamsServiceClient(h.Conn).CreateTeam(h.Ctx(),
		&identityv1.CreateTeamRequest{Name: teamName})
	require.NoError(t, err)
	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(h.Ctx(),
		&structurev1.CreateProjectRequest{Name: projectName, TeamId: team.GetTeam().GetId()})
	require.NoError(t, err)
	return scopedFixture{teamID: team.GetTeam().GetId(), projectID: proj.GetProject().GetId()}
}

// scopedMemberSession 造目标 Team 的 builtin-member 会话（member 持
// metrics:read，且只有本队行级授权）。
func scopedMemberSession(t *testing.T, h *apitest.Harness, name, teamID, password string) context.Context {
	t.Helper()
	createUserAs(t, h, name, teamID, identity.RoleMemberID, password)
	ctx, who := loginSession(t, h, name, password)
	require.Equal(t, teamID, who.GetTeamId())
	require.Equal(t, identity.RoleMemberID, who.GetRoleId())
	require.False(t, who.GetPlatformAdmin(), "member is not a platform admin")
	return ctx
}

// TestQueryScopedMetricsOwnTeamApp 钉 member 能查本队 App 图表，且 fake 收到
// 的 ProjectID/AppID/Process 来自行解析与请求白名单（客户端没有 project_id
// 或 PromQL 字段可传）。
func TestQueryScopedMetricsOwnTeamApp(t *testing.T) {
	fake := &fakeMetrics{}
	h := apitest.NewWithMetrics(t, fake)
	fixture := newScopedFixture(t, h, "alpha", "alpha-proj")
	app := h.CreateApp(t, fixture.projectID, "web")
	member := scopedMemberSession(t, h, "alpha-member", fixture.teamID, "alpha-member-pass")
	client := telemetryv1.NewMetricsServiceClient(h.Conn)

	fake.scopedPage = capability.SeriesPage{
		Series: []capability.Series{{
			Metric: map[string]string{capability.MetricLabelApp: app.GetId(), capability.MetricLabelProcess: "web"},
			Points: []capability.SeriesPoint{{Time: time.Unix(1767225600, 0).UTC(), Value: 12.5}},
		}},
		Total: 1,
	}
	resp, err := client.QueryScopedMetrics(member, &telemetryv1.QueryScopedMetricsRequest{
		AppId: app.GetId(), Metric: capability.MetricCPUPercent, Process: "web",
	})
	require.NoError(t, err)
	require.Len(t, resp.GetSeries(), 1)
	assert.InDelta(t, 12.5, resp.GetSeries()[0].GetPoints()[0].GetValue(), 0.001)
	assert.False(t, resp.GetTruncated())
	assert.Equal(t, int32(1), resp.GetSeriesTotal())

	queries := fake.scopedQueries()
	require.Len(t, queries, 1)
	q := queries[0]
	assert.Equal(t, fixture.projectID, q.ProjectID, "project comes from the App row, never the client")
	assert.Equal(t, app.GetId(), q.AppID)
	assert.Empty(t, q.DatabaseID)
	assert.Equal(t, "web", q.Process)
	assert.Equal(t, capability.MetricCPUPercent, q.Metric)
	assert.GreaterOrEqual(t, q.Step, capability.ScopedMetricsMinStep)
	assert.Equal(t, capability.ScopedMetricsMaxSeries, q.MaxSeries)

	// 无 process 的 memory 查询：可选字段留空，scope 仍由行解析。
	fake.scoped = nil
	_, err = client.QueryScopedMetrics(member, &telemetryv1.QueryScopedMetricsRequest{
		AppId: app.GetId(), Metric: capability.MetricMemoryWorkingSet,
	})
	require.NoError(t, err)
	queries = fake.scopedQueries()
	require.Len(t, queries, 1)
	assert.Empty(t, queries[0].Process)
	assert.Equal(t, fixture.projectID, queries[0].ProjectID)
}

// TestQueryScopedMetricsRejectsOtherTeam 钉跨队 member 对他队 App/Database
// 403 且零下游（授权前拒绝），不存在的行保持 404。
func TestQueryScopedMetricsRejectsOtherTeam(t *testing.T) {
	fake := &fakeMetrics{}
	h := apitest.NewWithMetrics(t, fake)
	alpha := newScopedFixture(t, h, "alpha", "alpha-proj")
	beta := newScopedFixture(t, h, "beta", "beta-proj")
	betaApp := h.CreateApp(t, beta.projectID, "web")
	member := scopedMemberSession(t, h, "alpha-member", alpha.teamID, "alpha-member-pass")
	client := telemetryv1.NewMetricsServiceClient(h.Conn)

	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	betaDB, err := dbs.CreateDatabase(h.Ctx(), &structurev1.CreateDatabaseRequest{
		ProjectId: beta.projectID, Name: "data", Engine: "postgres",
	})
	require.NoError(t, err)

	_, err = client.QueryScopedMetrics(member, &telemetryv1.QueryScopedMetricsRequest{
		AppId: betaApp.GetId(), Metric: capability.MetricCPUPercent,
	})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")

	_, err = client.QueryScopedMetrics(member, &telemetryv1.QueryScopedMetricsRequest{
		DatabaseId: betaDB.GetDatabase().GetId(), Metric: capability.MetricMemoryWorkingSet,
	})
	wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
	assert.Empty(t, fake.scopedQueries(), "authorization precedes any Metrics query")

	// 行不存在 → 404（与 authorizeAppID/authorizeProjectID 同源）。
	const missing = "01M01M01M01M01M01M01M01M01M"
	_, err = client.QueryScopedMetrics(member, &telemetryv1.QueryScopedMetricsRequest{
		AppId: missing, Metric: capability.MetricCPUPercent,
	})
	wantCode(t, err, codes.NotFound, "E_NOT_FOUND")
	_, err = client.QueryScopedMetrics(member, &telemetryv1.QueryScopedMetricsRequest{
		DatabaseId: missing, Metric: capability.MetricMemoryWorkingSet,
	})
	wantCode(t, err, codes.NotFound, "E_NOT_FOUND")
	assert.Empty(t, fake.scopedQueries(), "not-found rows never reach Metrics")
}

// TestQueryScopedMetricsValidation 钉受理面校验：指标白名单、scope 互斥、
// database+cpu_percent/process、窗口 >24h 全部 400，且 fake 零调用。
func TestQueryScopedMetricsValidation(t *testing.T) {
	fake := &fakeMetrics{}
	h := apitest.NewWithMetrics(t, fake)
	fixture := newScopedFixture(t, h, "alpha", "alpha-proj")
	app := h.CreateApp(t, fixture.projectID, "web")
	member := scopedMemberSession(t, h, "alpha-member", fixture.teamID, "alpha-member-pass")
	client := telemetryv1.NewMetricsServiceClient(h.Conn)
	db, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(h.Ctx(),
		&structurev1.CreateDatabaseRequest{ProjectId: fixture.projectID, Name: "data", Engine: "postgres"})
	require.NoError(t, err)

	tests := []struct {
		name    string
		req     *telemetryv1.QueryScopedMetricsRequest
		message string
	}{
		{
			name:    "empty metric",
			req:     &telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId()},
			message: "cpu_percent, cpu_cores, memory_working_set_bytes",
		},
		{
			name:    "unknown metric",
			req:     &telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: "disk_full"},
			message: "cpu_percent, cpu_cores, memory_working_set_bytes",
		},
		{
			name:    "both scopes",
			req:     &telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), DatabaseId: db.GetDatabase().GetId(), Metric: capability.MetricCPUCores},
			message: "exactly one of app_id or database_id",
		},
		{
			name:    "no scope",
			req:     &telemetryv1.QueryScopedMetricsRequest{Metric: capability.MetricCPUCores},
			message: "exactly one of app_id or database_id",
		},
		{
			name:    "database cpu_percent",
			req:     &telemetryv1.QueryScopedMetricsRequest{DatabaseId: db.GetDatabase().GetId(), Metric: capability.MetricCPUPercent},
			message: "cpu_percent is only available for app scope",
		},
		{
			name:    "database process",
			req:     &telemetryv1.QueryScopedMetricsRequest{DatabaseId: db.GetDatabase().GetId(), Metric: capability.MetricMemoryWorkingSet, Process: "web"},
			message: "process: only available for app scope",
		},
		{
			name: "window over 24h",
			req: &telemetryv1.QueryScopedMetricsRequest{
				AppId: app.GetId(), Metric: capability.MetricMemoryWorkingSet,
				Start: time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339),
				End:   time.Now().UTC().Format(time.RFC3339),
			},
			message: "must not exceed 24h",
		},
		{
			name:    "bad process shape",
			req:     &telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: capability.MetricMemoryWorkingSet, Process: "Web"},
			message: "process: must be a dns label",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.QueryScopedMetrics(member, tt.req)
			wantCode(t, err, codes.InvalidArgument, "E_INVALID_ARGUMENT")
			assert.Contains(t, err.Error(), tt.message)
			assert.Empty(t, fake.scopedQueries(), "rejected requests must not reach Metrics")
		})
	}
}

// TestQueryScopedMetricsTruncationMapping 钉 truncated/series_total 的响应
// 映射（scoped 超限不报错，返回前 N 条 + 实际总数）。
func TestQueryScopedMetricsTruncationMapping(t *testing.T) {
	fake := &fakeMetrics{}
	h := apitest.NewWithMetrics(t, fake)
	fixture := newScopedFixture(t, h, "alpha", "alpha-proj")
	app := h.CreateApp(t, fixture.projectID, "web")
	member := scopedMemberSession(t, h, "alpha-member", fixture.teamID, "alpha-member-pass")

	series := make([]capability.Series, capability.ScopedMetricsMaxSeries)
	for i := range series {
		series[i] = capability.Series{Metric: map[string]string{capability.MetricLabelApp: app.GetId()}}
	}
	fake.scopedPage = capability.SeriesPage{Series: series, Truncated: true, Total: 60}

	resp, err := telemetryv1.NewMetricsServiceClient(h.Conn).QueryScopedMetrics(member,
		&telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: capability.MetricCPUCores})
	require.NoError(t, err)
	assert.Len(t, resp.GetSeries(), capability.ScopedMetricsMaxSeries)
	assert.True(t, resp.GetTruncated())
	assert.Equal(t, int32(60), resp.GetSeriesTotal())
}

// TestQueryScopedMetricsPlatformAdminCrossTeam 钉平台管理员走同一 handler：
// teamAuthorized 的 owner 豁免使其可查任意 scope（raw 面另有平台门）。
func TestQueryScopedMetricsPlatformAdminCrossTeam(t *testing.T) {
	fake := &fakeMetrics{}
	h := apitest.NewWithMetrics(t, fake)
	beta := newScopedFixture(t, h, "beta", "beta-proj")
	app := h.CreateApp(t, beta.projectID, "web")

	_, err := telemetryv1.NewMetricsServiceClient(h.Conn).QueryScopedMetrics(h.Ctx(),
		&telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: capability.MetricCPUPercent})
	require.NoError(t, err)
	queries := fake.scopedQueries()
	require.Len(t, queries, 1)
	assert.Equal(t, beta.projectID, queries[0].ProjectID)
	assert.Equal(t, app.GetId(), queries[0].AppID)
}

// TestQueryScopedMetricsTemplateRejectionIsInternal 钉 scoped 面收到 VM 4xx
// 时映射 E_INTERNAL（模板是服务端固定的，不是调用方 400）。
func TestQueryScopedMetricsTemplateRejectionIsInternal(t *testing.T) {
	fake := &fakeMetrics{
		scopedErr: &capability.MetricsQueryError{Status: 422, Message: "invalid expression"},
	}
	h := apitest.NewWithMetrics(t, fake)
	fixture := newScopedFixture(t, h, "alpha", "alpha-proj")
	app := h.CreateApp(t, fixture.projectID, "web")
	member := scopedMemberSession(t, h, "alpha-member", fixture.teamID, "alpha-member-pass")

	_, err := telemetryv1.NewMetricsServiceClient(h.Conn).QueryScopedMetrics(member,
		&telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: capability.MetricCPUCores})
	wantCode(t, err, codes.Internal, "E_INTERNAL")
}

// TestQueryScopedMetricsSentinels 钉超体/超时在 scoped 面走共享映射。
func TestQueryScopedMetricsSentinels(t *testing.T) {
	fake := &fakeMetrics{scopedErr: capability.ErrMetricsResultTooLarge}
	h := apitest.NewWithMetrics(t, fake)
	fixture := newScopedFixture(t, h, "alpha", "alpha-proj")
	app := h.CreateApp(t, fixture.projectID, "web")
	member := scopedMemberSession(t, h, "alpha-member", fixture.teamID, "alpha-member-pass")
	client := telemetryv1.NewMetricsServiceClient(h.Conn)

	_, err := client.QueryScopedMetrics(member,
		&telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: capability.MetricCPUCores})
	wantCode(t, err, codes.ResourceExhausted, "E_METRICS_RESULT_TOO_LARGE")

	fake.scopedErr = capability.ErrMetricsQueryTimeout
	_, err = client.QueryScopedMetrics(member,
		&telemetryv1.QueryScopedMetricsRequest{AppId: app.GetId(), Metric: capability.MetricCPUCores})
	wantCode(t, err, codes.DeadlineExceeded, "E_METRICS_QUERY_TIMEOUT")
}
