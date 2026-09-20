package logs

import (
	"bufio"
	"context"
	"errors"
	"os"
	"slices"
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
	active := make(map[string]struct{}, len(apps))
	for _, app := range apps {
		active[app.Name] = struct{}{}
	}
	// M7-6：先对账已消失 app 的延迟淘汰（连续 miss 超窗 → 游标 + ring
	// 回收），再推进活跃面采集。
	m.evictStaleStreamState(active)
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
	// MG-1 纵深防御：单轮看门狗。scanLines 修复后底座流恒会排水结束，此处
	// 只兜「底座/管线再出同款永不返回缺陷」的极端面——超时记 Error 并放弃
	// 本轮（下一轮重开流重试），采集循环/落盘/prune 不再被单条流拖死。权衡：
	// 放弃后底座侧流 goroutine 可能挂在无读者的 channel 发送上，泄漏到 ctx
	// 取消（进程停机）——跳过一轮的代价优于全管线停摆，且正常路径（流必
	// 结束）不会触发，故选最小实现：只告警 + 弃轮，不主动断流。
	watchdog := time.NewTimer(m.pollWatchdog())
	defer watchdog.Stop()
	var last time.Time
deliver:
	for {
		select {
		case <-ctx.Done():
			return
		case <-watchdog.C:
			m.log.Error("logs: poll round exceeded watchdog deadline, abandoning this round",
				"app", app.Name, "service", service)
			return
		case line, ok := <-lines:
			if !ok {
				break deliver // 流自然结束。
			}
			at := line.At
			if at.IsZero() {
				// M7-4：无时间戳续行不推进游标——旧实现以墙钟代投，会把
				// cur.lastAt 拨到 now，下一轮 since 越过同批未投递行（跳批
				// 丢行）。行本身以零值 At 投递（视图层容忍 e.at 可空，console
				// 端 `e.at ?? ""`；落盘侧落入零日文件 00010101.jsonl，检索
				// 与保留期清理语义均覆盖）。取舍：若全批皆零 At，last 保持
				// 不动，下轮重复拉取该批——重复优于丢失，行内容相同幂等。
			} else if at.After(last) {
				last = at // 游标只随可信时间戳行推进。
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
	}
	if !last.IsZero() {
		m.mu.Lock()
		if last.After(cur.lastAt) {
			cur.lastAt = last
		}
		m.mu.Unlock()
	}
}

// minPollWatchdog 是单轮拉取看门狗的期限下限（MG-1 纵深防御）。
const minPollWatchdog = 30 * time.Second

// pollWatchdog 返回本轮看门狗期限：max(3×扫描周期, 30s)——既覆盖慢轮询
// 周期下的正常整轮时长，又有绝对下限防扫描周期配置过小时看门狗过敏。
// pollWatchdogOverride 是测试注入位（同包私有，缩短真实等待）。
func (m *Manager) pollWatchdog() time.Duration {
	if m.pollWatchdogOverride > 0 {
		return m.pollWatchdogOverride
	}
	d := 3 * m.cfg.ScanInterval()
	if d < minPollWatchdog {
		d = minPollWatchdog
	}
	return d
}

// streamEvictAfter 是 app 消失后采集状态的延迟淘汰窗（M7-6）：窗内 app
// 回归则撤销计时保留状态（采集连续性优先）；超窗删除该 app 全部
// per-service 游标与 ring——app 回归等价首启语义（游标自发现时刻重建，
// 历史由落盘承载，代价可接受）。不淘汰则 hub.streams/m.streams 只增不删，
// 随 app 创建/删除更迭无限累积（ring 每流 ringSize 条）。
const streamEvictAfter = 5 * time.Minute

// evictStaleStreamState 执行一轮淘汰对账（scanOnce 头部调用）：非 active
// app 记 miss 首见时刻；连续 miss 超过 streamEvictAfter 的 app 删除其全部
// 游标（m.streams 按 app 前缀）并同步清 hub 侧 ring。active 判定以本轮
// ListActiveApps 快照为准。
func (m *Manager) evictStaleStreamState(active map[string]struct{}) {
	now := m.clock()
	m.mu.Lock()
	// 计时更新：活跃 app 撤销 miss 计时；非活跃 app 记首见 miss 时刻。
	for _, cur := range m.streams {
		if _, ok := active[cur.app]; ok {
			delete(m.appMiss, cur.app)
			continue
		}
		if _, seen := m.appMiss[cur.app]; !seen {
			m.appMiss[cur.app] = now
		}
	}
	// 淘汰：连续 miss 超窗 → 删该 app 全部游标（各服务键在迭代中一并命中）。
	var evicted []string
	for key, cur := range m.streams {
		if _, ok := active[cur.app]; ok {
			continue
		}
		if first, miss := m.appMiss[cur.app]; miss && now.Sub(first) >= streamEvictAfter {
			delete(m.streams, key)
			if !slices.Contains(evicted, cur.app) {
				evicted = append(evicted, cur.app)
			}
		}
	}
	// 删尽的 app 清计时（app 再消失等价首次发现，重新起算）。
	for _, app := range evicted {
		delete(m.appMiss, app)
	}
	m.mu.Unlock()
	if len(evicted) > 0 {
		m.hub.evictApps(evicted)
		m.log.Info("logs: evicted stream state for inactive apps", "apps", strings.Join(evicted, ","))
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

// readPlainLines 读取纯文本日志文件的**最后** limit 行（M7-5：构建失败的
// 关键信息——错误摘要/退出原因——在文件尾部，头部截断使诊断面不可达，且
// 与容器源「超 limit 取最新 limit 条」语义相反）。滑窗实现：总驻留 ≤ limit
// 行，文件多大都不全量进内存；单行仍受 1MiB 扫描上限（builds 产物行不会
// 接近该量级，维持原语义不在本轮扩面）。
func readPlainLines(path string, limit int) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304：路径来自 builds 表登记（写侧平台受管）
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var win []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if limit > 0 && len(win) == limit {
			copy(win, win[1:]) // 挤掉最旧（滑窗）
			win[limit-1] = line
			continue
		}
		win = append(win, line)
	}
	return win, sc.Err()
}
