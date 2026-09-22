package metrics

// VM HTTP 消费面单测（httptest 假后端）：PromQL 透传形态、规范化投影、
// 错误面三分（后端不可达 / 坏查询 / 未启用哨兵）、瞬时计数（nodes_
// reporting 实现面）、健康拨测。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSearchPassthroughAndNormalization 区间查询：query 原样透传（操作员
// 工具——不做任何改写/沙箱）、start/end/step 参数化、序列 label 集与点集
// 规范化投影。
func TestSearchPassthroughAndNormalization(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[` +
			`{"metric":{"__name__":"up","job":"fleetly-cadvisor","instance":"127.0.0.1:8080"},` +
			`"values":[[1700000000,"1"],[1700000060,"1"]]},` +
			`{"metric":{"__name__":"container_memory_usage_bytes","name":"app_web.1.abc"},` +
			`"values":[[1700000000,"1048576"]]}]}}`))
	}))
	defer srv.Close()
	b := NewBackendWithBase(srv.URL)

	start := time.Unix(1699999900, 0)
	end := time.Unix(1700000100, 0)
	promql := `sum by (container_label_com_docker_swarm_service_name) (rate(container_cpu_time_seconds_total{job="fleetly-cadvisor"}[5m]))`
	series, err := b.Search(context.Background(), RangeQuery{
		Query: promql, Start: start, End: end, StepSeconds: 60,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotPath != QueryRangePath {
		t.Fatalf("path = %s, want %s", gotPath, QueryRangePath)
	}
	// 透传：服务端收到的 query 与输入逐字节一致。
	if gotQuery != promql {
		t.Fatalf("query passthrough broken:\n got %q\nwant %q", gotQuery, promql)
	}
	if len(series) != 2 {
		t.Fatalf("series = %d, want 2", len(series))
	}
	if series[0].Metric["job"] != "fleetly-cadvisor" || series[0].Metric["instance"] != "127.0.0.1:8080" {
		t.Fatalf("labels = %+v", series[0].Metric)
	}
	if len(series[0].Points) != 2 || series[0].Points[0].T != 1700000000 || series[0].Points[0].V != 1 {
		t.Fatalf("points = %+v", series[0].Points)
	}
	if series[1].Points[0].V != 1048576 {
		t.Fatalf("memory point = %+v", series[1].Points[0])
	}
}

// TestSearchLimitCap 序列截断：响应超 limit 时规范化面截断（诚实截断面，
// 不静默丢弃到未知）。
func TestSearchLimitCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[` +
			`{"metric":{"a":"1"},"values":[[1,"1"]]},` +
			`{"metric":{"a":"2"},"values":[[1,"1"]]}]}}`))
	}))
	defer srv.Close()
	series, err := NewBackendWithBase(srv.URL).Search(context.Background(), RangeQuery{Query: "up", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("series = %d, want 1 (capped)", len(series))
	}
}

// TestSearchErrorFaces 错误面三分：VM 不可达 → ErrBackendUnavailable；
// 4xx / status=error → ErrBadQuery（VM 原文透传）；传输中断 → 同不可达哨兵。
func TestSearchErrorFaces(t *testing.T) {
	// 不可达：回环拨一个必然关闭的端口。
	b := NewBackendWithBase("http://127.0.0.1:1")
	_, err := b.Search(context.Background(), RangeQuery{Query: "up"})
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("unreachable err = %v, want ErrBackendUnavailable", err)
	}

	// 4xx（坏 PromQL）。
	badQ := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error at char 5"}`))
	}))
	defer badQ.Close()
	_, err = NewBackendWithBase(badQ.URL).Search(context.Background(), RangeQuery{Query: "up{"})
	if !errors.Is(err, ErrBadQuery) || !strings.Contains(err.Error(), "parse error at char 5") {
		t.Fatalf("bad query err = %v, want ErrBadQuery with VM original text", err)
	}

	// 5xx。
	badS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badS.Close()
	_, err = NewBackendWithBase(badS.URL).Search(context.Background(), RangeQuery{Query: "up"})
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("5xx err = %v, want ErrBackendUnavailable", err)
	}

	// status=success 以外的 200 信封。
	badE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","error":"execution timeout"}`))
	}))
	defer badE.Close()
	_, err = NewBackendWithBase(badE.URL).Search(context.Background(), RangeQuery{Query: "up"})
	if !errors.Is(err, ErrBadQuery) || !strings.Contains(err.Error(), "execution timeout") {
		t.Fatalf("error-envelope err = %v, want ErrBadQuery", err)
	}
}

// TestCountInstant 瞬时计数（nodes_reporting 实现面）：标量型 count 查询
// 取首序列首点；空集 = 0；错误面同区间查询。
func TestCountInstant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != QueryInstantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, QueryInstantPath)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{},"value":[1700000000,"1"]}]}}`))
	}))
	defer srv.Close()
	n, err := NewBackendWithBase(srv.URL).CountInstant(context.Background(), `count(up{job="fleetly-cadvisor"} == 1)`)
	if err != nil || n != 1 {
		t.Fatalf("CountInstant = %d, %v; want 1, nil", n, err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer empty.Close()
	n, err = NewBackendWithBase(empty.URL).CountInstant(context.Background(), `count(up)`)
	if err != nil || n != 0 {
		t.Fatalf("CountInstant(empty) = %d, %v; want 0, nil", n, err)
	}
}

// TestPing 健康拨测：200 = nil；不可达 = ErrBackendUnavailable。
func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("OK"))
	}))
	defer srv.Close()
	if err := NewBackendWithBase(srv.URL).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := NewBackendWithBase("http://127.0.0.1:1").Ping(context.Background()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Ping(unreachable) = %v, want ErrBackendUnavailable", err)
	}
}

// TestErrMetricsNotEnabledSentinel 哨兵存在性（api 层映射
// E_METRICS_NOT_ENABLED 的语义锚——值即文档）。
func TestErrMetricsNotEnabledSentinel(t *testing.T) {
	if !strings.Contains(ErrMetricsNotEnabled.Error(), "opt-in") {
		t.Fatalf("sentinel text = %q", ErrMetricsNotEnabled.Error())
	}
}
