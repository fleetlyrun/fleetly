// Package capacity 是通用伸缩决策环应用模块（P2-2，设计真源
// docs/design/2026-11-19-functions-phase2.md §2）：容量 = 行面旋钮的自动
// 写者。「容量 = 行面旋钮」是 App/Function 共享平面（ADR-0102 收编条款）
// ——本模块先以 Function 单消费者验证环形状（App 侧接入 = 后续批）。
//
// 决策律（v1，设计 §2/§1.3）：
//   - 缩零（park）：min=0 函数收敛到 running 后活跃信号静默满 idle 窗
//     （行旋钮，0 = 平台缺省 300s）→ 行 CAS 归 0。running 门是设计 v2
//     §1.3 判据原词（收敛环观测 running 且静默满窗）——载体还在收敛
//     （pending）的函数不 park，防收敛与缩零赛跑。唤醒即活动（唤醒后
//     防抖窗重新计时——Knative scale-down-delay 同款）。
//   - 扩容：per-副本请求压力 > 目标阈值 → +1（配额 defer 语义——引擎
//     自发写绝不静默超限，ADR-0068/设计 §15.5：本拍放弃 + 事件明示，
//     沿上单发 defer 事件防风暴）。
//   - 缩容：活跃信号静默持续满窗 → 单拍至多减半（Knative
//     max-scale-down-rate 同款限速），地板 min。min=0 函数走缩零分支
//     （归 0 而非减半——产品语义）。
//
// 单写者纪律：决策环写行、收敛环只读行面（Phase 1「收敛环盲目」立法）；
// 与唤醒 CAS（RequestWake WHERE replicas=0）、用户 scale 的竞态由
// DecideCapacity 的 (replicas, updated_at) 乐观对锚串行——落败方下拍
// 重算，无撕列。swarm/k3s 双 Runtime 都走平台环（k3s HPA 原生接入是
// 后续优化批——单源纪律，此裁随实施 ADR）。
//
// 观测信号面（P2-4）：public 函数经 Proxy RouteMetricKey 分账键查
// traefik per-router 请求率；载体活动查负载词汇（ADR-0065）的 function
// 轴 CPU。观测面缺席（nil/查询失败）= 该信号缺失（诚实断口）——伸缩
// 信号缺席即不动（保守），park 退化为唤醒活动判定（观测面在时 busy 函数
// 恒活跃、静默函数才 park；观测面缺席时 busy 函数会在窗后被 park 并由
// 下一请求唤醒——退化语义，观测面健康是产品承诺的前提）。
package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// 事件名（eventcode 注册表执法面——registry 只增，本批两枚锚）。
const (
	eventFunctionScaled        = "function.scaled"
	eventFunctionScaleDeferred = "function.scale_deferred"
)

// 决策律参数（循环本地常量，与 logging/metrics 采集环同纪律——运维面
// 诉求出现时进 Options 端口）：
const (
	// decisionTick 是决策节拍锚（粗锚：1s tick 内自持 10s——idle 窗语义
	// 在秒级，节拍成本两枚 PromQL 查询/拍）。
	decisionTick = 10 * time.Second
	// defaultIdleTimeout 是 idle 行旋钮零值的平台缺省（设计 v2 §1.3：
	// 默认 300s 同 Knative scale-down-delay 惯例）。
	defaultIdleTimeout = 300 * time.Second
	// maxIdleTimeout 是行旋钮值域上界（受理位 CEL 同值域）。
	maxIdleTimeout = 86400 * time.Second
	// targetRequestsPerReplica 是扩容阈值：per-副本请求率（2m 窗均值，
	// req/s）超过即 +1。Knative concurrency=80 的秒级近似下取保守值——
	// 请求驱动函数的典型单副本吞吐远低于 80 qps（冷启成本 > 排队成本）。
	targetRequestsPerReplica = 10.0
	// cpuActiveFloor 是载体活动地板（负载词汇 cpu_cores 率低于此值视为
	// 静默——0.5% 核；容量探活/健康检查的常值开销以下）。
	cpuActiveFloor = 0.005
	// requestWindow 是请求率查询窗（rate 的 2m 惯例——平滑突发毛刺）。
	requestWindow = "2m"
)

// QuotaCheck 是函数配额定律端口（ADR-0068 单源 CheckFunctionQuota 的
// 组合根绑定闭包——delta 是本次副本增量；错误 = defer 而非拒绝：引擎
// 自发写绝不静默超限。类型真源在契约叶子——Deps↔Ports 结构体直转的
// 字段类型恒等要求）。
type QuotaCheck = contract.QuotaCheck

// Deps 是应用模块依赖与内核端口的注入面（与 contract.CapacityPorts 逐
// 字段同形——组合根工厂经直接互转装配，字段漂移即编译红）。
type Deps struct {
	Repos          transact.Stores
	Metrics        capability.Metrics // nil = 观测面缺席（park 退化唤醒活动判定）
	RouteMetricKey func(capability.Route) string
	QuotaCheck     QuotaCheck
	FactTeam       func(ctx context.Context, projectID string) (string, error)
	Commit         contract.CommitWrite
	KickFunctions  func()
	Log            *slog.Logger
	Clock          state.Clock

	BoundedStep func(ctx context.Context) (context.Context, context.CancelFunc)
}

// Capacity 是决策环应用模块本体（复数普通名词 struct + 白盒测试）。
type Capacity struct {
	repos          transact.Stores
	metrics        capability.Metrics
	routeMetricKey func(capability.Route) string
	quotaCheck     QuotaCheck
	factTeam       func(ctx context.Context, projectID string) (string, error)
	commit         contract.CommitWrite
	kick           func()
	log            *slog.Logger
	clock          state.Clock

	boundedStep func(ctx context.Context) (context.Context, context.CancelFunc)

	// watched 是 per-Function 活动账（进程内存：daemon 重启即失忆——
	// 失忆方向保守（lastActive 重置 = 多等一个 idle 窗才 park），重启
	// 安全。键 = 函数 id；停止观测的行随拍清扫（tombstone 无漏）。
	watchMu sync.Mutex
	watched map[string]watchState
	// lastScrapeAt 是决策节拍粗锚。
	lastScrapeAt time.Time
}

// watchState 是单函数的决策记忆（活动时刻 + 所见行面 + defer 沿标记）。
type watchState struct {
	lastActive  time.Time // 活动信号最近一次在场时刻（观测缺席不刷新）
	lastReplica int64     // 上拍所见 replicas（唤醒 0→N 转移的活动锚）
	lastStatus  string    // 上拍所见 status 缓存（running 转入沿的窗口重置锚）
	lastSeen    time.Time // 上拍所见时刻（账本清扫锚）
	deferred    bool      // 配额 defer 沿标记：容量实际变化即清位
}

// New 构造决策环应用模块（不启动——循环步由 engine 核驱动）。
func New(deps Deps) *Capacity {
	return &Capacity{
		repos:          deps.Repos,
		metrics:        deps.Metrics,
		routeMetricKey: deps.RouteMetricKey,
		quotaCheck:     deps.QuotaCheck,
		factTeam:       deps.FactTeam,
		commit:         deps.Commit,
		kick:           deps.KickFunctions,
		log:            deps.Log,
		clock:          deps.Clock,
		boundedStep:    deps.BoundedStep,
		watched:        make(map[string]watchState),
	}
}

// Name implements contract.Module.
func (c *Capacity) Name() string { return "capacity" }

// Loops implements contract.Module：单环 autoscale（决策环；ring 表登记
// 名与 engine construct.go 循环表一致——ringregistry 契约测试冻结）。
func (c *Capacity) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "autoscale", Step: c.Step}}
}

// Step 是决策循环收敛步：节拍锚 → 观测快照（请求率/载体 CPU 双面）→
// 逐行决策（缩零/扩容/缩容——事务内行 CAS + 事实扇出 + Kick）。
func (c *Capacity) Step(ctx context.Context) {
	now := c.clock.Now()
	if now.Sub(c.lastScrapeAt) < decisionTick {
		return
	}
	c.lastScrapeAt = now

	if c.boundedStep != nil {
		var cancel context.CancelFunc
		ctx, cancel = c.boundedStep(ctx)
		defer cancel()
	}
	rows, err := c.repos.Functions.List(ctx)
	if err != nil {
		c.log.Error("capacity step: list functions", "err", err)
		return
	}
	rates := c.requestRates(ctx, now)
	cpus := c.functionCPU(ctx, now)
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		c.decide(ctx, &rows[i], rates, cpus, now)
	}
	c.sweep(now)
}

// decide 是单行决策拍：活动信号评估（唤醒转移/请求率/载体 CPU 任一在场
// 即刷新活动时钟）→ 缩零/扩容/缩容三分支（事务内行 CAS——单写者纪律；
// defer 语义沿上明示）。
func (c *Capacity) decide(ctx context.Context, row *function.Function, rates map[string]float64, cpus map[string]float64, now time.Time) {
	c.watchMu.Lock()
	st, had := c.watched[row.ID]
	c.watchMu.Unlock()
	if !had || (st.lastReplica == 0 && row.Replicas > 0) {
		// 首见或唤醒转移（0→N）：活动时钟重置（唤醒即活动——Knative
		// scale-down-delay 同款；重启失忆方向保守）。
		st.lastActive = now
	}
	if had && row.Replicas != st.lastReplica {
		// 行面外生变化（用户 scale/唤醒落账）：决策基线被改写——defer
		// 沿失效（沿上单发按沿计，外生变化即新沿）。
		st.deferred = false
	}

	// 活动时钟只在 running 后计时（pending 收敛窗不计静默——running 门
	// 语义）：非 running 持续持钟，转入 running 的沿重置窗口（设计 v2
	// §1.3「收敛到 running 后静默满窗」——继承 pending 期静默会提前 park）。
	if row.Status != "running" || st.lastStatus != "running" {
		st.lastActive = now
	}
	st.lastStatus = row.Status
	if c.rateFor(row, rates) > 0 {
		st.lastActive = now
	}
	if cpus[lowerID(row.ID)] > cpuActiveFloor {
		st.lastActive = now
	}

	silent := now.Sub(st.lastActive) >= idleWindow(row.IdleTimeoutSecs)
	// landedTo 是本拍决策落账后的真值锚（-1 = 无决策写落账）：lastReplica
	// 保存决策后行面而非过期的列举快照——唤醒转移检测/defer 沿判定都锚
	// 真值。
	landedTo := int64(-1)

	switch {
	case row.MinReplicas == 0 && row.Replicas > 0 && row.Status == "running" && silent:
		// 缩零（park）：活跃信号静默满窗 → 归 0。行 CAS 对锚乐观并发
		// ——唤醒/用户 scale 竞态落败下拍重算（无撕列）。
		landed, err := c.commitDecide(ctx, row, 0, false)
		if err != nil {
			c.log.Error("capacity park", "function", row.ID, "err", err)
			return
		}
		if landed {
			c.log.Info("capacity parked function",
				"idle_for", now.Sub(st.lastActive).Round(time.Second).String(), "function", row.ID)
			st.deferred = false
			landedTo = 0
			c.kick()
		}
	case row.Replicas < row.MaxReplicas && c.pressureFor(row, rates) > targetRequestsPerReplica:
		// 扩容：per-副本请求压力超阈值 → +1（配额 defer 语义——绝不
		// 静默超限，事件沿上明示；defer 沿中不再重试直到行面变化）。
		if c.metrics == nil {
			return // 观测面缺席（手动装配）——保守不动
		}
		if st.deferred {
			return
		}
		target := row.Replicas + 1
		landed, err := c.commitDecide(ctx, row, target, true)
		if err != nil {
			if errors.Is(err, contract.ErrFunctionQuota) {
				// defer 语义（设计 §15.5 第四模态）：本拍放弃扩容 +
				// 事件明示，绝不静默超限；沿上单发（沿标记随本拍落账
				// ——提前返回前保存，防沿丢失下拍重发）。
				st.deferred = true
				st.lastReplica = row.Replicas
				st.lastSeen = now
				c.watchMu.Lock()
				c.watched[row.ID] = st
				c.watchMu.Unlock()
				c.log.Warn("capacity scale-up deferred (quota)",
					"function", row.ID, "target", target)
				c.emitDeferred(ctx, row, target)
				return
			}
			c.log.Error("capacity scale-up", "function", row.ID, "err", err)
			return
		}
		if landed {
			c.log.Info("capacity scaled up", "function", row.ID, "replicas", target)
			st.deferred = false
			landedTo = target
			c.kick()
		}
	case row.Replicas > row.MinReplicas && row.Status == "running" && silent:
		// 缩容：活跃信号静默持续满窗 → 单拍至多减半（Knative
		// max-scale-down-rate 同款），地板 min。min=0 函数走上面的缩零
		// 分支（归 0 而非减半——产品语义）。
		target := row.Replicas - max(1, row.Replicas/2)
		if target < row.MinReplicas {
			target = row.MinReplicas
		}
		if target == row.Replicas {
			return
		}
		landed, err := c.commitDecide(ctx, row, target, false)
		if err != nil {
			c.log.Error("capacity scale-down", "function", row.ID, "err", err)
			return
		}
		if landed {
			c.log.Info("capacity scaled down", "function", row.ID, "replicas", target)
			landedTo = target
			c.kick()
		}
	}

	st.lastReplica = row.Replicas
	if landedTo >= 0 {
		st.lastReplica = landedTo
	}
	st.lastSeen = now
	c.watchMu.Lock()
	c.watched[row.ID] = st
	c.watchMu.Unlock()
}

// commitDecide 经 CommitWrite 事务承载决策写（检查 → CAS 写 → 事实扇出
// ——单声明 kernel FactRow 默认双真；行与事件原子）。scale-up 带配额
// 检查（defer 语义——检查失败按哨兵分流事件面）。
func (c *Capacity) commitDecide(ctx context.Context, row *function.Function, target int64, withQuota bool) (bool, error) {
	var checks []contract.Check
	if withQuota && c.quotaCheck != nil {
		checks = append(checks, func(ctx context.Context, st transact.Stores) error {
			return c.quotaCheck(ctx, st, row.ProjectID, target-row.Replicas)
		})
	}
	landed := false
	err := c.commit(ctx, c.repos, contract.WriteFact{
		Checks: checks,
		Write: func(ctx context.Context, st transact.Stores) error {
			ok, werr := st.Functions.DecideCapacity(ctx, row.ID, target, row.Replicas, row.UpdatedAt)
			landed = ok
			return werr
		},
		Facts: []func() writes.FactRow{c.scaledFact(row, target)},
	})
	if err != nil {
		return false, err
	}
	return landed, nil
}

// emitDeferred 发配额 defer 事件（引擎自发写的诚实明示——沿上单发防
// 风暴；零写段合法，事实单落）。
func (c *Capacity) emitDeferred(ctx context.Context, row *function.Function, target int64) {
	fact := c.deferredFact(row, target)
	if err := c.commit(ctx, c.repos, contract.WriteFact{Facts: []func() writes.FactRow{fact}}); err != nil {
		c.log.Error("capacity emit deferred fact", "function", row.ID, "err", err)
	}
}

// scaledFact 铸 function.scaled 行事实（决策环来源——payload 携目标副本
// 与 cause：park|autoscale 同族动机位；Team 轴经 FactTeam 端口解析，
// 解析失败落平台级哨兵口径由 kernel 承载）。
func (c *Capacity) scaledFact(row *function.Function, replicas int64) func() writes.FactRow {
	cause := "autoscale"
	if replicas == 0 {
		cause = "park"
	}
	return func() writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionScaled, Aggregate: "function", ID: row.ID,
			Payload: mustJSON(contract.FunctionScaleEventPayload{Replicas: replicas, Cause: cause, Origin: "autoscaler"}),
			Action:  "function.scale", Resource: "function/" + row.ID,
			TeamID: c.teamOf(row.ProjectID),
		}
	}
}

// deferredFact 铸 function.scale_deferred 行事实（配额 defer 明示）。
func (c *Capacity) deferredFact(row *function.Function, target int64) func() writes.FactRow {
	return func() writes.FactRow {
		return writes.FactRow{
			Name: eventFunctionScaleDeferred, Aggregate: "function", ID: row.ID,
			Payload: mustJSON(contract.FunctionScaleEventPayload{Replicas: target, Reason: "quota", Origin: "autoscaler"}),
			Action:  "function.scale_deferred", Resource: "function/" + row.ID,
			TeamID: c.teamOf(row.ProjectID),
		}
	}
}

// teamOf 解析事件的 Team 轴（FactTeam 端口；失败落空 = 平台级——kernel
// FactRow 契约的既有口径）。
func (c *Capacity) teamOf(projectID string) string {
	if c.factTeam == nil {
		return ""
	}
	team, err := c.factTeam(context.Background(), projectID)
	if err != nil {
		return ""
	}
	return team
}

// requestRates 拉一拍 per-router 请求率快照（traefik_service_requests_total
// rate 2m——键 = Proxy RouteMetricKey 分账键；观测面缺席/查询失败 = nil
// ——诚实断口：伸缩保守不动）。
func (c *Capacity) requestRates(ctx context.Context, now time.Time) map[string]float64 {
	if c.metrics == nil || c.routeMetricKey == nil {
		return nil
	}
	// service 标签带 provider 后缀（`<key>@http`——traefik v3 实证）——
	// 分账键剥后缀匹配（裸键前缀锚）。
	q := fmt.Sprintf("sum by (service) (rate(traefik_service_requests_total[%s]))", requestWindow)
	series, err := c.metrics.QuerySeries(ctx, q, now.Add(-2*time.Minute), now, 0)
	if err != nil {
		c.log.Warn("capacity request rates: query", "err", err)
		return nil
	}
	out := make(map[string]float64, len(series))
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		name := s.Metric["service"]
		if i := strings.Index(name, "@"); i >= 0 {
			name = name[:i]
		}
		out[name] = s.Points[len(s.Points)-1].Value
	}
	return out
}

// functionCPU 拉一拍 per-function 载体 CPU 快照（负载词汇 function 轴
// ——private 函数的活动信号；查询失败 = nil）。
func (c *Capacity) functionCPU(ctx context.Context, now time.Time) map[string]float64 {
	if c.metrics == nil {
		return nil
	}
	q := fmt.Sprintf("max by (function) (%s{function!=\"\"})", capability.MetricWorkloadCPUCores)
	series, err := c.metrics.QuerySeries(ctx, q, now.Add(-2*time.Minute), now, 0)
	if err != nil {
		c.log.Warn("capacity function cpu: query", "err", err)
		return nil
	}
	out := make(map[string]float64, len(series))
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		out[s.Metric["function"]] = s.Points[len(s.Points)-1].Value
	}
	return out
}

// rateFor 取该函数的请求率（public 且分账键在场才可查——private 无请求
// 观测是结构性断口；rates nil（查询失败）返回 -1 = 无信号面，与真 0
// 区分——真 0 参与静默判定，无信号面不影响活动时钟）。
func (c *Capacity) rateFor(row *function.Function, rates map[string]float64) float64 {
	if rates == nil || row.Exposure != "public" || row.PublicHost == "" || c.routeMetricKey == nil {
		return -1
	}
	key := c.routeMetricKey(capability.Route{Host: row.PublicHost})
	if key == "" {
		return -1
	}
	return rates[key]
}

// pressureFor 取该函数的 per-副本请求压力（无信号面 = 0——不触发扩容）。
func (c *Capacity) pressureFor(row *function.Function, rates map[string]float64) float64 {
	rate := c.rateFor(row, rates)
	if rate <= 0 {
		return 0
	}
	return rate / float64(max(1, row.Replicas))
}

// idleWindow 折行旋钮（0 = 平台缺省；超上界钳制——存量行防呆）。
func idleWindow(secs int64) time.Duration {
	if secs <= 0 {
		return defaultIdleTimeout
	}
	d := time.Duration(secs) * time.Second
	if d > maxIdleTimeout {
		d = maxIdleTimeout
	}
	return d
}

// sweep 清扫停止观测的账本条目（tombstone/删除行——进程内存有界）。
func (c *Capacity) sweep(now time.Time) {
	const staleAfter = 10 * time.Minute
	c.watchMu.Lock()
	defer c.watchMu.Unlock()
	for id, st := range c.watched {
		if now.Sub(st.lastSeen) > staleAfter {
			delete(c.watched, id)
		}
	}
}

// lowerID 是函数 id 的小写归一（负载词汇 function 标签小写形态——采集
// 循环 sanitizeNamePart 同归一）。
func lowerID(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

// mustJSON 铸事件载荷（标量 map——Marshal 不失败；失败即编程错误 panic
// 与 grpcapi.MustJSON 同纪律）。
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("capacity: marshal fact payload: %v", err))
	}
	return b
}
