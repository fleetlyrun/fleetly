package ingress

// Manager 是入口适配器门面：Traefik 收敛 + 配置端点 + 路由发布 +
// ACME 证书。发布引擎经 RoutePublisher 端口（PublishRoutes）消费；
// fleetlyd ingress 服务壳消费 EnsureTraefik/Run/Handler。

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"sort"
	"sync"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// PublishInput 是一次路由发布的输入（engine.RoutePublisher 契约的载荷；
// domains/port 来自归一化 compose——首健康后由引擎提取）。
type PublishInput struct {
	AppID   string
	AppName string
	// Services 是入口服务集（有 fleetly.domains label 的服务）。
	Services []ServiceRoutes
}

// ServiceRoutes 是单个入口服务的路由声明。
type ServiceRoutes struct {
	Service string
	// Port 是后端端口（compose expose 首端口）。
	Port    string
	Domains []string
}

// Manager 是入口与证书管理器。
type Manager struct {
	cfg    Config
	store  *state.Store
	docker dockerClient
	log    *slog.Logger
	// nowFunc 时钟出口（续期窗口单测注入）。
	nowFunc func() time.Time

	// vw 是配置视图（Validate 通过才换入）。
	vw *view
	// certs 是证书库。
	certs *certStore
	// cfgPortOverride 由服务壳在监听确定后回填（挑战应答 URL 依赖）。
	cfgPortOverride int
	// tokenOnce 缓存 token（生成/加载一次）。
	tokenOnce sync.Once
	tokenVal  string
	tokenErr  error
	// mu 保护 advertiseIP/lastSpec（EnsureTraefik 与 attachNetwork 的
	// 生产者-消费者）。
	mu          sync.Mutex
	advertiseIP string
	lastSpec    *swarm.ServiceSpec
	// user 是 ACME 账号缓存。
	user *acmeUser
}

// NewManager 构造入口管理器（cfg 缺省回落；docker client 按
// DOCKER_HOST/本机套接字构造）。返回 cleanup 释放 docker 连接。
func NewManager(cfg Config, store *state.Store, log *slog.Logger) (*Manager, func(), error) {
	norm := cfg.Normalize()
	if err := norm.Validate(); err != nil {
		return nil, nil, err
	}
	dc, err := newRealDockerClient("")
	if err != nil {
		return nil, nil, err
	}
	m := newManagerWithDocker(norm, store, dc, log)
	return m, func() { _ = dc.Close() }, nil
}

// NewManagerWithDocker 以注入的 dockerClient 构造（单测）。
func NewManagerWithDocker(cfg Config, store *state.Store, dc dockerClient, log *slog.Logger) *Manager {
	return newManagerWithDocker(cfg.Normalize(), store, dc, log)
}

func newManagerWithDocker(norm Config, store *state.Store, dc dockerClient, log *slog.Logger) *Manager {
	return &Manager{
		cfg:     norm,
		store:   store,
		docker:  dc,
		log:     log,
		nowFunc: time.Now().UTC,
		vw:      newView(""),
		certs:   newCertStore(norm.CertDir),
	}
}

// WithClock 注入时钟（单测）。
func (m *Manager) WithClock(f func() time.Time) *Manager { m.nowFunc = f; return m }

// Config 返回生效配置（诊断）。
func (m *Manager) Config() Config { return m.cfg }

// SetConfigPort 由服务壳在监听确定后回填（挑战应答 URL 依赖）。
func (m *Manager) SetConfigPort(port int) { m.cfgPortOverride = port }

// cfgPort 是配置端点端口（override 优先，ConfigAddr 解析兜底）。
func (m *Manager) cfgPort() int {
	if m.cfgPortOverride != 0 {
		return m.cfgPortOverride
	}
	if _, port, err := net.SplitHostPort(m.cfg.ConfigAddr); err == nil {
		var p int
		if _, scanErr := fmt.Sscanf(port, "%d", &p); scanErr == nil {
			return p
		}
	}
	return 8422
}

// token 加载或生成配置端点 token（幂等；缓存）。
func (m *Manager) token(_ context.Context) (string, error) {
	m.tokenOnce.Do(func() {
		m.tokenVal, _, m.tokenErr = tokenLoadOrGenerate(m.cfg.TokenFile)
	})
	return m.tokenVal, m.tokenErr
}

// setResponderURL / responderURLValue 是挑战应答基址的写读（EnsureTraefik
// 探测 advertise addr 后回填；view 的快照合成消费——只改基址字段，路由
// 与挑战态不受影响）。
func (m *Manager) setResponderURL(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vw.setResponder(url)
	if u, err := neturl.Parse(url); err == nil {
		m.advertiseIP = u.Hostname()
	}
}

func (m *Manager) responderURLValue() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vw.responderURL
}

// Handler 返回配置端点 HTTP handler（服务壳挂到独立内部端口——取舍见
// provider.go 顶部注释）。
func (m *Manager) Handler(ctx context.Context) (http.Handler, error) {
	token, err := m.token(ctx)
	if err != nil {
		return nil, err
	}
	return newProviderHandler(m.vw, token), nil
}

// Run 是周期任务：Traefik 收敛 + 全量重发布 + 证书续期扫描（sweep）。
// 由 fleetlyd ingress 服务壳调用（ctx 取消返回）。收敛失败只降级日志
// （下轮重试），不影响控制面其余服务。
func (m *Manager) Run(ctx context.Context) error {
	m.sweep(ctx)
	ticker := time.NewTicker(m.cfg.RenewScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.sweep(ctx)
		}
	}
}

// sweep 一轮收敛：Traefik 幂等收敛（失败降级）→ 全量重发布（控制面重启
// 的视图重建路径）→ 证书续期扫描。
func (m *Manager) sweep(ctx context.Context) {
	if err := m.EnsureTraefik(ctx); err != nil {
		m.log.Warn("ingress: traefik converge deferred (retry next sweep)", "error", err)
		return
	}
	if err := m.republishAll(ctx); err != nil {
		m.log.Warn("ingress: republish sweep failed", "error", err)
		return
	}
	if err := m.renewDue(ctx); err != nil {
		m.log.Warn("ingress: cert renewal scan failed", "error", err)
	}
}

// renewDue 续期扫描：台账内全部带域名 app，进入续期窗口（默认到期前
// 30 天）或证书缺失的重签。逐 app 失败不阻断其余（审计 error 行 + 日志；
// 不发明新事件名——事件纪律见 acme.go）。
func (m *Manager) renewDue(ctx context.Context) error {
	routes, err := m.routesFromStore(ctx)
	if err != nil {
		return err
	}
	byApp := map[string][]string{}
	appIDs := map[string]string{}
	for _, r := range routes {
		byApp[r.App] = append(byApp[r.App], r.Domains...)
		if _, ok := appIDs[r.App]; !ok {
			if appRow, err := m.store.GetAppByName(ctx, r.App); err == nil {
				appIDs[r.App] = appRow.ID
			}
		}
	}
	for _, app := range sortedKeys(byApp) {
		domains := uniqueSorted(byApp[app])
		if _, err := m.ensureCertificate(ctx, appIDs[app], app, domains, true); err != nil {
			m.log.Warn("ingress: cert renewal", "app", app, "error", err)
		}
	}
	return m.publishWithCerts(ctx)
}

// routesFromStore 从域名台账构建全量路由集（app × service 聚合）。
func (m *Manager) routesFromStore(ctx context.Context) ([]Route, error) {
	rows, err := m.store.ListAllDomains(ctx)
	if err != nil {
		return nil, err
	}
	return routesFromLedger(ctx, m.store, rows)
}

// publish 换入全量视图（HTTP 路由形态）：从台账构建路由集 → 合成 →
// Validate → 换入。校验不过（键缺失/合成空）= 不换视图、不落库
// （Spike B 纪律）。
func (m *Manager) publish(ctx context.Context) error {
	routes, err := m.routesFromStore(ctx)
	if err != nil {
		return err
	}
	cfg := Synthesize(routes)
	if err := Validate(cfg); err != nil {
		// 全空视图（最后一个入口服务被移除）也在此拒绝：Traefik 侧保留
		// 上一份好配置（显式空 map 载荷会被 Traefik 拒绝，效果等价）；
		// 事实经调用方告警/审计披露——不发明静默清空路径。
		return err
	}
	m.vw.setRoutes(routes)
	return nil
}

// PublishRoutes 实现 engine.RoutePublisher：域名台账对账 → Traefik 收敛
// 与网络接入 → 全量配置收敛（先校验后换视图）→ 证书保障（缺/域名集变化/
// 临期即签发；签发失败不阻断已生效的 HTTP 路由，TLS 段随下次成功签发
// 收敛）。
//
// 语义注释（T2.15 设计取舍）：发布失败的处置权在调用方（引擎）——部署
// 不回滚、route.publish_failed 单独告警 + 审计；本方法自身是幂等收敛
// （重调即重试）。
func (m *Manager) PublishRoutes(ctx context.Context, in PublishInput) error {
	if in.AppName == "" {
		return fmt.Errorf("ingress: publish requires app name")
	}
	// ① 台账对账（声明集 = 本次部署归一化 compose 的 domains；服务移除
	// /域名撤销 = 声明集之外的行删除——「省略 = 删除」的路由面）。
	declared := make([]state.DomainServiceRoutes, 0, len(in.Services))
	for _, svc := range in.Services {
		declared = append(declared, state.DomainServiceRoutes{
			Service: svc.Service,
			Port:    svc.Port,
			Domains: append([]string{}, svc.Domains...),
		})
	}
	if err := m.store.ReplaceAppDomains(ctx, in.AppID, declared); err != nil {
		return err
	}
	// ② Traefik 收敛 + app 网络接入（swarm 未就绪显式失败——引擎侧降级
	// 告警语义的输入；不猜测底座状态）。
	if err := m.EnsureTraefik(ctx); err != nil {
		return err
	}
	if len(in.Services) > 0 {
		if err := m.attachNetwork(ctx, in.AppName); err != nil {
			return err
		}
	}
	// ③ 全量配置收敛（全量视图语义：其他 app 的路由同盘——单应用坏配置
	// 不影响其他应用路由的边界在本方法的 Validate/合成层面成立）。
	if err := m.publish(ctx); err != nil {
		return err
	}
	// ④ 证书保障：本 app 域名集的证书缺/变/临期即签发（HTTP-01 完整链；
	// 失败返回错误——HTTP 路由已在③生效）。成功后带 TLS 段重发布一次。
	domains := collectDomainsOf(in)
	if len(domains) > 0 {
		if _, err := m.ensureCertificate(ctx, in.AppID, in.AppName, domains, false); err != nil {
			return err
		}
		if err := m.publishWithCerts(ctx); err != nil {
			return err
		}
	}
	return nil
}

// publishWithCerts 全量重发布（带证书段）：证书库就绪的 app 路由挂
// CertificateRef（443 路由 + tls.certificates）；无证书 app 仅 HTTP。
func (m *Manager) publishWithCerts(ctx context.Context) error {
	routes, err := m.routesFromStore(ctx)
	if err != nil {
		return err
	}
	withCerts := make([]Route, 0, len(routes))
	apps := map[string]bool{}
	for _, r := range routes {
		apps[r.App] = true
		withCerts = append(withCerts, r)
	}
	for _, app := range sortedKeys(apps) {
		pair, err := m.certs.Load(app)
		if err != nil {
			continue // 无证书 app：仅 HTTP 路由（Load 缺失/损坏均不阻断）
		}
		for i := range withCerts {
			if withCerts[i].App == app {
				withCerts[i].Cert = &CertificateRef{App: app, SHA256: pair.SHA256, NotAfter: pair.NotAfter.UnixNano()}
			}
		}
	}
	cfg := Synthesize(withCerts)
	if err := Validate(cfg); err != nil {
		return err
	}
	m.vw.setRoutes(withCerts)
	return nil
}

// republishAll 是 sweep 的全量重发布（HTTP 段 + 证书段；幂等）。
func (m *Manager) republishAll(ctx context.Context) error {
	if err := m.publish(ctx); err != nil {
		return err
	}
	return m.publishWithCerts(ctx)
}

// Status 是入口状态投影（fleetlyd 诊断）。
type Status struct {
	Traefik     ingressServiceState
	AdvertiseIP string
	Responder   string
}

// Status 读取入口服务实况（Traefik 侧；配置端点健康经 Handler 面探测）。
func (m *Manager) Status(ctx context.Context) (Status, error) {
	cur, err := m.docker.ServiceInspect(ctx, IngressServiceName)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Traefik:     cur,
		AdvertiseIP: m.advertiseIP,
		Responder:   m.vw.responderURL,
	}, nil
}

// collectDomainsOf 收集发布输入的全部域名（排序去重；多 SAN 单证书/app）。
func collectDomainsOf(in PublishInput) []string {
	var out []string
	for _, svc := range in.Services {
		out = append(out, svc.Domains...)
	}
	return uniqueSorted(out)
}

// routesFromLedger 把台账行聚合成路由集（app 名经 GetAppByID 解析；
// tombstone/删除中的 app 路由不发布）。确定性：按 (app, service) 字典序。
func routesFromLedger(ctx context.Context, st *state.Store, rows []state.Domain) ([]Route, error) {
	nameByAppID := map[string]string{}
	type key struct{ app, service string }
	order := []key{}
	byKey := map[key]*Route{}
	for _, row := range rows {
		if _, ok := nameByAppID[row.AppID]; !ok {
			appRow, err := st.GetAppByID(ctx, row.AppID)
			if err != nil {
				nameByAppID[row.AppID] = "" // 不可解析（已删除）：路由不发布
				continue
			}
			nameByAppID[row.AppID] = appRow.Name
		}
		app := nameByAppID[row.AppID]
		if app == "" {
			continue
		}
		k := key{app: app, service: row.Service}
		r, exists := byKey[k]
		if !exists {
			r = &Route{App: app, Service: row.Service, Port: row.Port}
			byKey[k] = r
			order = append(order, k)
		}
		r.Domains = append(r.Domains, row.Domain)
	}
	out := make([]Route, 0, len(order))
	sort.Slice(order, func(i, j int) bool {
		if order[i].app != order[j].app {
			return order[i].app < order[j].app
		}
		return order[i].service < order[j].service
	})
	for _, k := range order {
		r := byKey[k]
		r.Domains = uniqueSorted(r.Domains)
		out = append(out, *r)
	}
	return out, nil
}

// uniqueSorted 去重排序。
func uniqueSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// sortedKeys map 键字典序。
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
