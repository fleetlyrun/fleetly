package victoriametrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New("10.0.0.1:8428", t.TempDir(), 30)
	require.NoError(t, err)
	return p
}

// TestCredentialPersistRoundTrip 钉凭证幂等持久（E28）+ 权限 + fail loud。
func TestCredentialPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:8428", dir, 30)
	require.NoError(t, err)
	p2, err := New("10.0.0.1:8428", dir, 30)
	require.NoError(t, err)
	assert.Equal(t, p1.cred.Secret, p2.cred.Secret, "credential must persist across constructions")

	info, err := os.Stat(filepath.Join(dir, "keys", credentialFile))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	_, err = New("10.0.0.1:8428", "", 30)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keys", credentialFile), []byte("{"), 0o600))
	_, err = New("10.0.0.1:8428", dir, 30)
	require.Error(t, err, "malformed credential must fail loudly")
}

// TestManagedWorkloadsShape 钉受管双 Workload 形态（ADR-0041 决策 1/2）：
// VM 存储（mesh/卷/宽限/密码 file:// 不进 argv）+ cadvisor（Global/host
// 发布/四条只读宿主绑定）。
func TestManagedWorkloadsShape(t *testing.T) {
	p := newTestProvider(t)
	ws := p.ManagedWorkloads()
	require.Len(t, ws, 2)

	vm := ws[0]
	assert.Equal(t, "victoriametrics/victoria-metrics:v1.152.0", vm.Image)
	assert.Equal(t, []capability.PortPublish{{PublishedPort: 8428, TargetPort: 8428}}, vm.Publish, "mesh publish is the zero-value default")
	require.Len(t, vm.Volumes, 1)
	assert.Equal(t, 60*time.Second, vm.StopGrace)
	assert.False(t, vm.Global)
	require.NotEmpty(t, vm.Command)
	assert.True(t, strings.HasPrefix(vm.Command[0], "/"))
	joined := strings.Join(vm.Command, " ")
	assert.Contains(t, joined, "-retentionPeriod=30d")
	assert.Contains(t, joined, "-httpAuth.password=file:///run/secrets/"+authFile)
	assert.NotContains(t, joined, p.cred.Secret, "password must never appear in argv")

	cd := ws[1]
	assert.Equal(t, "gcr.io/cadvisor/cadvisor:v0.55.1", cd.Image)
	assert.True(t, cd.Global, "cadvisor must schedule one task per node")
	assert.EqualValues(t, 1, cd.Replicas, "global form carries the per-node desired count for spec-parity drift comparison")
	require.Len(t, cd.Publish, 1)
	assert.Equal(t, capability.PublishModeHost, cd.Publish[0].Mode, "cadvisor endpoint is host-published per node")
	assert.Equal(t, int32(8080), cd.Publish[0].PublishedPort)
	// 滚动序取舍钉死（N2 评审 P2-5）：host 发布的 stop-first 由 swarm 翻译层
	// 按本声明（PublishModeHost）统一执法，声明面无序字段；宽限留零 =
	// 编排器缺省硬杀兜底——无状态采集端 SIGTERM 即退，与数据面 60s 排水窗
	// 分立。
	assert.Zero(t, cd.StopGrace, "stateless collector keeps zero grace (provider default 10s hard-kill bound); rolling order is enforced at the swarm translation layer from the host-publish declaration")
	require.Len(t, cd.HostBinds, 4)
	for _, b := range cd.HostBinds {
		assert.True(t, b.ReadOnly, "collector binds must be read-only")
	}
	assert.Empty(t, cd.Volumes, "collector is stateless — no pin face")
	assert.True(t, cd.SkipMaterials, "collector must not receive the store credential (ADR-0041)")
}

// TestMaterialsStableAcrossRestarts 钉 E28：材料字节跨构造稳定。
func TestMaterialsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:8428", dir, 30)
	require.NoError(t, err)
	first := p1.ManagedMaterials()
	for i := 0; i < 50; i++ {
		p2, err := New("10.0.0.1:8428", dir, 30)
		require.NoError(t, err)
		assert.Equal(t, string(first.SecretFiles[authFile]), string(p2.ManagedMaterials().SecretFiles[authFile]))
	}
}

// TestImportPrometheusShape 钉导入请求：extra_label 附加 + basic auth +
// exposition 原文体。
func TestImportPrometheusShape(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	err := p.ImportPrometheus(context.Background(), []byte("container_cpu_usage_seconds_total 123\n"),
		map[string]string{"job": "fleetly-cadvisor", "node": "01NODE"})
	require.NoError(t, err)
	assert.Contains(t, gotPath, "/api/v1/import/prometheus")
	// url.Values.Encode：键值分隔 = 保持字面，值内 =（job=fleetly-cadvisor
	// 整体是值）编码 %3D——服务端解码后是 extra_label=job=fleetly-cadvisor。
	assert.Contains(t, gotPath, "extra_label=job%3Dfleetly-cadvisor")
	assert.Contains(t, gotPath, "extra_label=node%3D01NODE")
	assert.True(t, strings.HasPrefix(gotAuth, "Basic "))
	assert.Equal(t, "text/plain", gotCT)
	assert.Equal(t, "container_cpu_usage_seconds_total 123\n", string(gotBody))

	require.NoError(t, p.ImportPrometheus(context.Background(), nil, nil), "empty body is a no-op")
}

// TestImportRejected 钉非 2xx 上抛（节拍退避面）。
func TestImportRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("maintenance"))
	}))
	defer srv.Close()
	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	err := p.ImportPrometheus(context.Background(), []byte("x 1\n"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

// TestQuerySeriesRoundTrip 钉查询请求形态（query/start/end/step unix 秒）
// 与 matrix 响应解析（真机 ground truth 形态：Prometheus 兼容 envelope）；
// 多标签命中逐序列返回（C1 Console 图表批：单序列截断已废）。
func TestQuerySeriesRoundTrip(t *testing.T) {
	var formMu sync.Mutex
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		formMu.Lock()
		gotForm = r.Form
		formMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[` +
			`{"metric":{"__name__":"container_memory_working_set_bytes","node":"01N1"},"values":[[1767225600,"1024.5"],[1767225615,"2048"]]},` +
			`{"metric":{"__name__":"container_memory_working_set_bytes","node":"01N2"},"values":[]}` +
			`]}}` + "\n"))
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	series, err := p.QuerySeries(context.Background(),
		`container_memory_working_set_bytes`,
		time.Unix(1767225600, 0), time.Unix(1767225900, 0), 15*time.Second)
	require.NoError(t, err)
	require.Len(t, series, 2)
	s := series[0]
	assert.Equal(t, map[string]string{"__name__": "container_memory_working_set_bytes", "node": "01N1"}, s.Metric)
	require.Len(t, s.Points, 2)
	assert.InDelta(t, 1024.5, s.Points[0].Value, 0.001)
	assert.InDelta(t, 2048, s.Points[1].Value, 0.001)
	assert.Equal(t, time.Unix(1767225615, 0).UTC(), s.Points[1].Time)
	assert.Empty(t, series[1].Points)

	formMu.Lock()
	q := gotForm
	formMu.Unlock()
	assert.Equal(t, `container_memory_working_set_bytes`, q.Get("query"))
	assert.Equal(t, "1767225600", q.Get("start"))
	assert.Equal(t, "1767225900", q.Get("end"))
	assert.Equal(t, "15", q.Get("step"))
}

// TestQuerySeriesError 钉 VM 4xx → capability.MetricsQueryError（F-B1：
// 查询错属调用方面，Message 取信封 error 字段——API 层据此落
// E_INVALID_ARGUMENT 而非 E_INTERNAL）。
func TestQuerySeriesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid PromQL"}`))
	}))
	defer srv.Close()
	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	_, err := p.QuerySeries(context.Background(), "~~", time.Now().Add(-time.Minute), time.Now(), 0)
	require.Error(t, err)
	var qe *capability.MetricsQueryError
	require.ErrorAs(t, err, &qe)
	assert.Equal(t, http.StatusBadRequest, qe.Status)
	assert.Equal(t, "invalid PromQL", qe.Message)
	assert.Contains(t, err.Error(), "invalid PromQL")
}

// TestQuerySeriesServerError 钉 5xx 不入 MetricsQueryError 型——平台故障
// 面保持 E_INTERNAL 语义，不与调用方查询错混淆（F-B1 分流的另一侧）。
func TestQuerySeriesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"timeout","error":"query timeout"}`))
	}))
	defer srv.Close()
	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	_, err := p.QuerySeries(context.Background(), "up", time.Now().Add(-time.Minute), time.Now(), 0)
	require.Error(t, err)
	var qe *capability.MetricsQueryError
	assert.NotErrorAs(t, err, &qe)
	assert.Contains(t, err.Error(), "503")
}

// TestHealthProbe 钉 TCP 探测。
func TestHealthProbe(t *testing.T) {
	p := newTestProvider(t)
	assert.False(t, p.Health(context.Background()).Healthy)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	p2, err := New(ln.Addr().String(), t.TempDir(), 30)
	require.NoError(t, err)
	assert.True(t, p2.Health(context.Background()).Healthy)
}

// TestRetentionVariants 钉保留窗旗标随构造参数。
func TestRetentionVariants(t *testing.T) {
	for _, days := range []int64{7, 90} {
		p, err := New("10.0.0.1:8428", t.TempDir(), days)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(p.ManagedWorkloads()[0].Command, " "), fmt.Sprintf("-retentionPeriod=%dd", days))
	}
	p, err := New("10.0.0.1:8428", t.TempDir(), 0)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(p.ManagedWorkloads()[0].Command, " "), "-retentionPeriod=30d")
}

// matrixBody 造 n 条序列的 query_range 成功响应（每条一个采样点；H8
// 阶段 A 序列上限测试用）。
func matrixBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"metric":{"__name__":"up","instance":"i%d"},"values":[[1767225600,"1"]]}`, i)
	}
	b.WriteString(`]}}`)
	return []byte(b.String())
}

// paddedMatrixBody 造一条带 padding 字节标签的序列响应（响应体上限测试用；
// 单序列小值点，撑大的是标签而不是命中数）。
func paddedMatrixBody(paddingBytes int) []byte {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","padding":"`)
	b.WriteString(strings.Repeat("x", paddingBytes))
	b.WriteString(`"},"values":[[1767225600,"1"]]}]}}`)
	return []byte(b.String())
}

// TestQuerySeriesRawLimits 钉 raw 诊断面的资源上限（设计文档 §5.3.3）：
// 响应体 8 MiB、序列数 1000。超限是调用方查询/时间窗过宽——返回
// capability.ErrMetricsResultTooLarge（API 面落 E_METRICS_RESULT_TOO_LARGE
// 429），边界值（恰好 1000 序列、8 MiB 以内）正常返回。
func TestQuerySeriesRawLimits(t *testing.T) {
	providerFor := func(t *testing.T, body []byte) *Provider {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)
		p := newTestProvider(t)
		p.addr = strings.TrimPrefix(srv.URL, "http://")
		return p
	}
	query := func(p *Provider) ([]capability.Series, error) {
		return p.QuerySeries(context.Background(), "up",
			time.Unix(1767225600, 0), time.Unix(1767225900, 0), 15*time.Second)
	}

	t.Run("exactly 1000 series succeeds", func(t *testing.T) {
		series, err := query(providerFor(t, matrixBody(maxQuerySeries)))
		require.NoError(t, err)
		require.Len(t, series, maxQuerySeries)
	})

	t.Run("1001 series rejected", func(t *testing.T) {
		_, err := query(providerFor(t, matrixBody(maxQuerySeries+1)))
		require.Error(t, err)
		assert.True(t, errors.Is(err, capability.ErrMetricsResultTooLarge))
		var qe *capability.MetricsQueryError
		assert.NotErrorAs(t, err, &qe, "size caps are not caller query errors")
		assert.Contains(t, err.Error(), "exceeds 1000 series")
		assert.Contains(t, err.Error(), "narrow the query or the time range")
	})

	t.Run("response over 8 MiB rejected", func(t *testing.T) {
		_, err := query(providerFor(t, paddedMatrixBody(maxQueryResponseBytes+1024)))
		require.Error(t, err)
		assert.True(t, errors.Is(err, capability.ErrMetricsResultTooLarge))
		var qe *capability.MetricsQueryError
		assert.NotErrorAs(t, err, &qe, "size caps are not caller query errors")
		assert.Contains(t, err.Error(), "exceeds 8 MiB")
		assert.Contains(t, err.Error(), "narrow the query or the time range")
	})

	t.Run("response under 8 MiB succeeds", func(t *testing.T) {
		body := paddedMatrixBody(maxQueryResponseBytes - 2048)
		require.LessOrEqual(t, len(body), maxQueryResponseBytes, "fixture must stay inside the read limit")
		series, err := query(providerFor(t, body))
		require.NoError(t, err)
		require.Len(t, series, 1)
		require.Len(t, series[0].Points, 1)
	})
}

// TestQueryScopedTemplate 钉 scoped 查询落在 HTTP 面的 query 参数逐字节
// 等于固定模板（调用方无表达式可插）。
func TestQueryScopedTemplate(t *testing.T) {
	var formMu sync.Mutex
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		formMu.Lock()
		gotForm = r.Form
		formMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(matrixBody(1))
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	page, err := p.QueryScoped(context.Background(), capability.ScopedQuery{
		Metric:    capability.MetricCPUPercent,
		ProjectID: "01PROJ",
		AppID:     "01APP",
		Process:   "web",
		Start:     time.Unix(1767225600, 0),
		End:       time.Unix(1767229200, 0),
		Step:      15 * time.Second,
	})
	require.NoError(t, err)
	require.Len(t, page.Series, 1)
	assert.False(t, page.Truncated)
	assert.Equal(t, 1, page.Total)

	formMu.Lock()
	q := gotForm
	formMu.Unlock()
	want := `(fleetly_container_cpu_percent{app="01APP",process="web"}) or ` +
		`(100 * rate(container_cpu_usage_seconds_total{namespace="fleetly-01PROJ",pod=~"fleetly-01APP-web(-.*)?",container!=""}[2m]) / on(node) group_left machine_cpu_cores)`
	assert.Equal(t, want, q.Get("query"), "scoped PromQL is server-owned; the HTTP query param must be the exact template")
	assert.Equal(t, "1767225600", q.Get("start"))
	assert.Equal(t, "1767229200", q.Get("end"))
	assert.Equal(t, "15", q.Get("step"))
}

// TestQueryScopedTruncates 钉 scoped 序列上限的截断语义（60 条 → 50 +
// truncated + total=60；恰好 50 不截断），与 raw 的拒绝语义分立。
func TestQueryScopedTruncates(t *testing.T) {
	providerFor := func(t *testing.T, n int) *Provider {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(matrixBody(n))
		}))
		t.Cleanup(srv.Close)
		p := newTestProvider(t)
		p.addr = strings.TrimPrefix(srv.URL, "http://")
		return p
	}
	query := func(p *Provider) (capability.SeriesPage, error) {
		return p.QueryScoped(context.Background(), capability.ScopedQuery{
			Metric: capability.MetricMemoryWorkingSet, ProjectID: "01PROJ", AppID: "01APP",
			Start: time.Unix(1767225600, 0), End: time.Unix(1767229200, 0), Step: 15 * time.Second,
		})
	}

	t.Run("60 series truncated to 50", func(t *testing.T) {
		page, err := query(providerFor(t, capability.ScopedMetricsMaxSeries+10))
		require.NoError(t, err)
		assert.Len(t, page.Series, capability.ScopedMetricsMaxSeries)
		assert.True(t, page.Truncated)
		assert.Equal(t, capability.ScopedMetricsMaxSeries+10, page.Total)
	})

	t.Run("exactly 50 series not truncated", func(t *testing.T) {
		page, err := query(providerFor(t, capability.ScopedMetricsMaxSeries))
		require.NoError(t, err)
		assert.Len(t, page.Series, capability.ScopedMetricsMaxSeries)
		assert.False(t, page.Truncated)
		assert.Equal(t, capability.ScopedMetricsMaxSeries, page.Total)
	})

	t.Run("caller MaxSeries override", func(t *testing.T) {
		p := providerFor(t, 5)
		page, err := p.QueryScoped(context.Background(), capability.ScopedQuery{
			Metric: capability.MetricMemoryWorkingSet, ProjectID: "01PROJ", AppID: "01APP",
			Start: time.Unix(1767225600, 0), End: time.Unix(1767229200, 0), Step: 15 * time.Second,
			MaxSeries: 2,
		})
		require.NoError(t, err)
		assert.Len(t, page.Series, 2)
		assert.True(t, page.Truncated)
		assert.Equal(t, 5, page.Total)
	})

	t.Run("invalid scoped input propagates", func(t *testing.T) {
		p := providerFor(t, 1)
		_, err := p.QueryScoped(context.Background(), capability.ScopedQuery{
			Metric: "disk_full", ProjectID: "01PROJ", AppID: "01APP",
			Start: time.Unix(1767225600, 0), End: time.Unix(1767229200, 0), Step: 15 * time.Second,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown scoped metric")
	})
}

// TestQueryTimeout 钉 provider 上界超时的类型化：命中
// capability.ErrMetricsQueryTimeout；父 ctx 主动取消不落超时码（设计文档
// §5.3.3/§7）。
func TestQueryTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(matrixBody(1))
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	p.timeout = 30 * time.Millisecond
	_, err := p.QuerySeries(context.Background(), "up",
		time.Unix(1767225600, 0), time.Unix(1767225900, 0), 15*time.Second)
	require.Error(t, err)
	assert.True(t, errors.Is(err, capability.ErrMetricsQueryTimeout), "provider timeout must be typed: %v", err)
	assert.Contains(t, err.Error(), "did not answer within 30ms")

	// 父 ctx 取消：不是超时码（客户端主动放弃）。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.QuerySeries(ctx, "up", time.Unix(1767225600, 0), time.Unix(1767225900, 0), 15*time.Second)
	require.Error(t, err)
	assert.False(t, errors.Is(err, capability.ErrMetricsQueryTimeout), "client cancellation must not map to the timeout code: %v", err)
	assert.True(t, errors.Is(err, context.Canceled))
}
