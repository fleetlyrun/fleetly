package logs

import (
	"bufio"
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Manager 主体：采集循环 / Follow / History / 清理（类型与构造在 logs.go）。

// stream 是采集循环内的 per (app, service) 轮询游标。
type stream struct {
	app     string
	appID   string
	service string
	lastAt  time.Time // 轮询起点游标（首轮 = 发现时刻；此后 = 末行时间）
}

// Run 是采集主循环（lynx 服务壳 Start 消费）：周期扫描 active apps 的
// 受管服务 → 逐流轮询 docker service logs（follow=false + since 游标）→
// 脱敏 → ring + 落盘 + 扇出。ctx 取消即返回。首轮前做一次落盘清理。
func (m *Manager) Run(ctx context.Context) error {
	if _, err := m.dsk.prune(ctx, time.Duration(m.cfg.RetentionDays)*24*time.Hour); err != nil {
		m.log.Warn("logs: initial prune failed", "error", err.Error())
	}
	pruneTicker := time.NewTicker(time.Hour)
	defer pruneTicker.Stop()
	ticker := time.NewTicker(m.cfg.ScanInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pruneTicker.C:
			if n, err := m.dsk.prune(ctx, time.Duration(m.cfg.RetentionDays)*24*time.Hour); err != nil {
				m.log.Warn("logs: prune failed", "error", err.Error())
			} else if n > 0 {
				m.log.Info("logs: pruned expired log files", "files", n)
			}
		case <-ticker.C:
			m.scanOnce(ctx)
		}
	}
}

// scanOnce 执行一轮扫描：发现服务集并逐流轮询拉取增量。
func (m *Manager) scanOnce(ctx context.Context) {
	apps, err := m.st.ListActiveApps(ctx)
	if err != nil {
		m.log.Warn("logs: list apps failed", "error", err.Error())
		return
	}
	for _, app := range apps {
		services, err := m.port.ManagedServiceProcesses(ctx, app.Name)
		if err != nil {
			// 底座暂态（swarm 未就绪等）：跳过本轮，下轮重试。
			m.log.Debug("logs: service discovery failed", "app", app.Name, "error", err.Error())
			continue
		}
		red := m.red.forApp(ctx, app.ID)
		for _, svc := range services {
			m.pollStream(ctx, app, svc, red)
		}
	}
}

// pollStream 拉取单条流的本轮增量并入库（ring + 落盘 + 扇出）。
func (m *Manager) pollStream(ctx context.Context, app state.App, service string, red *redactor) {
	m.mu.Lock()
	key := streamKey(app.Name, service)
	cur, ok := m.streams[key]
	if !ok {
		// 首轮游标冻结在发现时刻（不回灌历史——历史由既有落盘承载；
		// 首启无落盘则从零开始积累，属 v0.1 明确语义）。游标只随「实际
		// 收到的行」推进：无行时窗口保持增长，行不会因轮询跳拍而漏采。
		cur = &stream{app: app.Name, appID: app.ID, service: service, lastAt: m.clock()}
		m.streams[key] = cur
	}
	since := cur.lastAt
	m.mu.Unlock()

	swarmName, err := naming.ServiceName(app.Name, service)
	if err != nil {
		m.log.Warn("logs: swarm service name resolve failed", "app", app.Name, "service", service, "error", err.Error())
		return
	}
	lines, err := m.port.StreamServiceLogs(ctx, swarmName, since, false)
	if err != nil {
		// 服务不存在（未部署/已删）安静跳过；其余底座暂态 debug 级。
		m.log.Debug("logs: stream open failed", "app", app.Name, "service", service, "error", err.Error())
		return
	}
	var last time.Time
	for line := range lines {
		at := line.At
		if at.IsZero() {
			at = m.clock()
		}
		if at.After(last) {
			last = at
		}
		e := Entry{
			App:     app.Name,
			Service: service,
			At:      at,
			Stderr:  line.Stderr,
			Line:    red.redact(line.Line),
			Source:  SourceContainer,
		}
		m.hub.ingest(e)
		if err := m.dsk.append(ctx, e); err != nil {
			m.log.Warn("logs: disk append failed", "app", app.Name, "error", err.Error())
		}
	}
	if !last.IsZero() {
		m.mu.Lock()
		if last.After(cur.lastAt) {
			cur.lastAt = last
		}
		m.mu.Unlock()
	}
}

// Follow 订阅实时日志（T2.20：实时流；gRPC Follow RPC 的管线入口）。
// 先回放 ring（限深回放），后实时扇出；ctx 取消自动注销（断线重连 =
// 客户端重新 Follow）。
func (m *Manager) Follow(ctx context.Context, app, service string) (<-chan Entry, func()) {
	return m.hub.follow(ctx, app, service)
}

// HistoryQuery 是历史检索参数（app 必填；service/source 空 = 不过滤）。
type HistoryQuery struct {
	App     string
	Service string
	Source  string // "" | container | build
	Since   time.Time
	Until   time.Time
	Limit   int
}

// History limit 缺省与天花板。
const (
	defaultHistoryLimit = 200
	maxHistoryLimit     = 1000
	maxBuildsPerHistory = 50
)

// History 检索历史日志：container 来源走落盘扫描；build 来源读 builds 表
// log_path 产物（行级无时间戳，行时间取构建开始时刻——诚实缺失，不伪造
// 逐行时间）。结果按时间升序，超 limit 取最新 limit 条。
func (m *Manager) History(ctx context.Context, q HistoryQuery) ([]Entry, error) {
	if q.Limit <= 0 {
		q.Limit = defaultHistoryLimit
	}
	if q.Limit > maxHistoryLimit {
		q.Limit = maxHistoryLimit
	}
	app, err := m.st.GetAppByName(ctx, q.App)
	if err != nil {
		return nil, err
	}
	var out []Entry
	if q.Source == "" || q.Source == SourceContainer {
		rows, err := m.dsk.query(ctx, app.Name, q.Service, SourceContainer, q.Since, q.Until, q.Limit)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	if q.Source == "" || q.Source == SourceBuild {
		rows, err := m.buildLogEntries(ctx, app, q)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > q.Limit {
		out = out[len(out)-q.Limit:]
	}
	return out, nil
}

// buildLogEntries 从 builds 表产物读构建日志（source=build）。B3（出站
// 字节出口收口）：出口与容器日志同管线过该 app 的 redactor——构建日志
// 同样可能回显 secret 值（拉源 token、env 值），不得因来源不同绕过脱敏。
func (m *Manager) buildLogEntries(ctx context.Context, app state.App, q HistoryQuery) ([]Entry, error) {
	builds, err := m.st.ListAppBuilds(ctx, app.ID, maxBuildsPerHistory)
	if err != nil {
		return nil, err
	}
	red := m.red.forApp(ctx, app.ID)
	var out []Entry
	for _, b := range builds {
		if q.Service != "" && b.Service != q.Service {
			continue
		}
		if b.LogPath == "" {
			continue
		}
		at := b.StartedAt
		if at.IsZero() {
			at = b.CreatedAt
		}
		if !q.Since.IsZero() && at.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && at.After(q.Until) {
			continue
		}
		lines, err := readPlainLines(b.LogPath, q.Limit)
		if err != nil {
			m.log.Warn("logs: read build log failed", "build", b.ID, "error", err.Error())
			continue
		}
		for _, line := range lines {
			out = append(out, Entry{
				App:     app.Name,
				Service: b.Service,
				At:      at,
				Line:    red.redact(line),
				Source:  SourceBuild,
			})
		}
	}
	return out, nil
}

// readPlainLines 读取纯文本日志文件（builds 产物形态），至多 limit 行。
func readPlainLines(path string, limit int) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304：路径来自 builds 表登记（写侧平台受管）
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() && (limit <= 0 || len(out) < limit) {
		out = append(out, strings.TrimRight(sc.Text(), "\r"))
	}
	return out, sc.Err()
}
