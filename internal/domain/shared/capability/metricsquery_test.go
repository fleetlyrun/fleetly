package capability

// scoped 指标查询纯函数单测（H8 阶段 B，设计文档 §5.3.2/§5.3.3/§7）：
// 固定模板逐字节 golden（swarm 词汇臂 + k3s raw 臂 `or` 合成），时间窗/
// step 钳制边界，非法输入拒绝。

import (
	"math"
	"strings"
	"testing"
	"time"
)

func appQuery(metric string) ScopedQuery {
	return ScopedQuery{
		Metric:    metric,
		ProjectID: "01PROJ",
		AppID:     "01APP",
		Process:   "web",
		Start:     time.Unix(1767225600, 0).UTC(),
		End:       time.Unix(1767229200, 0).UTC(),
		Step:      15 * time.Second,
	}
}

func databaseQuery(metric string) ScopedQuery {
	return ScopedQuery{
		Metric:     metric,
		ProjectID:  "01PROJ",
		DatabaseID: "01DB",
		Start:      time.Unix(1767225600, 0).UTC(),
		End:        time.Unix(1767229200, 0).UTC(),
		Step:       15 * time.Second,
	}
}

// projectQuery 是 project scope 形态：AppID/DatabaseID 均空，ProjectID 即
// 作用域（词汇臂不覆盖 project 维度，双臂都走 raw——见 BuildScopedPromQL
// 注释）。
func projectQuery(metric string) ScopedQuery {
	return ScopedQuery{
		Metric:    metric,
		ProjectID: "01PROJ",
		Start:     time.Unix(1767225600, 0).UTC(),
		End:       time.Unix(1767229200, 0).UTC(),
		Step:      15 * time.Second,
	}
}

// TestBuildScopedPromQL 钉死每个模板形状（app/database/project × 指标 × 双
// runtime 臂共 13 条精确字符串）与 process 可选面。
func TestBuildScopedPromQL(t *testing.T) {
	const (
		appSelector = `{namespace="fleetly-01PROJ",pod=~"fleetly-01APP-web(-.*)?",container!=""}`
		appNoProc   = `{namespace="fleetly-01PROJ",pod=~"fleetly-01APP-.*",container!=""}`
		dbSelector  = `{namespace="fleetly-01PROJ",pod=~"fleetly-db-01DB(-.*)?",container!=""}`
		// project scope 双臂都是 raw：swarm 臂按写侧 cAdvisor 投影标签
		// container_label_fleetly_ns_project 收敛（词汇标签集没有 project
		// 维度，ADR-0065 标签集是注册表），k3s 臂按 namespace 收敛。
		projSwarmSelector = `{container_label_fleetly_ns_project="01PROJ",container!=""}`
		projK3sSelector   = `{namespace="fleetly-01PROJ",container!=""}`
	)
	tests := []struct {
		name string
		q    ScopedQuery
		want string
	}{
		{
			name: "app cpu_percent with process",
			q:    appQuery(MetricCPUPercent),
			want: `(fleetly_container_cpu_percent{app="01APP",process="web"}) or (100 * rate(container_cpu_usage_seconds_total` + appSelector + `[2m]) / on(node) group_left machine_cpu_cores)`,
		},
		{
			name: "app cpu_cores with process",
			q:    appQuery(MetricCPUCores),
			want: `(fleetly_container_cpu_cores{app="01APP",process="web"}) or (rate(container_cpu_usage_seconds_total` + appSelector + `[2m]))`,
		},
		{
			name: "app memory with process",
			q:    appQuery(MetricMemoryWorkingSet),
			want: `(fleetly_container_memory_working_set_bytes{app="01APP",process="web"}) or (container_memory_working_set_bytes` + appSelector + `)`,
		},
		{
			name: "app cpu_percent without process",
			q: func() ScopedQuery {
				q := appQuery(MetricCPUPercent)
				q.Process = ""
				return q
			}(),
			want: `(fleetly_container_cpu_percent{app="01APP"}) or (100 * rate(container_cpu_usage_seconds_total` + appNoProc + `[2m]) / on(node) group_left machine_cpu_cores)`,
		},
		{
			name: "app cpu_cores without process",
			q: func() ScopedQuery {
				q := appQuery(MetricCPUCores)
				q.Process = ""
				return q
			}(),
			want: `(fleetly_container_cpu_cores{app="01APP"}) or (rate(container_cpu_usage_seconds_total` + appNoProc + `[2m]))`,
		},
		{
			name: "app memory without process",
			q: func() ScopedQuery {
				q := appQuery(MetricMemoryWorkingSet)
				q.Process = ""
				return q
			}(),
			want: `(fleetly_container_memory_working_set_bytes{app="01APP"}) or (container_memory_working_set_bytes` + appNoProc + `)`,
		},
		{
			name: "database cpu_cores",
			q:    databaseQuery(MetricCPUCores),
			want: `(fleetly_container_cpu_cores{database="01DB"}) or (rate(container_cpu_usage_seconds_total` + dbSelector + `[2m]))`,
		},
		{
			name: "database memory",
			q:    databaseQuery(MetricMemoryWorkingSet),
			want: `(fleetly_container_memory_working_set_bytes{database="01DB"}) or (container_memory_working_set_bytes` + dbSelector + `)`,
		},
		{
			name: "project cpu_cores",
			q:    projectQuery(MetricCPUCores),
			want: `(rate(container_cpu_usage_seconds_total` + projSwarmSelector + `[2m])) or (rate(container_cpu_usage_seconds_total` + projK3sSelector + `[2m]))`,
		},
		{
			name: "project memory",
			q:    projectQuery(MetricMemoryWorkingSet),
			want: `(container_memory_working_set_bytes` + projSwarmSelector + `) or (container_memory_working_set_bytes` + projK3sSelector + `)`,
		},
		{
			name: "project cpu_cores rate window follows step",
			q: func() ScopedQuery {
				q := projectQuery(MetricCPUCores)
				q.Step = 90 * time.Second
				return q
			}(),
			want: `(rate(container_cpu_usage_seconds_total` + projSwarmSelector + `[6m])) or (rate(container_cpu_usage_seconds_total` + projK3sSelector + `[6m]))`,
		},
		{
			name: "app cpu_cores rate window follows step",
			q: func() ScopedQuery {
				q := appQuery(MetricCPUCores)
				q.Step = 90 * time.Second
				return q
			}(),
			want: `(fleetly_container_cpu_cores{app="01APP",process="web"}) or (rate(container_cpu_usage_seconds_total` + appSelector + `[6m]))`,
		},
		{
			name: "app cpu_cores rate window capped at one hour",
			q: func() ScopedQuery {
				q := appQuery(MetricCPUCores)
				q.Step = 3600 * time.Second
				return q
			}(),
			want: `(fleetly_container_cpu_cores{app="01APP",process="web"}) or (rate(container_cpu_usage_seconds_total` + appSelector + `[1h]))`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildScopedPromQL(tt.q)
			if err != nil {
				t.Fatalf("BuildScopedPromQL: %v", err)
			}
			if got != tt.want {
				t.Errorf("template drifted\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestBuildScopedPromQLEscapesLabelValues 钉标签值转义（引号/反斜杠）与
// 无用户表达式拼接面。
func TestBuildScopedPromQLEscapesLabelValues(t *testing.T) {
	q := ScopedQuery{
		Metric:    MetricMemoryWorkingSet,
		ProjectID: `proj"x\y`,
		AppID:     "01APP",
		Start:     time.Unix(1767225600, 0).UTC(),
		End:       time.Unix(1767229200, 0).UTC(),
		Step:      15 * time.Second,
	}
	got, err := BuildScopedPromQL(q)
	if err != nil {
		t.Fatalf("BuildScopedPromQL: %v", err)
	}
	if !strings.Contains(got, `namespace="fleetly-proj\"x\\y"`) {
		t.Errorf("namespace label value must be PromQL-escaped, got: %s", got)
	}
	if strings.Contains(got, "label_replace") || strings.Contains(got, "label_join") {
		t.Errorf("scoped template must never rewrite labels, got: %s", got)
	}
}

// TestBuildScopedPromQLRejects 钉非法输入（模板不可由调用方扩大作用域）。
func TestBuildScopedPromQLRejects(t *testing.T) {
	base := appQuery(MetricCPUCores)
	tests := []struct {
		name string
		q    ScopedQuery
		want string
	}{
		{
			name: "unknown metric",
			q:    func() ScopedQuery { q := base; q.Metric = "disk_full"; return q }(),
			want: "unknown scoped metric",
		},
		{
			name: "both scopes",
			q:    func() ScopedQuery { q := base; q.DatabaseID = "01DB"; return q }(),
			want: "exactly one",
		},
		{
			name: "no scope",
			q: func() ScopedQuery {
				q := base
				q.AppID = ""
				q.ProjectID = ""
				return q
			}(),
			want: "exactly one",
		},
		{
			name: "missing project",
			q:    func() ScopedQuery { q := base; q.ProjectID = ""; return q }(),
			want: "project id is required",
		},
		{
			name: "process on database scope",
			q:    func() ScopedQuery { q := databaseQuery(MetricCPUCores); q.Process = "web"; return q }(),
			want: "only available for app scope",
		},
		{
			name: "cpu_percent on database scope",
			q:    databaseQuery(MetricCPUPercent),
			want: "only available for app scope",
		},
		{
			name: "cpu_percent on project scope",
			q:    projectQuery(MetricCPUPercent),
			want: "only available for app scope",
		},
		{
			name: "process on project scope",
			q:    func() ScopedQuery { q := projectQuery(MetricCPUCores); q.Process = "web"; return q }(),
			want: "only available for app scope",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildScopedPromQL(tt.q)
			if err == nil {
				t.Fatalf("BuildScopedPromQL must reject %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error must mention %q, got: %v", tt.want, err)
			}
		})
	}
}

// TestClampScopedStep 钉窗口/步长钳制边界（设计文档 §7：25h→error；
// 24h+step=1→≥87s；1h+step=0→15s；step=1h→1h；点数 ≤1001）。
func TestClampScopedStep(t *testing.T) {
	start := time.Unix(1767225600, 0).UTC()
	tests := []struct {
		name      string
		start     time.Time
		end       time.Time
		requested int64
		want      time.Duration
		wantErr   string
	}{
		{name: "25h window rejected", start: start, end: start.Add(25 * time.Hour), wantErr: "24h"},
		{name: "start equals end rejected", start: start, end: start, wantErr: "start must be before end"},
		{name: "start after end rejected", start: start, end: start.Add(-time.Minute), wantErr: "start must be before end"},
		{name: "negative step rejected", start: start, end: start.Add(time.Hour), requested: -1, wantErr: "must not be negative"},
		{name: "overflowing step rejected", start: start, end: start.Add(time.Hour), requested: math.MaxInt64, wantErr: "too large"},
		{name: "24h auto step", start: start, end: start.Add(24 * time.Hour), want: 360 * time.Second},
		{name: "24h step 1 clamped to points floor", start: start, end: start.Add(24 * time.Hour), requested: 1, want: 87 * time.Second},
		{name: "1h auto step", start: start, end: start.Add(time.Hour), want: 15 * time.Second},
		{name: "1h step 1 clamped to 15s", start: start, end: start.Add(time.Hour), requested: 1, want: 15 * time.Second},
		{name: "1h step 1h kept", start: start, end: start.Add(time.Hour), requested: 3600, want: time.Hour},
		{name: "oversized step kept", start: start, end: start.Add(time.Hour), requested: 1_000_000, want: 1_000_000 * time.Second},
		{name: "sub-second window floors to 15s", start: start, end: start.Add(500 * time.Millisecond), want: 15 * time.Second},
		{name: "exactly 24h accepted", start: start, end: start.Add(24 * time.Hour), requested: 3600, want: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClampScopedStep(tt.start, tt.end, tt.requested)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ClampScopedStep must reject %s, got %s", tt.name, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error must mention %q, got: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClampScopedStep: %v", err)
			}
			if got != tt.want {
				t.Errorf("step = %s, want %s", got, tt.want)
			}
			if got%time.Second != 0 {
				t.Errorf("step must be whole seconds, got %s", got)
			}
			window := tt.end.Sub(tt.start)
			if points := int64(window/got) + 1; points > 1001 {
				t.Errorf("point count %d exceeds the 1001 cap (window %s, step %s)", points, window, got)
			}
		})
	}
}
