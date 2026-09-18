package ingress

// 配置视图：控制面侧的「当前好配置」内存态。发布路径先合成、Validate
// 通过才换入（Spike B 纪律：坏配置不出控制面）；Traefik 经 HTTP provider
// 轮询 /configs 时取到的永远是视图当前值（视图为空/未发布 → 显式空 map
// ——Traefik 拒绝该载荷并保留其侧既有配置，等价「不动」语义，实测）。
//
// 控制面重启后视图由状态库域名台账重建（manager 启动 sweep）——Traefik 侧
// 「保留旧配置」的窗口被压缩到一次轮询周期。

import (
	"sync"
	"sync/atomic"
	"time"
)

// acmeChallengeRouterName / acmeChallengeServiceName 是挑战路由/服务键
// （挑战路径反代到控制面应答端点，架构 §2.6 证书行）。
const (
	acmeChallengeRouterName  = "fleetly-acme-challenge"
	acmeChallengeServiceName = "fleetly-acme-responder"
)

// view 是线程安全配置视图（发布单写者 + provider 多读者 + ACME 挑战
// Present/CleanUp 并发覆盖）。
type view struct {
	mu sync.RWMutex
	// routes 是当前路由集（视图重建的确定性输入）。
	routes []Route
	// responderURL 是控制面挑战应答端点的可达 URL（Traefik 视角，
	// http://<宿主 IP>:<配置端点端口>）。
	responderURL string
	// challenges 是在途 ACME 挑战（token → keyAuth）；Present 注入、
	// CleanUp 移除。挑战路由/服务在 snapshot 时叠加。
	challenges map[string]string
	// revision 单调递增：每次路由/挑战变更 +1；provider 记录 Traefik
	// 最近一次取走的 revision（Present 等待收敛的依据——挑战路由必须
	// 先于 CA 校验请求到达 Traefik）。
	revision atomic.Int64
	// servedRevision 是 Traefik 最近一次成功拉取的 revision。
	servedRevision atomic.Int64
	// servedEver 记录是否有 Traefik 来取过配置（false = 入口尚未接管）。
	servedEver atomic.Bool
}

func newView(responderURL string) *view {
	return &view{challenges: map[string]string{}, responderURL: responderURL}
}

// setResponder 更新挑战应答基址（EnsureTraefik 探测 advertise addr 后
// 回填；只改基址字段，路由/挑战态不受影响）。
func (v *view) setResponder(url string) {
	v.mu.Lock()
	v.responderURL = url
	v.mu.Unlock()
}

// setRoutes 换入路由集并推进 revision（调用方保证已 Validate）。
func (v *view) setRoutes(routes []Route) {
	v.mu.Lock()
	v.routes = append([]Route{}, routes...)
	v.mu.Unlock()
	v.revision.Add(1)
}

// currentRevision 返回当前 revision。
func (v *view) currentRevision() int64 { return v.revision.Load() }

// snapshot 合成当前应答载荷：路由快照 + 在途挑战叠加。挑战存在时载荷
// 追加挑战 router/service（PathPrefix 显式优先级 1000 压过 host 路由的
// 规则长度优先级——挑战路径必须命中应答端点）。
func (v *view) snapshot() *DynamicConfig {
	v.mu.RLock()
	routes := append([]Route{}, v.routes...)
	challenges := make(map[string]string, len(v.challenges))
	for k, keyAuth := range v.challenges {
		challenges[k] = keyAuth
	}
	responder := v.responderURL
	v.mu.RUnlock()

	cfg := Synthesize(routes)
	if len(challenges) == 0 {
		return cfg
	}
	cfg.HTTP.Routers[acmeChallengeRouterName] = &Router{
		Rule:        "PathPrefix(`" + acmeChallengePathPrefix + "`)",
		EntryPoints: []string{"web"},
		Service:     acmeChallengeServiceName,
		Priority:    1000,
	}
	cfg.HTTP.Services[acmeChallengeServiceName] = &DynamicService{
		LoadBalancer: &LoadBalancer{
			Servers:          []Server{{URL: responder}},
			ServersTransport: defaultServersTransportName + "@http",
		},
	}
	return cfg
}

// addChallenge 注入挑战令牌并返回注入时的 revision（Present 等待收敛用）。
func (v *view) addChallenge(token, keyAuth string) int64 {
	v.mu.Lock()
	v.challenges[token] = keyAuth
	v.mu.Unlock()
	return v.revision.Add(1)
}

// removeChallenge 移除挑战令牌（CleanUp；幂等）。
func (v *view) removeChallenge(token string) {
	v.mu.Lock()
	delete(v.challenges, token)
	v.mu.Unlock()
	v.revision.Add(1)
}

// challengeKeyAuth 查询挑战令牌对应 keyAuth（应答端点）。
func (v *view) challengeKeyAuth(token string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	keyAuth, ok := v.challenges[token]
	return keyAuth, ok
}

// markServed 记录 Traefik 拉走的 revision。
func (v *view) markServed() {
	v.servedRevision.Store(v.currentRevision())
	v.servedEver.Store(true)
}

// awaitServed 等待 Traefik 取走 ≥ rev 的配置（Present 后的收敛门：
// Traefik 轮询节奏 + 预算；入口从未接管过时立即返回 false——挑战必然
// 失败，由 lego 上报，不给死锁）。返回 false = 超时未收敛。
func (v *view) awaitServed(rev int64, budget time.Duration) bool {
	if !v.servedEver.Load() {
		return false
	}
	deadline := time.Now().Add(budget)
	for v.servedRevision.Load() < rev {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(80 * time.Millisecond)
	}
	return true
}

// EmptyConfig 是显式空 map 载荷（视图未发布时的应答：Traefik 拒绝该载荷
// 并保留其侧既有配置——「控制面没有好配置时不动入口」语义，Spike B b1
// 实测「键在值空」的 decode 拒绝路径；裸 {} 的清空形态永不出本包）。
func EmptyConfig() *DynamicConfig {
	return &DynamicConfig{HTTP: &HTTPDynamic{
		Routers:  map[string]*Router{},
		Services: map[string]*DynamicService{},
	}}
}
