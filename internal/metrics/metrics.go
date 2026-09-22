// Package metrics 是托管 metrics 三件套（D-W5-2 opt-in，E6 观测专项设计
// §4/§4.2，W5-S3）：VictoriaMetrics 单机版（查询/存储）+ cAdvisor（容器
// 指标采集，global）+ node_exporter（节点指标采集，global）。
//
// 两个职责面：
//
//  1. duty（本文件 + spec.go + docker.go，victorialogs/rustfs manager 同款
//     形态）：设置驱动——metrics.mode=on 时幂等部署/收敛三件 Swarm 服务
//     （钉版镜像、VM 卷钉 manager、三件全部 host 网络任务 + 进程原生回环
//     监听〔零公网面不变量，D-W5-4 等价承载；跨节点采集诚实边界见 spec.go
//     头注记〕、-retentionPeriod 对齐 metrics.retention_days）；切回 unset
//     时三件服务移除 + 抓取 config 清场，**数据卷保留**（rustfs 禁用同型
//     数据安全语义）。常驻收敛循环（失败退避重试、收敛后按扫描周期复检
//     漂移），差分事件 metrics.stack_deployed / metrics.stack_removed
//     （注册表只增；payload 不含任何敏感材料——全链无凭据面）。
//
//  2. 查询后端（backend.go）：fleetlyd 侧 SearchMetrics 的 VM 消费面——
//     PromQL 透传（操作员工具，不做查询沙箱——诚实口径设计 §4.2）、
//     /api/v1/query_range 查询与 /health 健康拨测。
//
// 与 victorialogs duty 的差异（设计裁决的落地）：opt-in（缺省关——未显式
// 设置过 metrics.mode 的库不部署任何东西，验收标准「mode=unset 零新增常
// 驻」）；三件服务 + 抓取 config 对象的联动收敛；抓取面（cAdvisor/
// node_exporter 在位即被 VM 抓——无 hub 直推链路）。
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// settingsLoadTimeout 是 status/健康检查面的设置读取预算（CheckHealth
// 无 ctx 形态的自有预算，victorialogs 同款）。
const settingsLoadTimeout = 3 * time.Second

// DefaultRetentionDays 是 metrics.retention_days 的缺省值（设计 §4.1：
// 14d；internal/runtime/config.go 的 MetricsConfig 缺省回落指向本常量
// ——单一事实源在本包）。
const DefaultRetentionDays = 14

// Config 是 metrics.* 配置节的 typed 形态（retention 对齐；Normalize 回落
// 缺省——单一事实源纪律）。
type Config struct {
	// RetentionDays 是 VM -retentionPeriod 的对齐天数（metrics.retention_days）。
	RetentionDays int
}

// Normalize 回落缺省值（非正值一律 DefaultRetentionDays——保留期是契约
// 默认，不允许误配成 0 静默关闭清理）。
func (c Config) Normalize() Config {
	if c.RetentionDays <= 0 {
		c.RetentionDays = DefaultRetentionDays
	}
	return c
}

// Manager 是托管 metrics 三件套 duty 管理器。
type Manager struct {
	store  *state.Store
	docker dockerPort
	log    *slog.Logger
	// retentionDays 是 -retentionPeriod 的对齐源（config 键
	// metrics.retention_days；构造期注入，spec 渲染消费）。
	retentionDays int

	// retryInterval 是收敛失败的退避（零值回落 retryInterval 常量——
	// 单测注入短退避驱动重试断言）。
	retryInterval time.Duration
	// health 是回环健康拨测端口（CheckHealth 的可达性面；单测注入。
	// nil = 不拨测——部署在位即视为健康，单测/精简装配形态）。
	health func(ctx context.Context) error
}

// NewManager 构造 duty 管理器（自建 Docker 连接；cleanup 释放）。
func NewManager(store *state.Store, retentionDays int, log *slog.Logger) (*Manager, func(), error) {
	dc, err := newRealDockerClient("")
	if err != nil {
		return nil, nil, err
	}
	return NewManagerWithDocker(store, retentionDays, dc, log), func() { _ = dc.Close() }, nil
}

// NewManagerWithDocker 以注入的 dockerPort 构造（单测）。
func NewManagerWithDocker(store *state.Store, retentionDays int, dc dockerPort, log *slog.Logger) *Manager {
	return &Manager{store: store, retentionDays: retentionDays, docker: dc, log: log}
}

// WithHealth 注入回环健康拨测端口（生产装配 = Backend.Ping；nil = 不拨测）。
func (m *Manager) WithHealth(h func(ctx context.Context) error) *Manager {
	m.health = h
	return m
}

// Run 是常驻收敛循环（victorialogs Run 同构；fleetlyd 装配壳调用；ctx 取消
// 返回）：每拍 LoadMetricsSettings 现读（运行期设置不缓存长驻）——mode=on
// 走三件部署收敛，其他值走移除清场（幂等，收敛即稳态）；失败退避重试，
// 收敛后按扫描周期复检漂移。
func (m *Manager) Run(ctx context.Context) error {
	retry := m.retryInterval
	if retry <= 0 {
		retry = retryInterval
	}
	scan := scanInterval
	converged := false
	for {
		oc, err := m.Ensure(ctx)
		switch {
		case err != nil:
			converged = false
			m.log.Warn("metrics: converge deferred (retrying)", "error", err, "retry_in", retry.String())
		case oc == outcomeDeployed && !converged:
			converged = true
			m.log.Info("metrics: converged (managed metrics stack deployed; loopback-only host tasks, "+
				"the data volume persists across mode switch)", "services",
				VictoriaServiceName+","+CAdvisorServiceName+","+NodeExporterServiceName, "volume", VolumeName)
		case oc == outcomeIdle && !converged:
			converged = true
			m.log.Info("metrics: quiesced (metrics.mode != on; no managed deployment owed)")
		}
		if !sleepCtx(ctx, retryOrScan(retry, scan, converged)) {
			return nil
		}
	}
}

// outcome 是一拍收敛的结论（部署在位 / 非托管稳态）。
type outcome int

const (
	outcomeDeployed outcome = iota
	outcomeIdle
)

// managedServices 是三件服务名（收敛与清场的固定次序——VM 先建后删：建
// 时采集器先在位则首拍即可抓到；删时先删 VM 停掉抓取面再删采集器）。
func managedServices() []string {
	return []string{CAdvisorServiceName, NodeExporterServiceName, VictoriaServiceName}
}

// Ensure 执行一拍收敛。返回当前应许态结论与可重试错误。
func (m *Manager) Ensure(ctx context.Context) (outcome, error) {
	in, err := m.store.LoadMetricsSettings(ctx)
	if err != nil {
		return outcomeIdle, fmt.Errorf("metrics: load metrics settings: %w", err)
	}
	if in.Mode != state.MetricsModeOn {
		return outcomeIdle, m.removeIfPresent(ctx)
	}
	return outcomeDeployed, m.converge(ctx)
}

// converge 三件部署/漂移收敛（设计 §4.1；victorialogs converge 同构，无
// 凭据面）：抓取 config → 数据卷 → 期望 spec（VM 钉 manager + 限额 + host
// 网络回环监听 + retention 参数 + 抓取配置引用；cAdvisor/node_exporter
// global）→ inspect 缺失创建/漂移更新 → 旧抓取 config GC。
func (m *Manager) converge(ctx context.Context) error {
	active, err := m.docker.Info(ctx)
	if err != nil {
		return err
	}
	if !active {
		return ErrNotSwarmReady
	}
	// manager 平台 ID（meta 单值真源；identity duty 尚未铸造时显式失败
	// 退避重试——约束引用空 ID 会得到永不调度的任务，宁缺毋错）。
	platformID, err := m.store.GetMeta(ctx, state.MetaKeyPlatformNodeID)
	if err != nil {
		return fmt.Errorf("metrics: read platform node id: %w", err)
	}
	if platformID == "" {
		return errors.New("metrics: platform node id not ensured yet (identity duty pending; the pin constraint requires it)")
	}
	// ① 抓取配置（swarm config 对象，内容寻址——服务引用它的名字，内容
	//    变更经新对象 + 服务更新收敛）。返回底座对象 ID——服务 spec 的
	//    ConfigReference 需要 ID+名双写（只写名 = "malformed config
	//    reference"，W3 secret-ID 同族教训，2026-09-22 dind 实证）。
	scrapeSpec := buildScrapeConfigSpec()
	scrapeID, err := m.docker.ConfigEnsure(ctx, scrapeSpec.Name, scrapeSpec)
	if err != nil {
		return err
	}
	// 网络目标锚（"host" 名在服务创建时被 engine 归一为网络 ID 存储——
	// 幂等比对前把实况目标解析回名，同锚比较）。
	// ② 数据卷（本地命名卷——数据重力钉 manager）。
	if err := m.docker.VolumeEnsure(ctx, VolumeName); err != nil {
		return err
	}
	// ③ 三件期望 spec → 幂等收敛（采集器先于 VM——服务创建次序即切片序）。
	desired := map[string]swarm.ServiceSpec{
		VictoriaServiceName:     buildVictoriaSpec(platformID, m.retentionDays),
		CAdvisorServiceName:     buildCAdvisorSpec(),
		NodeExporterServiceName: buildNodeExporterSpec(),
	}
	for _, name := range managedServices() {
		want := desired[name]
		// 抓取配置引用在收敛期补 ConfigID（spec 构造保持纯函数——ID 是
		// 底座会话事实，不入权威字面）。
		anchorSpec(&want, scrapeID)
		cur, err := m.docker.ServiceInspect(ctx, name)
		if err != nil {
			return err
		}
		if cur.Exists {
			// 实况网络目标（ID 形态）解析回名后再比对（见 dockerPort.
			// NetworkName 注记）；解析失败显式退避重试，不误判漂移。
			for i, t := range cur.Networks {
				n, err := m.docker.NetworkName(ctx, t)
				if err != nil {
					return err
				}
				cur.Networks[i] = n
			}
		}
		reason := ""
		switch {
		case !cur.Exists:
			if err := m.docker.ServiceCreate(ctx, want); err != nil {
				return err
			}
			reason = "created"
			m.log.Info("metrics: service created",
				"service", name, "image", serviceImage(name))
		case !specEqual(cur, want):
			if err := m.docker.ServiceUpdate(ctx, name, cur.Version, want); err != nil {
				return err
			}
			reason = "updated"
			m.log.Info("metrics: service updated to desired spec (spec drift)",
				"service", name)
		}
		if reason != "" {
			m.emitEvent(ctx, "metrics.stack_deployed", "platform:metrics", map[string]string{
				"service": name,
				"image":   serviceImage(name),
				"reason":  reason,
			})
		}
	}
	// ④ 旧抓取 config GC（内容寻址换版后的遗留对象；best-effort——服务在
	//    用引用的 config 删除会被底座拒绝，此处只清真正失引用的旧版）。
	m.gcScrapeConfigs(ctx, scrapeSpec.Name)
	return nil
}

// hostNetworkName 是三件共用的宿主网络目标名（spec 权威字面；比对期把
// 实况存储的 ID 经 NetworkName 解析回名——dockerPort 注记）。
const hostNetworkName = "host"

// anchorSpec 把底座会话事实锚入期望 spec：抓取配置引用补 ConfigID
//（ConfigName 保持——实况投影按名比对；只写名会被 swarm 以 "malformed
// config reference" 拒绝，W3 secret-ID 同族教训，2026-09-22 dind 实证）。
func anchorSpec(spec *swarm.ServiceSpec, scrapeID string) {
	if cs := spec.TaskTemplate.ContainerSpec; cs != nil {
		for _, ref := range cs.Configs {
			if ref.ConfigName == scrapeConfigName() {
				ref.ConfigID = scrapeID
			}
		}
	}
}

// serviceImage 是三件服务的钉版镜像字面（日志/事件 payload 用——不 inspect，
// 期望即权威）。
func serviceImage(name string) string {
	switch name {
	case VictoriaServiceName:
		return DefaultVictoriaMetricsImage
	case CAdvisorServiceName:
		return DefaultCAdvisorImage
	case NodeExporterServiceName:
		return DefaultNodeExporterImage
	}
	return ""
}

// gcScrapeConfigs 清场失引用的旧抓取 config（带自描述 label 的对象里，
// 除当前版之外的全部；best-effort——失败只日志，不阻断收敛）。
func (m *Manager) gcScrapeConfigs(ctx context.Context, current string) {
	names, err := m.docker.ConfigListNames(ctx)
	if err != nil {
		m.log.Warn("metrics: scrape config gc list failed", "error", err)
		return
	}
	for _, n := range names {
		if n == current {
			continue
		}
		if err := m.docker.ConfigRemove(ctx, n); err != nil {
			m.log.Warn("metrics: scrape config gc remove failed", "config", n, "error", err)
		}
	}
}

// removeIfPresent 是 mode 离开 on 的清场（幂等）：三件服务在 → 移除 +
// 事件 metrics.stack_removed（payload 带 volume_retained=true）；抓取
// config 全部清场。数据卷**永不删除**（rustfs 禁用同型数据安全语义——
// 切回 on 复用卷，指标历史延续）。
func (m *Manager) removeIfPresent(ctx context.Context) error {
	removedAny := false
	// 删除序：VM 先（停抓取面）→ 采集器。
	for i := len(managedServices()) - 1; i >= 0; i-- {
		name := managedServices()[i]
		cur, err := m.docker.ServiceInspect(ctx, name)
		if err != nil {
			return err
		}
		if !cur.Exists {
			continue
		}
		if err := m.docker.ServiceRemove(ctx, name); err != nil {
			return err
		}
		removedAny = true
	}
	// 抓取 config 清场（服务已无引用；best-effort）。
	if names, err := m.docker.ConfigListNames(ctx); err == nil {
		for _, n := range names {
			if err := m.docker.ConfigRemove(ctx, n); err != nil {
				m.log.Warn("metrics: scrape config cleanup failed", "config", n, "error", err)
			}
		}
	} else {
		m.log.Warn("metrics: scrape config cleanup list failed", "error", err)
	}
	if !removedAny {
		return nil
	}
	m.log.Info("metrics: managed services removed (metrics.mode left on); "+
		"data volume retained — data survives; switch back to reattach it",
		"volume", VolumeName)
	m.emitEvent(ctx, "metrics.stack_removed", "platform:metrics", map[string]string{
		"volume_retained": "true",
	})
	return nil
}

// emitEvent 追加平台事件（Outbox 单写；失败只日志——事件披露不阻断收敛）。
func (m *Manager) emitEvent(ctx context.Context, name, subject string, payload map[string]string) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	err = m.store.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.AppendEvent(ctx, state.Event{Name: name, Subject: subject, Payload: string(raw)})
		return err
	})
	if err != nil {
		m.log.Warn("metrics: event append failed", "event", name, "error", err)
	}
}

// ComponentStatus 是单件托管服务的部署态投影（status 面消费）。
type ComponentStatus struct {
	Name string
	// Exists 报告服务是否在位（mode=on 且 Exists=false = 部署中/未部署
	// ——duty 退避收敛中）。
	Exists bool
	// Image 是实况镜像引用（不在位为空）。
	Image string
}

// DeploymentStatus 是三件部署态投影（CLI metrics status / RPC status 面）。
type DeploymentStatus struct {
	// Components 按固定序（cAdvisor、node_exporter、VM）投影三件实况。
	Components []ComponentStatus
}

// ComponentNames 是 status 面的固定投影序（采集器在前、VM 殿后——与收敛
// 次序一致）。
func ComponentNames() []string {
	return []string{CAdvisorServiceName, NodeExporterServiceName, VictoriaServiceName}
}

// DeploymentStatus 读取三件服务在位实况（只读面；底座不可达如实报错）。
func (m *Manager) DeploymentStatus(ctx context.Context) (DeploymentStatus, error) {
	out := DeploymentStatus{Components: []ComponentStatus{}}
	for _, name := range ComponentNames() {
		cur, err := m.docker.ServiceInspect(ctx, name)
		if err != nil {
			return DeploymentStatus{}, err
		}
		out.Components = append(out.Components, ComponentStatus{Name: name, Exists: cur.Exists, Image: cur.Image})
	}
	return out, nil
}

// CheckHealth 是 system status 组件检查器（metrics）的部署面：mode 非 on
// = 无所欠（健康——opt-in 缺省零常驻，验收标准 7）；mode=on 时三件应在位
// ——缺失即收敛未完成（duty 会继续收敛，红是过渡态的如实表达）。VM 健康
// 拨测在位后执行：不可达即红（查询面降级，采集面不受影响——诚实口径）。
func (m *Manager) CheckHealth() error {
	ctx, cancel := context.WithTimeout(context.Background(), settingsLoadTimeout)
	defer cancel()
	in, err := m.store.LoadMetricsSettings(ctx)
	if err != nil {
		return fmt.Errorf("metrics: load metrics settings: %w", err)
	}
	if in.Mode != state.MetricsModeOn {
		return nil
	}
	for _, name := range ComponentNames() {
		cur, err := m.docker.ServiceInspect(ctx, name)
		if err != nil {
			return fmt.Errorf("metrics: service inspect %s: %w", name, err)
		}
		if !cur.Exists {
			return fmt.Errorf("metrics: metrics.mode=on but service %s is not deployed yet (duty converging)", name)
		}
	}
	if m.health != nil {
		hctx, hcancel := context.WithTimeout(ctx, 2*time.Second)
		defer hcancel()
		if err := m.health(hctx); err != nil {
			return fmt.Errorf("metrics: health probe failed (query face degraded; scrape face unaffected): %w", err)
		}
	}
	return nil
}

// sleepCtx 睡眠直到 d 到期或 ctx 取消（返回 false = ctx 已取消）。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// retryOrScan 收敛失败/未收敛走短退避，已收敛走扫描周期（漂移复检节奏）。
func retryOrScan(retry, scan time.Duration, converged bool) time.Duration {
	if converged && scan > 0 {
		return scan
	}
	return retry
}
