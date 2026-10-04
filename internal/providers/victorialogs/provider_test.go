package victorialogs

import (
	"context"
	"encoding/json"
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

// newTestProvider 构造带临时数据根的 Provider。
func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New("10.0.0.1:9428", t.TempDir(), 30)
	require.NoError(t, err)
	return p
}

// TestCredentialPersistRoundTrip 钉 ADR-0040 决策 1：同 dataRoot 二次构造
// 同密码（幂等持久）；文件权限 0o600；缺 dataRoot 拒绝；损坏文件拒绝而
// 非静默再生成。
func TestCredentialPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:9428", dir, 30)
	require.NoError(t, err)
	p2, err := New("10.0.0.1:9428", dir, 30)
	require.NoError(t, err)
	assert.Equal(t, p1.cred.Secret, p2.cred.Secret, "credential must persist across constructions")

	info, err := os.Stat(filepath.Join(dir, "keys", credentialFile))
	require.NoError(t, err)
	if runtime.GOOS != "windows" { // POSIX 权限位（Windows 无此语义）
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "credential file must be owner-only")
	}

	_, err = New("10.0.0.1:9428", "", 30)
	require.Error(t, err, "empty data root must be rejected")
	_, err = New("", dir, 30)
	require.Error(t, err, "empty addr must be rejected")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "keys", credentialFile), []byte("{"), 0o600))
	_, err = New("10.0.0.1:9428", dir, 30)
	require.Error(t, err, "malformed credential must fail loudly")
}

// TestManagedWorkloadShape 钉受管形态（ADR-0040 决策 1）：钉版镜像、绝对
// 路径入口、retention 旗标、密码 file:// 材料通道（密码绝不进 argv）、
// 9428 发布、数据卷、60s 停止宽限、系统隔离域。
func TestManagedWorkloadShape(t *testing.T) {
	p := newTestProvider(t)
	ws := p.ManagedWorkloads()
	require.Len(t, ws, 1)
	w := ws[0]
	assert.Equal(t, "victoriametrics/victoria-logs:v1.52.0", w.Image, "image must be pinned")
	assert.Equal(t, []capability.WorkloadPort{{Port: 9428, Protocol: capability.ProtocolHTTP}}, w.Ports)
	assert.Equal(t, []capability.PortPublish{{PublishedPort: 9428, TargetPort: 9428}}, w.Publish)
	assert.Equal(t, 60*time.Second, w.StopGrace)
	require.Len(t, w.Volumes, 1)
	assert.Equal(t, volumeID, w.Volumes[0].VolumeID)
	assert.Equal(t, storageRoot, w.Volumes[0].Target)
	require.NotEmpty(t, w.Command)
	assert.True(t, strings.HasPrefix(w.Command[0], "/"), "entry must be an absolute path (zot lesson)")
	joined := strings.Join(w.Command, " ")
	assert.Contains(t, joined, "-retentionPeriod=30d")
	assert.Contains(t, joined, "-httpAuth.password=file:///run/secrets/"+authFile)
	assert.NotContains(t, joined, p.cred.Secret, "password must never appear in argv (cluster-inspectable)")
	assert.Equal(t, capability.NamespaceRef{Team: "fleetly", Project: "system", App: "logging"}, p.ManagedNamespace())
}

// TestRetentionVariants 钉保留窗旗标随构造参数进 argv（变更 = 指纹变更 =
// 一次受管滚动）。
func TestRetentionVariants(t *testing.T) {
	for _, days := range []int64{7, 90} {
		p, err := New("10.0.0.1:9428", t.TempDir(), days)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(p.ManagedWorkloads()[0].Command, " "), fmt.Sprintf("-retentionPeriod=%dd", days))
	}
	// 非正值回退工厂缺省 30。
	p, err := New("10.0.0.1:9428", t.TempDir(), 0)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(p.ManagedWorkloads()[0].Command, " "), "-retentionPeriod=30d")
}

// TestMaterialsStableAcrossRestarts 钉 E28/决策 1：材料字节跨构造稳定
// （P7 纪律：受管载体指纹不随进程重启漂移）。
func TestMaterialsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	p1, err := New("10.0.0.1:9428", dir, 30)
	require.NoError(t, err)
	first := p1.ManagedMaterials()
	for i := 0; i < 100; i++ {
		p2, err := New("10.0.0.1:9428", dir, 30)
		require.NoError(t, err)
		m := p2.ManagedMaterials()
		require.Len(t, m.SecretFiles, 1)
		assert.Equal(t, string(first.SecretFiles[authFile]), string(m.SecretFiles[authFile]))
	}
	assert.Equal(t, p1.cred.Secret, string(first.SecretFiles[authFile]), "material is the raw password (no newline)")
	assert.NotContains(t, string(first.SecretFiles[authFile]), "\n")
}

// TestBuildLogSQL 钉 LogsQL 构造：域过滤 + WorkloadID 过滤 + 文本过滤器
// （_msg:~ 形态——管道形态被 VL 拒，真机实证 2026-10-04）；{} 只收流字段
// （fleetly_team 是行字段——进 {} 恒空集不报错，不参与构造）；字符串字面量
// 转义（\ 与 "）；文本子串语义（正则元字符转义）。
func TestBuildLogSQL(t *testing.T) {
	q := capability.LogQuery{
		Namespace:  capability.NamespaceRef{Team: "t1", Project: "P1", App: "app1"},
		WorkloadID: "W1",
		Text:       "err.or",
	}
	assert.Equal(t, `{fleetly_project="P1",fleetly_app="app1",fleetly_workload="W1"} _msg:~"err\.or"`, buildLogSQL(q))

	q.Text = `he said "hi"\done`
	assert.Equal(t, `{fleetly_project="P1",fleetly_app="app1",fleetly_workload="W1"} _msg:~"he said \"hi\"\\done"`, buildLogSQL(q))

	// 值内引号与反斜杠的字面量转义。
	q2 := capability.LogQuery{Namespace: capability.NamespaceRef{Project: `a"b\c`}, Text: "x"}
	assert.Contains(t, buildLogSQL(q2), `fleetly_project="a\"b\\c"`)
}

// TestVLLineFieldNames 钉 Ingest 行形态的字段名（json tag 与 field* 常量
// 一字不差——Ingest 写入与 LogsQL 过滤共用词汇）。
func TestVLLineFieldNames(t *testing.T) {
	b, err := json.Marshal(vlLine{})
	require.NoError(t, err)
	for _, name := range []string{fieldTeam, fieldProject, fieldApp, fieldWorkload, fieldTask, fieldNode, fieldKind, fieldBuild} {
		assert.Contains(t, string(b), `"`+name+`"`, "field %s must be in the ingest line shape", name)
	}
}

// TestIngestRequestShape 钉 Ingest 请求形态：POST /insert/jsonline 带
// _stream_fields；basic auth；ndjson 体（帧域字段平铺）。
func TestIngestRequestShape(t *testing.T) {
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
	ts := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	err := p.Ingest(context.Background(), []capability.LogFrame{{
		WorkloadID: "W1", Container: "task1", Node: "n1", Time: ts, Line: []byte("hello"),
		Team: "t1", Project: "P1", App: "app1", Kind: capability.LogKindRuntime,
	}})
	require.NoError(t, err)
	assert.Contains(t, gotPath, "/insert/jsonline")
	assert.Contains(t, gotPath, "_stream_fields="+fieldProject)
	assert.Equal(t, "application/stream+json", gotCT)
	assert.True(t, strings.HasPrefix(gotAuth, "Basic "), "basic auth must be set")
	lines := strings.Split(strings.TrimRight(string(gotBody), "\n"), "\n")
	require.Len(t, lines, 1)
	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &decoded))
	assert.Equal(t, "hello", decoded["_msg"])
	assert.Equal(t, "P1", decoded[fieldProject])
	assert.Equal(t, capability.LogKindRuntime, decoded[fieldKind])
	assert.Equal(t, ts.Format(time.RFC3339Nano), decoded["_time"])

	require.NoError(t, p.Ingest(context.Background(), nil), "empty batch is a no-op")
}

// TestIngestRejected 钉非 2xx 上抛（游标不推进——engine 断流自愈锚）。
func TestIngestRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("maintenance"))
	}))
	defer srv.Close()
	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	err := p.Ingest(context.Background(), []capability.LogFrame{{Line: []byte("x")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
	assert.Contains(t, err.Error(), "maintenance")
}

// collectWriter 收集帧的测试 writer（并发安全——tail 流在独立 goroutine
// 写、测试主体读）。
type collectWriter struct {
	mu     sync.Mutex
	frames []capability.LogFrame
}

func (c *collectWriter) WriteLog(_ context.Context, f capability.LogFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, f)
	return nil
}

func (c *collectWriter) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

// TestQueryBuildFilter 钉 build 域回读过滤锚：Source → fleetly_build 流
// 过滤；text 缺席 = 无管道（build 回读是全量读，ADR-0040 决策 3）。
func TestQueryBuildFilter(t *testing.T) {
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: "t1", Project: "P1"},
		Source:    "01JD0BUILD0000000000000000C1",
	}
	assert.Equal(t,
		`{fleetly_project="P1",fleetly_build="01JD0BUILD0000000000000000C1"}`,
		buildLogSQL(q))
}

// TestQueryRoundTrip 钉检索请求形态（query/start/end/limit form 字段）与
// 响应解析——夹具 = staging 真机 ground truth 形态（平铺字段 + `_stream`
// 字符串 + `_stream_id` 附加面，2026-10-04 实录）、升序输出。
func TestQueryRoundTrip(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		gotForm = r.Form
		w.Header().Set("Content-Type", "application/stream+json; charset=utf-8")
		// 乱序返回（倒序）——断言实现侧统一升序。
		_, _ = w.Write([]byte(
			`{"_msg":"third","_stream":"{fleetly_app=\"A\",fleetly_task=\"t3\"}","_stream_id":"00000000000000004052ddcf1691e8128","_time":"2026-10-04T12:00:03.466359027Z","fleetly_workload":"W1","fleetly_task":"t3","fleetly_node":"n2","fleetly_project":"P1","fleetly_kind":"runtime"}` + "\n" +
				`{"_msg":"first","_stream":"{fleetly_app=\"A\",fleetly_task=\"t1\"}","_stream_id":"00000000000000000df346141b478a812","_time":"2026-10-04T12:00:01.5Z","fleetly_workload":"W1","fleetly_task":"t1","fleetly_kind":"runtime","fleetly_node":"n1"}` + "\n" +
				"\n" + // 空行容忍
				`{"_msg":"second","_stream_id":"00000000000000000df346141b478a813","_time":"2026-10-04T12:00:02Z","fleetly_workload":"W1","fleetly_task":"t2","fleetly_kind":"build","fleetly_build":"B1"}` + "\n"))
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	var cw collectWriter
	err := p.Query(context.Background(), capability.LogQuery{
		Namespace:  capability.NamespaceRef{Project: "P1", App: "app1"},
		WorkloadID: "W1",
		Text:       "irrelevant-server-side",
		Since:      time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC),
		Until:      time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC),
		TailLines:  50,
	}, &cw)
	require.NoError(t, err)
	require.Len(t, cw.frames, 3)
	assert.Equal(t, "first", string(cw.frames[0].Line))
	assert.Equal(t, "second", string(cw.frames[1].Line))
	assert.Equal(t, "third", string(cw.frames[2].Line))
	// 平铺形态解析（_stream 字符串附加面被忽略、不碍解码）。
	assert.Equal(t, "t3", cw.frames[2].Container)
	assert.Equal(t, "n2", cw.frames[2].Node)
	assert.Equal(t, "t1", cw.frames[0].Container)
	assert.Equal(t, "W1", cw.frames[0].WorkloadID)
	// 纳秒 _time 完整往返。
	assert.Equal(t, "2026-10-04T12:00:03.466359027Z", cw.frames[2].Time.UTC().Format(time.RFC3339Nano))
	// build 域归因。
	assert.Equal(t, "B1", cw.frames[1].Source)
	assert.Equal(t, capability.LogKindBuild, cw.frames[1].Kind)

	// form 断言（检索请求契约面——过滤器位 _msg:~ 形态，真机实证）。
	assert.Contains(t, gotForm.Get("query"), `fleetly_project="P1"`)
	assert.Contains(t, gotForm.Get("query"), `fleetly_workload="W1"`)
	assert.Contains(t, gotForm.Get("query"), ` _msg:~"`)
	assert.Equal(t, "50", gotForm.Get("limit"))
	assert.Equal(t, "2026-10-04T11:00:00Z", gotForm.Get("start"))
	assert.Equal(t, "2026-10-04T13:00:00Z", gotForm.Get("end"))
}

// TestTailStream 钉 tail 端点：start_offset 回填窗（Since 距今秒数）、
// 流式逐帧写出、ctx 取消干净收口。
func TestTailStream(t *testing.T) {
	var formMu sync.Mutex
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		formMu.Lock()
		gotForm = r.Form
		formMu.Unlock()
		w.Header().Set("Content-Type", "application/stream+json; charset=utf-8")
		_, _ = w.Write([]byte(`{"_time":"2026-10-04T12:00:01Z","_msg":"live1","fleetly_workload":"W1","fleetly_kind":"runtime"}` + "\n"))
		_, _ = w.Write([]byte(`{"_time":"2026-10-04T12:00:02Z","_msg":"live2","fleetly_workload":"W1","fleetly_kind":"runtime"}` + "\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // ndjson 流式语义：写即推（否则 httptest 缓冲到 handler 返回）
		}
		<-r.Context().Done() // 模拟持续尾随流（客户端取消时服务端收口）
	}))
	defer srv.Close()

	p := newTestProvider(t)
	p.addr = strings.TrimPrefix(srv.URL, "http://")
	var cw collectWriter
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = p.Query(ctx, capability.LogQuery{
			Namespace: capability.NamespaceRef{Project: "P1"},
			Text:      "err", Follow: true,
			Since: time.Now().Add(-90 * time.Minute),
		}, &cw)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && cw.len() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	cw.mu.Lock()
	frames := append([]capability.LogFrame(nil), cw.frames...)
	cw.mu.Unlock()
	require.Len(t, frames, 2, "tail must stream frames until cancel")
	assert.Equal(t, "live1", string(frames[0].Line))
	formMu.Lock()
	offset := gotForm.Get("start_offset")
	formMu.Unlock()
	assert.Contains(t, offset, "s")
	assert.GreaterOrEqual(t, parseOffsetSeconds(t, offset), int64(5300), "backfill window ≈ now-since (90m)")
}

func parseOffsetSeconds(t *testing.T, s string) int64 {
	t.Helper()
	require.True(t, strings.HasSuffix(s, "s"))
	var v int64
	_, err := fmt.Sscanf(s, "%ds", &v)
	require.NoError(t, err)
	return v
}

// TestHealthProbe 钉 Health 的 TCP 探测（起服健康/不可达 unhealthy）。
func TestHealthProbe(t *testing.T) {
	p := newTestProvider(t)
	report := p.Health(context.Background())
	assert.False(t, report.Healthy)

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
