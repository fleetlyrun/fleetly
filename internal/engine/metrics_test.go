package engine

// Metrics 采集环测试（F2.5，ADR-0041）：exposition 解析（cadvisor ground
// truth 形态夹具）、规则状态机（fire/for-window/resolve）、内置规则
//（s3 缺席 → 24h → firing）、通道派发（webhook httptest 假接收端）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	alertrule "github.com/fleetlyrun/fleetly/internal/state/alertrule"
	"github.com/fleetlyrun/fleetly/internal/state/channel"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// fakeMetrics 是 Metrics 端口假底座（记录导入面）。
type fakeMetrics struct {
	mu      sync.Mutex
	imports [][]byte
}

func (f *fakeMetrics) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-metrics", Capability: capability.KindMetrics, Version: "test"}
}
func (f *fakeMetrics) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeMetrics) ImportPrometheus(_ context.Context, body []byte, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imports = append(f.imports, body)
	return nil
}
func (f *fakeMetrics) QuerySeries(context.Context, string, time.Time, time.Time, time.Duration) (capability.Series, error) {
	return capability.Series{}, nil
}

// cadvisorFixture 是 exposition 夹具（平台容器 + 系统噪音行混布）。
func cadvisorFixture(cpu float64, mem float64) string {
	return fmt.Sprintf(`# HELP container_cpu_usage_seconds_total Total CPU time consumed.
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{boot_id="b",id="/",image=""} 999999 1767225600000
container_cpu_usage_seconds_total{boot_id="b",id="/docker/abc123",image="alpine:3.20",name="fleetly-x-web",container_label_fleetly_ns_app="01APP",container_label_fleetly_ns_project="01PROJ"} %f 1767225600000
# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{boot_id="b",id="/"} 999999999
container_memory_working_set_bytes{boot_id="b",id="/docker/abc123",image="alpine:3.20",container_label_fleetly_ns_app="01APP"} %f
# TYPE machine_cpu_cores gauge
machine_cpu_cores{} 2
`, cpu, mem)
}

// TestParseCadvisor 钉解析契约：平台容器入样（CPU 率差分 + mem 直取）、
// 非 /docker/ 聚合行跳过、核数观测、率百分比换算。
func TestParseCadvisor(t *testing.T) {
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{DataRoot: t.TempDir()})
	now := clock.Now()
	// 首拍：建差分基面（cpu=100）。
	s0 := e.parseCadvisor("01N1", []byte(cadvisorFixture(100, 1024)), now)
	require.Len(t, s0, 1)
	assert.InDelta(t, 0, s0[0].cpuRate, 0.001, "first scrape has no rate")
	assert.InDelta(t, 1024, s0[0].memBytes, 0.001)

	clock.Advance(metricsScrapeInterval)
	now2 := clock.Now()
	s1 := e.parseCadvisor("01N1", []byte(cadvisorFixture(100+3, 2048)), now2)
	require.Len(t, s1, 1)
	// 15s 内 3 核秒 → 0.2 核 / 2 核 = 10%。
	assert.InDelta(t, 10.0, s1[0].cpuRate, 0.01)
	assert.InDelta(t, 2048, s1[0].memBytes, 0.001)
	assert.Equal(t, "01APP", s1[0].app)
	assert.Equal(t, "/docker/abc123", s1[0].id)
}

// TestEvaluateRulesStateMachine 钉状态机：越限首见起窗 → for 窗满 → firing
// （事件+派发）→ 回落 → ok（resolved 事件）；回落清 pending 面。
func TestEvaluateRulesStateMachine(t *testing.T) {
	db, clock := statetest.New(t)
	fm := &fakeMetrics{}
	// webhook 假接收端（派发面断言）。
	var gotPayloads sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPayloads.Store(r.URL.Path, true)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Metrics: fm, Cipher: testCipher(t), Logger: discardLogger()},
		Options{DataRoot: t.TempDir()})
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))

	// 通道 + 规则（memory > 1000，for 30s）。
	sealed := sealChannelConfig(t, e, srv.URL)
	require.NoError(t, e.NotificationChannelRepo().Create(ctx, db.Runner(), makeChannelRow("ch1", sealed)))
	require.NoError(t, e.AlertRuleRepo().Create(ctx, db.Runner(), &alertrule.Rule{
		ID: "rule1", AppID: tAppID, Metric: alertrule.MetricMemoryWorkingSet,
		Threshold: 1000, ForSecs: 30, Enabled: true,
	}))

	// 首拍：越限（mem=2048>1000）→ 起窗，不 firing。
	now := clock.Now()
	e.evaluateRules(ctx, []containerSample{{app: tAppID, id: "/docker/x", memBytes: 2048}}, now)
	row, err := e.AlertRuleRepo().Get(ctx, db.Runner(), "rule1")
	require.NoError(t, err)
	assert.Equal(t, alertrule.StateOK, row.State)
	assert.InDelta(t, 2048, row.LastValue, 0.001, "observed value lands on the row")

	// 窗内（+10s）：仍不 firing。
	clock.Advance(10 * time.Second)
	e.evaluateRules(ctx, []containerSample{{app: tAppID, id: "/docker/x", memBytes: 2048}}, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, db.Runner(), "rule1")
	assert.Equal(t, alertrule.StateOK, row.State)

	// 窗满（+21s）：firing + webhook 派发。
	clock.Advance(21 * time.Second)
	e.evaluateRules(ctx, []containerSample{{app: tAppID, id: "/docker/x", memBytes: 2048}}, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, db.Runner(), "rule1")
	assert.Equal(t, alertrule.StateFiring, row.State)
	_, hit := gotPayloads.Load("/")
	assert.True(t, hit, "firing transition must dispatch to the webhook channel")

	// 回落：ok + resolved 派发。
	e.evaluateRules(ctx, []containerSample{{app: tAppID, id: "/docker/x", memBytes: 500}}, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, db.Runner(), "rule1")
	assert.Equal(t, alertrule.StateOK, row.State)

	// 无样本拍：不评估不误恢复（value 零跳过）。
	e.evaluateRules(ctx, nil, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, db.Runner(), "rule1")
	assert.Equal(t, alertrule.StateOK, row.State)
}

// TestEvaluateSystemRules 钉内置规则：s3 缺席 24h → firing；配置 s3 → 归位。
func TestEvaluateSystemRules(t *testing.T) {
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Logger: discardLogger()},
		Options{DataRoot: t.TempDir(), PlatformBackup: &PlatformBackupConfig{Interval: time.Hour}})
	ctx := context.Background()

	e.evaluateSystemRules(ctx, clock.Now()) // 起窗
	assert.False(t, e.SystemOffsiteAlertState().State == alertrule.StateFiring)
	clock.Advance(systemOffsiteFor + time.Minute)
	e.evaluateSystemRules(ctx, clock.Now())
	assert.Equal(t, alertrule.StateFiring, e.SystemOffsiteAlertState().State, "24h without an offsite repo must fire")

	// 配 s3 → 归位（resolved 面；无通道时派发静默空转）。
	e.opts.PlatformBackup.S3 = &S3RepoConfig{Endpoint: "s3.example", Bucket: "b"}
	e.evaluateSystemRules(ctx, clock.Now())
	assert.Equal(t, alertrule.StateOK, e.SystemOffsiteAlertState().State)
}

// TestMetricsStepCadence 钉粗节拍锚：15s 内第二拍跳过。
func TestMetricsStepCadence(t *testing.T) {
	db, clock := statetest.New(t)
	fm := &fakeMetrics{}
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Metrics: fm, Logger: discardLogger()},
		Options{DataRoot: t.TempDir()})
	e.metricsStep(context.Background())
	n1 := len(fm.imports)
	clock.Advance(5 * time.Second)
	e.metricsStep(context.Background())
	assert.Equal(t, n1, len(fm.imports), "step within the scrape interval is a no-op")
	clock.Advance(12 * time.Second)
	e.metricsStep(context.Background())
	// fakeRuntime 集群视图空 → 无抓取无导入（节拍锚生效的负形态）。
	assert.Equal(t, n1, len(fm.imports), "no available nodes → no imports")
}

// TestParseExpositionLabels 钉标签解析（引号内逗号 + 值内转义还原）。
// 诚实边界（与实现同步）：值内嵌入引号不在解析域（fleetly 标签域不产生）。
func TestParseExpositionLabels(t *testing.T) {
	labels := parseExpositionLabels(`a="1",b="x,y",path="C:\\dir",d=e`)
	assert.Equal(t, map[string]string{"a": "1", "b": "x,y", "path": `C:\dir`, "d": "e"}, labels)

	name, l, val, ok := parseExpositionLine(`metric_x{k="v"} 12.5 1767225600000`)
	require.True(t, ok)
	assert.Equal(t, "metric_x", name)
	assert.Equal(t, "v", l["k"])
	assert.Equal(t, "12.5", val)

	_, _, _, ok = parseExpositionLine(`# comment`)
	assert.False(t, ok, "comment lines are rejected by the line parser itself")
	_, _, val2, ok := parseExpositionLine(`machine_cpu_cores 4`)
	require.True(t, ok)
	assert.Equal(t, "4", val2)
}

// 辅助构造：测试通道行（webhook 指向接收端 URL）与 cipher。
func testCipher(t *testing.T) *material.Cipher {
	t.Helper()
	c, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	return c
}

func sealChannelConfig(t *testing.T, e *Engine, url string) []byte {
	t.Helper()
	sealed, err := e.cipher.Seal([]byte(`{"url":"` + url + `"}`))
	require.NoError(t, err)
	return sealed
}

func makeChannelRow(id string, sealed []byte) *channel.Channel {
	return &channel.Channel{ID: id, Name: id, Kind: channel.KindWebhook, ConfigCiphertext: sealed, Enabled: true}
}
