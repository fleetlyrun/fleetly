package cmd

// H8 阶段 B：metrics series golden（设计 docs/design/2026-10-19-security-
// backlog-design.md §5.4/§7）——成功面人类/--json 双形态（fake Metrics 经
// apitest.NewWithMetrics 注入，生产装配不变）+ App/Database 双 scope +
// usage 拒绝/服务端 store 停用错误面。golden 再生成：
// go test ./cmd/fleetly/cmd -run 'TestGoldenMetricsSeries' -update。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// goldenScopedMetrics 是 metrics series golden 的假 Metrics 面：记录
// scoped 查询参数（scope/process 构造的断言面）并返回固定 page。
type goldenScopedMetrics struct {
	queries []capability.ScopedQuery
	page    capability.SeriesPage
}

func (f *goldenScopedMetrics) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "golden-metrics", Capability: capability.KindMetrics}
}

func (f *goldenScopedMetrics) Health(_ context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *goldenScopedMetrics) ImportPrometheus(_ context.Context, _ []byte, _ map[string]string) error {
	return nil
}

func (f *goldenScopedMetrics) QuerySeries(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]capability.Series, error) {
	return nil, nil
}

func (f *goldenScopedMetrics) QueryScoped(_ context.Context, q capability.ScopedQuery) (capability.SeriesPage, error) {
	f.queries = append(f.queries, q)
	return f.page, nil
}

// newGoldenMetricsHarness 装配自动驱动夹具 + 注入 fake Metrics，并把 CLI
// 拨号接缝/owner token 指向该夹具（与 newGoldenHarness 同款接缝）。
func newGoldenMetricsHarness(t *testing.T, metrics capability.Metrics) *apitest.Harness {
	t.Helper()
	h := apitest.NewWithMetrics(t, metrics)
	origDial := dialClient
	dialClient = func(_ string, opts ...fleetly.Option) (*fleetly.Client, error) {
		return fleetly.Dial("passthrough:///bufnet", append(opts, h.DialOpts()...)...)
	}
	t.Cleanup(func() { dialClient = origDial })
	t.Setenv("FLEETLY_TOKEN", h.Token)
	return h
}

func TestGoldenMetricsSeries(t *testing.T) {
	fake := &goldenScopedMetrics{}
	h := newGoldenMetricsHarness(t, fake)
	project := h.CreateProject(t, "shop")
	app := h.CreateApp(t, project.GetId(), "web")
	db, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(h.Ctx(),
		&structurev1.CreateDatabaseRequest{ProjectId: project.GetId(), Name: "data", Engine: "postgres"})
	require.NoError(t, err)

	// 固定时间窗/点值（fake 不受服务端钳制影响）——golden 确定性锚。
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	fake.page = capability.SeriesPage{
		Series: []capability.Series{
			{
				Metric: map[string]string{"app": app.GetId(), "process": "web", "container": "fleetly-web-1"},
				Points: []capability.SeriesPoint{
					{Time: t0, Value: 12.5},
					{Time: t0.Add(time.Minute), Value: 0}, // protojson 零值缺省出口锚
				},
			},
			{
				Metric: map[string]string{"app": app.GetId(), "process": "worker"},
				Points: []capability.SeriesPoint{{Time: t0, Value: 3}},
			},
		},
		Total: 2,
	}
	appArgs := []string{
		"metrics", "series", "--app", app.GetId(), "--metric", "cpu_percent", "--process", "web",
		"--start", "2026-10-09T12:00:00Z", "--end", "2026-10-09T13:00:00Z", "--step-seconds", "60",
	}
	code, out, stderr := runCLI(t, appArgs...)
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "metrics-series", normalizeGolden(out))

	require.Len(t, fake.queries, 1)
	q := fake.queries[0]
	require.Equal(t, app.GetId(), q.AppID, "app scope is passed as app_id")
	require.Empty(t, q.DatabaseID)
	require.Equal(t, "cpu_percent", q.Metric)
	require.Equal(t, "web", q.Process)
	require.Equal(t, 60*time.Second, q.Step)
	require.Equal(t, capability.ScopedMetricsMaxSeries, q.MaxSeries)

	code, out, stderr = runCLI(t, append(append([]string{}, appArgs...), "--json")...)
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "metrics-series-json", normalizeGolden(out))

	// Database scope：真实库行授权后由服务端解析 Project；cpu_percent 仅
	// app scope，database 走 memory。
	fake.page = capability.SeriesPage{
		Series: []capability.Series{{
			Metric: map[string]string{"database": db.GetDatabase().GetId(), "container": "fleetly-db-data-1"},
			Points: []capability.SeriesPoint{{Time: t0, Value: 1048576}},
		}},
		Total: 1,
	}
	dbArgs := []string{
		"metrics", "series", "--database", db.GetDatabase().GetId(), "--metric", "memory_working_set_bytes",
		"--start", "2026-10-09T12:00:00Z", "--end", "2026-10-09T13:00:00Z",
	}
	code, out, stderr = runCLI(t, append(append([]string{}, dbArgs...), "--json")...)
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "metrics-series-database-json", normalizeGolden(out))

	require.Len(t, fake.queries, 3)
	q = fake.queries[2]
	require.Empty(t, q.AppID, "database scope must not carry app_id")
	require.Equal(t, db.GetDatabase().GetId(), q.DatabaseID)
	require.Equal(t, "memory_working_set_bytes", q.Metric)
	require.Empty(t, q.Process)

	// Project scope：--project 只带 project_id（服务端按行级授权聚合本队
	// 项目）；project 无 process 维度、无 cpu_percent 语义。
	fake.page = capability.SeriesPage{
		Series: []capability.Series{{
			Metric: map[string]string{"container_label_fleetly_ns_project": project.GetId(), "container": "fleetly-web-1"},
			Points: []capability.SeriesPoint{{Time: t0, Value: 2097152}},
		}},
		Total: 1,
	}
	projArgs := []string{
		"metrics", "series", "--project", project.GetId(), "--metric", "memory_working_set_bytes",
		"--start", "2026-10-09T12:00:00Z", "--end", "2026-10-09T13:00:00Z",
	}
	code, out, stderr = runCLI(t, projArgs...)
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "metrics-series-project", normalizeGolden(out))

	code, out, stderr = runCLI(t, append(append([]string{}, projArgs...), "--json")...)
	require.Equal(t, 0, code, "stderr=%q", stderr)
	require.Empty(t, stderr)
	compareGolden(t, "metrics-series-project-json", normalizeGolden(out))

	require.Len(t, fake.queries, 5)
	q = fake.queries[4]
	require.Empty(t, q.AppID, "project scope must not carry app_id")
	require.Empty(t, q.DatabaseID, "project scope must not carry database_id")
	require.Equal(t, project.GetId(), q.ProjectID)
	require.Equal(t, "memory_working_set_bytes", q.Metric)
	require.Empty(t, q.Process)
}

// TestMetricsSeriesProjectCPUPercentRejected 钉 project scope 的 cpu_percent
// 由服务端拒绝（400 信封；fake 零调用——CLI 不复制服务端指标白名单）。
func TestMetricsSeriesProjectCPUPercentRejected(t *testing.T) {
	fake := &goldenScopedMetrics{}
	h := newGoldenMetricsHarness(t, fake)
	project := h.CreateProject(t, "shop")

	code, out, stderr := runCLI(t, "metrics", "series", "--project", project.GetId(), "--metric", "cpu_percent")
	require.Equal(t, 1, code, "stderr=%q", stderr)
	require.Empty(t, out, "error paths write nothing to stdout")
	require.Contains(t, stderr, "E_INVALID_ARGUMENT")
	require.Contains(t, stderr, "cpu_percent is only available for app scope")
	require.Empty(t, fake.queries, "rejected requests must not reach Metrics")
}

// TestMetricsSeriesFlagValidation 钉客户端参数校验（scope 恰一、
// --metric 必填）的 usage 拒绝面；golden 之外的单测直断言退出码/文案。
func TestMetricsSeriesFlagValidation(t *testing.T) {
	_ = newGoldenHarness(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no-scope", []string{"metrics", "series", "--metric", "cpu_percent"}, "exactly one of --app, --database or --project is required"},
		{"scope-conflict", []string{"metrics", "series", "--app", "a", "--database", "d", "--metric", "cpu_percent"}, "--app, --database and --project are mutually exclusive"},
		{"project-scope-conflict", []string{"metrics", "series", "--project", "p", "--app", "a", "--metric", "memory_working_set_bytes"}, "--app, --database and --project are mutually exclusive"},
		{"no-metric", []string{"metrics", "series", "--app", "a"}, "--metric is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := runCLI(t, tc.args...)
			require.Equal(t, exitUsage, code)
			require.Empty(t, out)
			require.Contains(t, stderr, tc.want)
		})
	}
}

// TestFormatMetricLabels 钉人类输出标签组的稳定序（map 遍历序不进入
// golden）与空标签形态。
func TestFormatMetricLabels(t *testing.T) {
	require.Equal(t, "app=web,container=c1,process=web", formatMetricLabels(map[string]string{
		"process": "web", "app": "web", "container": "c1",
	}))
	require.Equal(t, "-", formatMetricLabels(nil))
}

// TestGoldenMetricsSeriesErrors 钉 usage 拒绝与服务端 store 停用面（stderr
// 是内容面；stdout 空）。
func TestGoldenMetricsSeriesErrors(t *testing.T) {
	h := newGoldenHarness(t)
	project := h.CreateProject(t, "shop")
	app := h.CreateApp(t, project.GetId(), "web")

	cases := []struct {
		name string
		args []string
		code int
	}{
		{"metrics-series-no-scope", []string{"metrics", "series", "--metric", "cpu_percent"}, exitUsage},
		{"metrics-series-scope-conflict", []string{"metrics", "series", "--app", app.GetId(), "--database", "db-golden", "--metric", "cpu_percent"}, exitUsage},
		{"metrics-series-project-scope-conflict", []string{"metrics", "series", "--project", project.GetId(), "--app", app.GetId(), "--metric", "memory_working_set_bytes"}, exitUsage},
		{"metrics-series-no-metric", []string{"metrics", "series", "--app", app.GetId()}, exitUsage},
		{"metrics-series-error", []string{"metrics", "series", "--app", app.GetId(), "--metric", "cpu_percent"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := runCLI(t, tc.args...)
			require.Equal(t, tc.code, code, "stderr=%q", stderr)
			require.Empty(t, out, "error paths write nothing to stdout")
			compareGolden(t, tc.name, normalizeGolden(stderr))
		})
	}
}
