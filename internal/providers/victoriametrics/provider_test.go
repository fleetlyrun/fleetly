package victoriametrics

import (
	"context"
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

	"github.com/fleetlyrun/fleetly/internal/capability"
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
// 与 matrix 响应解析（真机 ground truth 形态：Prometheus 兼容 envelope）。
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
			`{"metric":{"__name__":"container_memory_working_set_bytes","node":"01N1"},"values":[[1767225600,"1024.5"],[1767225615,"2048"]]}` +
			`]}}` + "\n"))
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	s, err := p.QuerySeries(context.Background(),
		`max(container_memory_working_set_bytes{node="01N1"})`,
		time.Unix(1767225600, 0), time.Unix(1767225900, 0), 15*time.Second)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"__name__": "container_memory_working_set_bytes", "node": "01N1"}, s.Metric)
	require.Len(t, s.Points, 2)
	assert.InDelta(t, 1024.5, s.Points[0].Value, 0.001)
	assert.InDelta(t, 2048, s.Points[1].Value, 0.001)
	assert.Equal(t, time.Unix(1767225615, 0).UTC(), s.Points[1].Time)

	formMu.Lock()
	q := gotForm
	formMu.Unlock()
	assert.Equal(t, `max(container_memory_working_set_bytes{node="01N1"})`, q.Get("query"))
	assert.Equal(t, "1767225600", q.Get("start"))
	assert.Equal(t, "1767225900", q.Get("end"))
	assert.Equal(t, "15", q.Get("step"))
}

// TestQuerySeriesError 钉 VM 错误信封上抛（status=error + error 文本）。
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
	assert.Contains(t, err.Error(), "invalid PromQL")
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
