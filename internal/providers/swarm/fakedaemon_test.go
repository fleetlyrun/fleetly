package swarm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// fakeDaemon 是 hermetic 的最小 docker API 面（http.RoundTripper 形态，
// C19 测试底座）：Ensure/pollTasks/DescribeCluster 直连 cli 的路径没有函
// 数缝，传输层替换是唯一的 hermetic 通路——moby client 以
// WithHTTPClient/WithAPIVersion 注入（钉死版本跳过协商的 /_ping 前置）。
// 按 method+path 路由（/vX.Y 前缀剥离），带服务/任务/网络/节点的内存存
// 储、调用计数与按次故障注入。
type fakeDaemon struct {
	mu      sync.Mutex
	store   map[string]swarm.Service // 服务存储（键 = Spec.Name，ID = "srv-"+名）
	secrets map[string]swarm.Secret  // Secret 存储（键 = Spec.Name，ID = "sec-"+名；孤儿清扫面 E29）
	tasks   map[string][]swarm.Task  // service ID → 任务快照
	nets    map[string]string        // 网络载体名 → 网络 ID
	nodes   []swarm.Node             // 节点表（NodeUpdate 原地改写）
	mutate  func(*swarm.ServiceSpec) // 服务端物化模拟（daemon 版本漂移形态）
	fail    map[string]int           // 路由键 → 剩余故障次数（500）
	counts  map[string]int           // 路由键 → 调用次数
	keys    []string                 // 路由键序列（量级断言）
	qs      map[string]url.Values    // 路由键 → 最近一次请求的 query（过滤断言）
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		store:   map[string]swarm.Service{},
		secrets: map[string]swarm.Secret{},
		tasks:   map[string][]swarm.Task{},
		nets:    map[string]string{},
		fail:    map[string]int{},
		counts:  map[string]int{},
		qs:      map[string]url.Values{},
	}
}

// newClient 构造注入 fake 传输层的 moby client。
func (f *fakeDaemon) newClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(
		client.WithHTTPClient(&http.Client{Transport: f}),
		client.WithAPIVersion("1.52"),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func (f *fakeDaemon) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[key]
}

func (f *fakeDaemon) queriesOf(key string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]url.Values, 0, f.counts[key])
	for _, k := range f.keys {
		if k == key {
			out = append(out, f.qs[key])
		}
	}
	return out
}

// failNext 注入指定路由接下来 n 次的 500 故障。
func (f *fakeDaemon) failNext(key string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[key] = n
}

// addService 预置存量服务（Ensure 幂等/update 路径的"已存在"形态）。
func (f *fakeDaemon) addService(spec swarm.ServiceSpec) swarm.Service {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc := swarm.Service{ID: "srv-" + spec.Name, Spec: spec}
	if f.mutate != nil {
		f.mutate(&svc.Spec)
	}
	f.store[spec.Name] = svc
	return svc
}

// addSecret 预置存量 Secret 载体（孤儿清扫面的现役/孤儿/宽限窗形态）。
func (f *fakeDaemon) addSecret(sec swarm.Secret) swarm.Secret {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sec.ID == "" {
		sec.ID = "sec-" + sec.Spec.Name
	}
	f.secrets[sec.Spec.Name] = sec
	return sec
}

// secretCount 返回存量 Secret 数（清扫结果断言）。
func (f *fakeDaemon) secretCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.secrets)
}

// RoundTrip 实现 http.RoundTripper：按 method+path 路由（版本前缀剥离）。
func (f *fakeDaemon) RoundTrip(r *http.Request) (*http.Response, error) {
	p := r.URL.Path
	if strings.HasPrefix(p, "/v") {
		if i := strings.IndexByte(p[1:], '/'); i >= 0 {
			p = p[i+1:]
		}
	}
	key := r.Method + " " + p
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[key]++
	f.keys = append(f.keys, key)
	f.qs[key] = r.URL.Query()
	status, body, known := f.routeLocked(r, key)
	if !known {
		status = http.StatusNotFound
		body = map[string]string{"message": "fake daemon: no route for " + key}
	}
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.WriteHeader(status)
	_ = json.NewEncoder(rec).Encode(body)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

// routeLocked 分发到各端点模拟（调用方已持锁）。
func (f *fakeDaemon) routeLocked(r *http.Request, key string) (status int, body any, known bool) {
	if n := f.fail[key]; n > 0 {
		f.fail[key] = n - 1
		return http.StatusInternalServerError, map[string]string{"message": "injected fake daemon failure"}, true
	}
	switch {
	case key == "GET /services":
		items := make([]swarm.Service, 0, len(f.store))
		for _, svc := range f.store {
			items = append(items, svc)
		}
		items = filterServices(items, decodeFilters(r.URL.Query()))
		sort.Slice(items, func(i, j int) bool { return items[i].Spec.Name < items[j].Spec.Name })
		return http.StatusOK, items, true
	case key == "POST /services/create":
		var spec swarm.ServiceSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			return http.StatusBadRequest, map[string]string{"message": err.Error()}, true
		}
		if f.mutate != nil {
			f.mutate(&spec)
		}
		f.store[spec.Name] = swarm.Service{ID: "srv-" + spec.Name, Spec: spec}
		return http.StatusOK, swarm.ServiceCreateResponse{ID: "srv-" + spec.Name}, true
	case strings.HasPrefix(key, "GET /services/"):
		ref := strings.TrimPrefix(key, "GET /services/")
		if svc, ok := f.byRef(ref); ok {
			return http.StatusOK, svc, true
		}
		return http.StatusNotFound, map[string]string{"message": fmt.Sprintf("service %s not found", ref)}, true
	case strings.HasPrefix(key, "POST /services/") && strings.HasSuffix(key, "/update"):
		ref := strings.TrimSuffix(strings.TrimPrefix(key, "POST /services/"), "/update")
		svc, ok := f.byRef(ref)
		if !ok {
			return http.StatusNotFound, map[string]string{"message": fmt.Sprintf("service %s not found", ref)}, true
		}
		var spec swarm.ServiceSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			return http.StatusBadRequest, map[string]string{"message": err.Error()}, true
		}
		if f.mutate != nil {
			f.mutate(&spec)
		}
		svc.Spec = spec
		f.store[svc.Spec.Name] = svc
		return http.StatusOK, swarm.ServiceUpdateResponse{}, true
	case strings.HasPrefix(key, "DELETE /services/"):
		ref := strings.TrimPrefix(key, "DELETE /services/")
		if svc, ok := f.byRef(ref); ok {
			delete(f.store, svc.Spec.Name)
		}
		return http.StatusOK, map[string]string{}, true
	case key == "GET /secrets":
		items := make([]swarm.Secret, 0, len(f.secrets))
		for _, sec := range f.secrets {
			items = append(items, sec)
		}
		items = filterSecrets(items, decodeFilters(r.URL.Query()))
		sort.Slice(items, func(i, j int) bool { return items[i].Spec.Name < items[j].Spec.Name })
		return http.StatusOK, items, true
	case strings.HasPrefix(key, "DELETE /secrets/"):
		ref := strings.TrimPrefix(key, "DELETE /secrets/")
		for name, sec := range f.secrets {
			if name == ref || sec.ID == ref {
				delete(f.secrets, name)
				return http.StatusOK, map[string]string{}, true
			}
		}
		return http.StatusNotFound, map[string]string{"message": fmt.Sprintf("secret %s not found", ref)}, true
	case key == "GET /tasks":
		filters := decodeFilters(r.URL.Query())
		items := make([]swarm.Task, 0)
		for sid, list := range f.tasks {
			if ids := filters["service"]; len(ids) > 0 && !ids[sid] {
				continue
			}
			items = append(items, list...)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		return http.StatusOK, items, true
	case strings.HasPrefix(key, "GET /networks/"):
		name := strings.TrimPrefix(key, "GET /networks/")
		if id, ok := f.nets[name]; ok {
			return http.StatusOK, network.Inspect{Network: network.Network{Name: name, ID: id}}, true
		}
		return http.StatusNotFound, map[string]string{"message": fmt.Sprintf("network %s not found", name)}, true
	case key == "GET /nodes":
		return http.StatusOK, f.nodes, true
	case strings.HasPrefix(key, "POST /nodes/") && strings.HasSuffix(key, "/update"):
		id := strings.TrimSuffix(strings.TrimPrefix(key, "POST /nodes/"), "/update")
		for i := range f.nodes {
			if f.nodes[i].ID != id {
				continue
			}
			var spec swarm.NodeSpec
			if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
				return http.StatusBadRequest, map[string]string{"message": err.Error()}, true
			}
			f.nodes[i].Spec = spec
			f.nodes[i].Version.Index++
			return http.StatusOK, map[string]string{}, true
		}
		return http.StatusNotFound, map[string]string{"message": fmt.Sprintf("node %s not found", id)}, true
	case strings.HasPrefix(key, "GET /nodes/"):
		id := strings.TrimPrefix(key, "GET /nodes/")
		for _, n := range f.nodes {
			if n.ID == id {
				return http.StatusOK, n, true
			}
		}
		return http.StatusNotFound, map[string]string{"message": fmt.Sprintf("node %s not found", id)}, true
	default:
		return 0, nil, false
	}
}

// byRef 按名字或 ID 查服务（inspect 用名、update/remove 用 ID 的双形态）。
func (f *fakeDaemon) byRef(ref string) (swarm.Service, bool) {
	if svc, ok := f.store[ref]; ok {
		return svc, true
	}
	for _, svc := range f.store {
		if svc.ID == ref {
			return svc, true
		}
	}
	return swarm.Service{}, false
}

// decodeFilters 还原 filters 查询参数（JSON 形态 map[term]map[value]bool）。
func decodeFilters(q url.Values) map[string]map[string]bool {
	raw := q.Get("filters")
	if raw == "" {
		return nil
	}
	var f map[string]map[string]bool
	_ = json.Unmarshal([]byte(raw), &f)
	return f
}

// filterSecrets 按 label 全等过滤 Secret（filterServices 的 Secret 面孪生：
// 真 daemon 的服务端过滤语义，fake 只实现测试用到的 label 项）。
func filterSecrets(items []swarm.Secret, filters map[string]map[string]bool) []swarm.Secret {
	labels := filters["label"]
	if len(labels) == 0 {
		return items
	}
	out := items[:0]
	for _, sec := range items {
		match := true
		for kv := range labels {
			k, v, _ := strings.Cut(kv, "=")
			if sec.Spec.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, sec)
		}
	}
	return out
}

// filterServices 按 label 全等过滤（真 daemon 的服务端过滤语义，fake 只
// 实现测试用到的 label 项）。
func filterServices(items []swarm.Service, filters map[string]map[string]bool) []swarm.Service {
	labels := filters["label"]
	if len(labels) == 0 {
		return items
	}
	out := items[:0]
	for _, svc := range items {
		match := true
		for kv := range labels {
			k, v, _ := strings.Cut(kv, "=")
			if svc.Spec.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, svc)
		}
	}
	return out
}
