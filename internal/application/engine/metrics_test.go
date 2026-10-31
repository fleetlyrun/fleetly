package engine

// Metrics 采集环测试（F2.5，ADR-0041）：exposition 解析（cadvisor ground
// truth 形态夹具）、规则状态机（fire/for-window/resolve）、内置规则
//（s3 缺席 → 24h → firing）、通道派发（webhook httptest 假接收端）。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
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
func (f *fakeMetrics) QuerySeries(context.Context, string, time.Time, time.Time, time.Duration) ([]capability.Series, error) {
	return nil, nil
}
func (f *fakeMetrics) QueryScoped(context.Context, capability.ScopedQuery) (capability.SeriesPage, error) {
	return capability.SeriesPage{}, nil
}

// cadvisorFixture 是 exposition 夹具（平台容器 + 系统噪音行混布）。两个
// 平台载体：web（app 域——进告警样本 + 词汇）、postgres（database 域——
// 只进词汇），身份标签按 cadvisor 实测形态（container_label_* 前缀）。
func cadvisorFixture(cpu float64, mem float64) string {
	return fmt.Sprintf(`# HELP container_cpu_usage_seconds_total Total CPU time consumed.
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{boot_id="b",id="/",image=""} 999999 1767225600000
container_cpu_usage_seconds_total{boot_id="b",id="/system.slice/docker-abc123.scope",image="alpine:3.20",name="fleetly-x-web",container_label_fleetly_ns_app="01app",container_label_fleetly_ns_project="01proj",container_label_fleetly_workload_id="01workload",container_label_fleetly_process="web"} %f 1767225600000
container_cpu_usage_seconds_total{boot_id="b",id="/system.slice/docker-def456.scope",image="pgvector:17",name="fleetly-db-xyz",container_label_fleetly_ns_database="01db",container_label_fleetly_workload_id="01dbworkload",container_label_fleetly_process="postgres"} %f 1767225600000
# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{boot_id="b",id="/"} 999999999
container_memory_working_set_bytes{boot_id="b",id="/system.slice/docker-abc123.scope",image="alpine:3.20",name="fleetly-x-web",container_label_fleetly_ns_app="01app",container_label_fleetly_workload_id="01workload",container_label_fleetly_process="web"} %f
container_memory_working_set_bytes{boot_id="b",id="/system.slice/docker-def456.scope",image="pgvector:17",name="fleetly-db-xyz",container_label_fleetly_ns_database="01db",container_label_fleetly_workload_id="01dbworkload",container_label_fleetly_process="postgres"} %f
# TYPE machine_cpu_cores gauge
machine_cpu_cores{} 2
`, cpu, cpu, mem, mem)
}

// TestParseCadvisor 钉解析契约：平台容器入样（CPU 率差分 + mem 直取）、
// 非 /docker/ 聚合行跳过、核数观测、率百分比换算。
func TestParseCadvisor(t *testing.T) {
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{Data: DataOptions{DataRoot: t.TempDir()}})
	now := clock.Now()
	// 首拍：建差分基面（cpu=100）。
	s0, _ := e.parseCadvisor("01N1", []byte(cadvisorFixture(100, 1024)), now)
	require.Len(t, s0, 1)
	assert.InDelta(t, 0, s0[0].cpuRate, 0.001, "first scrape has no rate")
	assert.InDelta(t, 1024, s0[0].memBytes, 0.001)

	clock.Advance(metricsScrapeInterval)
	now2 := clock.Now()
	s1, _ := e.parseCadvisor("01N1", []byte(cadvisorFixture(100+3, 2048)), now2)
	require.Len(t, s1, 1)
	// 15s 内 3 核秒 → 0.2 核 / 2 核 = 10%。
	assert.InDelta(t, 10.0, s1[0].cpuRate, 0.01)
	assert.InDelta(t, 2048, s1[0].memBytes, 0.001)
	assert.Equal(t, "01app", s1[0].app, "app label value is lowercased (sanitizeNamePart downstream)")
	assert.Equal(t, "/system.slice/docker-abc123.scope", s1[0].id)
}

// TestParseCadvisorWorkloadVocabulary 钉负载词汇契约（ADR-0065）：一切带
// fleetly.workload.id 的载体入词汇（database 域只进词汇不进告警样本）、
// 身份标签还原、差分率与 percent 预计算、首拍 cpu 诚实缺席、exposition
// 金形态（指标名/标签集/行序）确定。
func TestParseCadvisorWorkloadVocabulary(t *testing.T) {
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{Data: DataOptions{DataRoot: t.TempDir()}})

	// 首拍：memory 在场、cpu 两指标缺席（差分无基面——零值与缺席是两件事）。
	_, vocab0 := e.parseCadvisor("01N1", []byte(cadvisorFixture(100, 1024)), clock.Now())
	require.Len(t, vocab0, 2)
	for _, w := range vocab0 {
		assert.False(t, w.cpuKnown, "first scrape has no differential baseline")
		assert.True(t, w.memKnown)
	}
	out0 := string(composeWorkloadExposition(vocab0))
	assert.NotContains(t, out0, MetricWorkloadCPUPercent)
	assert.NotContains(t, out0, MetricWorkloadCPUCores)
	assert.Contains(t, out0, MetricWorkloadMemory)

	// 二拍：率与 percent 预计算（3 核秒 / 15s = 0.2 核；/ 2 核 = 10%）。
	clock.Advance(metricsScrapeInterval)
	appSamples, vocab1 := e.parseCadvisor("01N1", []byte(cadvisorFixture(103, 2048)), clock.Now())
	assert.Len(t, appSamples, 1, "database-domain carriers stay out of the alerting path")
	require.Len(t, vocab1, 2)
	byContainer := map[string]workloadSample{}
	for _, w := range vocab1 {
		byContainer[w.container] = w
	}
	web, ok := byContainer["fleetly-x-web"]
	require.True(t, ok)
	assert.Equal(t, "01app", web.app)
	assert.Equal(t, "web", web.process)
	assert.Equal(t, "01workload", web.workload)
	assert.InDelta(t, 0.2, web.cpuCores, 0.001)
	assert.True(t, web.pctKnown)
	assert.InDelta(t, 10.0, web.cpuPercent, 0.01)
	assert.InDelta(t, 2048, web.memBytes, 0.001)
	pg, ok := byContainer["fleetly-db-xyz"]
	require.True(t, ok)
	assert.Equal(t, "", pg.app, "database domain carries no app subject")
	assert.Equal(t, "01db", pg.database)
	assert.Equal(t, "postgres", pg.process)

	// exposition 金形态：域主体在前、身份三元组收尾、行序按容器名固定。
	out := string(composeWorkloadExposition(vocab1))
	wantDB := MetricWorkloadMemory + `{database="01db",process="postgres",workload="01dbworkload",container="fleetly-db-xyz"} 2048`
	wantWeb := MetricWorkloadMemory + `{app="01app",process="web",workload="01workload",container="fleetly-x-web"} 2048`
	assert.Contains(t, out, wantDB)
	assert.Contains(t, out, wantWeb)
	assert.Less(t, strings.Index(out, wantDB), strings.Index(out, wantWeb), "rows sort by container name")
	assertLineValue(t, out, MetricWorkloadCPUCores+`{app="01app"`, 0.2)
	assertLineValue(t, out, MetricWorkloadCPUPercent+`{app="01app"`, 10.0)
	// 渲染幂等（同输入同字节——导入与断言都吃确定形态）。
	assert.Equal(t, out, string(composeWorkloadExposition(vocab1)))
}

// assertLineValue 取命中前缀的首行样本值做数值断言（percent 的浮点尾差
// 不进金形态——值域断言归 InDelta）。
func assertLineValue(t *testing.T, exposition, prefix string, want float64) {
	t.Helper()
	for _, line := range strings.Split(exposition, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		_, val, found := strings.Cut(line, "} ")
		require.True(t, found, "line %q carries a sample value", line)
		v, err := strconv.ParseFloat(val, 64)
		require.NoError(t, err)
		assert.InDelta(t, want, v, 0.01, "line %q", line)
		return
	}
	t.Fatalf("exposition has no line with prefix %q", prefix)
}

// TestEvaluateRulesStateMachine 钉状态机：越限首见起窗（pending_since 落
// 列）→ 窗内采样推进不重置 → for 窗满 → firing（事件+派发，迁移清列）→
// 回落 → ok（resolved 事件，清列）；无样本拍不误恢复。
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

	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Metrics: fm, Cipher: testCipher(t), Logger: discardLogger()},
		Options{Data: DataOptions{DataRoot: t.TempDir()}})
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))

	// 通道 + 规则（memory > 1000，for 30s）。
	sealed := sealChannelConfig(t, e, srv.URL)
	require.NoError(t, e.NotificationChannelRepo().Create(ctx, makeChannelRow("ch1", sealed)))
	require.NoError(t, e.AlertRuleRepo().Create(ctx, &alertrule.Rule{
		ID: "rule1", AppID: tAppID, Metric: alertrule.MetricMemoryWorkingSet,
		Threshold: 1000, ForSecs: 30, Enabled: true,
	}))

	// 首拍：越限（mem=2048>1000）→ 起窗落列，不 firing。
	now := clock.Now()
	e.evaluateRules(ctx, []containerSample{{app: strings.ToLower(tAppID), id: "/system.slice/docker-x.scope", memBytes: 2048}}, now)
	row, err := e.AlertRuleRepo().Get(ctx, "rule1")
	require.NoError(t, err)
	assert.Equal(t, alertrule.StateOK, row.State)
	assert.InDelta(t, 2048, row.LastValue, 0.001, "observed value lands on the row")
	assert.Equal(t, state.FormatTime(now), row.PendingSince, "first breach persists the window start on the row")

	// 窗内（+10s）：仍不 firing，起算点不重置（采样推进不重开窗）。
	clock.Advance(10 * time.Second)
	e.evaluateRules(ctx, []containerSample{{app: strings.ToLower(tAppID), id: "/system.slice/docker-x.scope", memBytes: 2048}}, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, "rule1")
	assert.Equal(t, alertrule.StateOK, row.State)
	assert.Equal(t, state.FormatTime(now), row.PendingSince, "in-window samples must not reset the pending start")

	// 窗满（+21s）：firing + webhook 派发 + 迁移清列。
	clock.Advance(21 * time.Second)
	e.evaluateRules(ctx, []containerSample{{app: strings.ToLower(tAppID), id: "/system.slice/docker-x.scope", memBytes: 2048}}, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, "rule1")
	assert.Equal(t, alertrule.StateFiring, row.State)
	assert.Empty(t, row.PendingSince, "the firing transition clears the pending column")
	_, hit := gotPayloads.Load("/")
	assert.True(t, hit, "firing transition must dispatch to the webhook channel")

	// 回落：ok + resolved 派发 + 清列。
	e.evaluateRules(ctx, []containerSample{{app: strings.ToLower(tAppID), id: "/system.slice/docker-x.scope", memBytes: 500}}, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, "rule1")
	assert.Equal(t, alertrule.StateOK, row.State)
	assert.Empty(t, row.PendingSince)

	// 无样本拍：不评估不误恢复（value 零跳过）。
	e.evaluateRules(ctx, nil, clock.Now())
	row, _ = e.AlertRuleRepo().Get(ctx, "rule1")
	assert.Equal(t, alertrule.StateOK, row.State)
}

// TestEvaluateRulesPendingWindowSurvivesRestart：pending 起算点直接落列
// （B2a W1b 直接改列形态）——构造 pending 行后换全新 engine 实例（重启
// 等价，内存面全失），窗从原起点继续：重启后首拍（已过 for 窗）即 firing
// 不重新起窗（重启不重置断言）。
func TestEvaluateRulesPendingWindowSurvivesRestart(t *testing.T) {
	db, clock := statetest.New(t)
	fm := &fakeMetrics{}
	mkEngine := func() *Engine {
		return New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Metrics: fm, Cipher: testCipher(t), Logger: discardLogger()},
			Options{Data: DataOptions{DataRoot: t.TempDir()}})
	}
	e := mkEngine()
	ctx := context.Background()
	require.NoError(t, seedProjectApp(t, clock, db))
	require.NoError(t, e.AlertRuleRepo().Create(ctx, &alertrule.Rule{
		ID: "rule1", AppID: tAppID, Metric: alertrule.MetricMemoryWorkingSet,
		Threshold: 1000, ForSecs: 30, Enabled: true,
	}))

	// 越限首见：pending 行落库（for 窗 30s 起算）。
	started := clock.Now()
	sample := []containerSample{{app: strings.ToLower(tAppID), id: "/system.slice/docker-x.scope", memBytes: 2048}}
	e.evaluateRules(ctx, sample, started)
	row, err := e.AlertRuleRepo().Get(ctx, "rule1")
	require.NoError(t, err)
	require.Equal(t, state.FormatTime(started), row.PendingSince)
	require.Equal(t, alertrule.StateOK, row.State)

	// 重启等价：全新 engine 实例（同库，内存 pending 面不存在）；窗内
	// 采样推进仍不 firing、起算点保持原值。
	clock.Advance(10 * time.Second)
	e2 := mkEngine()
	e2.evaluateRules(ctx, sample, clock.Now())
	row2, err := e2.AlertRuleRepo().Get(ctx, "rule1")
	require.NoError(t, err)
	assert.Equal(t, state.FormatTime(started), row2.PendingSince, "restart must resume from the persisted window start")
	assert.Equal(t, alertrule.StateOK, row2.State, "still inside the for-window after restart")

	// 窗满（重启实例上）：firing——窗从原起点继续，不重置重计。
	clock.Advance(21 * time.Second)
	e2.evaluateRules(ctx, sample, clock.Now())
	row3, err := e2.AlertRuleRepo().Get(ctx, "rule1")
	require.NoError(t, err)
	assert.Equal(t, alertrule.StateFiring, row3.State,
		"the window must continue from the original start point across a restart (no reset)")
	assert.Empty(t, row3.PendingSince)
}

// TestEvaluateSystemRules 钉内置规则：s3 缺席 24h → firing；配置 s3 → 归位。
func TestEvaluateSystemRules(t *testing.T) {
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Logger: discardLogger()},
		Options{Data: DataOptions{DataRoot: t.TempDir()}, Backup: BackupOptions{PlatformBackup: &PlatformBackupConfig{Interval: time.Hour}}})
	ctx := context.Background()

	e.evaluateSystemRules(ctx, clock.Now()) // 起窗
	assert.False(t, e.SystemOffsiteAlertState().State == alertrule.StateFiring)
	clock.Advance(systemOffsiteFor + time.Minute)
	e.evaluateSystemRules(ctx, clock.Now())
	assert.Equal(t, alertrule.StateFiring, e.SystemOffsiteAlertState().State, "24h without an offsite repo must fire")

	// 配 s3 → 归位（resolved 面；无通道时派发静默空转）。
	e.opts.Backup.PlatformBackup.S3 = &S3RepoConfig{Endpoint: "s3.example", Bucket: "b"}
	e.evaluateSystemRules(ctx, clock.Now())
	assert.Equal(t, alertrule.StateOK, e.SystemOffsiteAlertState().State)
}

// TestSystemOffsiteAlertStateRace 钉锁面：内置规则状态对（firing/above）
// 由采集环 goroutine 写（evaluateSystemRules）、gRPC handler goroutine 经
// SystemOffsiteAlertState 读——两侧无 happens-before，锁面覆盖时 -race
// 绿；map 面（above/lastCPU/cores）不进此测（采集环单写者纪律承载）。
func TestSystemOffsiteAlertStateRace(t *testing.T) {
	db, clock := statetest.New(t)
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Logger: discardLogger()},
		Options{Data: DataOptions{DataRoot: t.TempDir()}, Backup: BackupOptions{PlatformBackup: &PlatformBackupConfig{Interval: time.Hour}}})
	ctx := context.Background()

	// 写侧先手：起窗并推到 firing（状态对双臂都建立）。
	e.evaluateSystemRules(ctx, clock.Now())
	clock.Advance(systemOffsiteFor + time.Minute)
	e.evaluateSystemRules(ctx, clock.Now())
	require.Equal(t, alertrule.StateFiring, e.SystemOffsiteAlertState().State)

	// 读侧：N 个 goroutine 并发快照观测面（gRPC handler 形状）。
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = e.SystemOffsiteAlertState()
				}
			}
		}()
	}
	// 写侧与读侧交错：归位 → 起窗 → 满窗 firing 的迁移沿反复穿越
	//（firing 与 above 双字段都有并发写落点）。
	for i := 0; i < 20; i++ {
		e.opts.Backup.PlatformBackup.S3 = &S3RepoConfig{Endpoint: "s3.example", Bucket: "b"}
		clock.Advance(time.Hour)
		e.evaluateSystemRules(ctx, clock.Now())
		e.opts.Backup.PlatformBackup.S3 = nil
		clock.Advance(time.Hour)
		e.evaluateSystemRules(ctx, clock.Now())
		clock.Advance(systemOffsiteFor)
		e.evaluateSystemRules(ctx, clock.Now())
	}
	close(stop)
	wg.Wait()

	// 末拍配 s3 → 归位（收口后的终态断言）。
	e.opts.Backup.PlatformBackup.S3 = &S3RepoConfig{Endpoint: "s3.example", Bucket: "b"}
	e.evaluateSystemRules(ctx, clock.Now())
	assert.Equal(t, alertrule.StateOK, e.SystemOffsiteAlertState().State)
}

// TestMetricsStepCadence 钉粗节拍锚：15s 内第二拍跳过。
func TestMetricsStepCadence(t *testing.T) {
	db, clock := statetest.New(t)
	fm := &fakeMetrics{}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Metrics: fm, Logger: discardLogger()},
		Options{Data: DataOptions{DataRoot: t.TempDir()}})
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
	c, err := keyring.LoadCipher(t.TempDir())
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
