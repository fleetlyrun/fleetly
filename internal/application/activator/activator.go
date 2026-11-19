// Package activator 是唤醒组件的请求面本体（P2-1a 第三刀，设计 v2
// §1.2/§1.4）：缩零期请求的扣留 + 唤醒 + 就绪转发。三段式语义（AWS Web
// Functions 模板）：唤醒中扣留（deadline 内透传响应）、超限 429、终态
// 失败 503。X-Fleetly-Wake 自证头标注冷路径（e2e「退出数据路径」断言锚）。
//
// 职责边界（设计 §1.2）：不做鉴权、不做载荷处理、不做协议理解——收到
// 请求原样代发（method/path/body/header 逐字透传，调用语义归函数自己）。
// 唤醒写径与后端解析经三个窄接口（控制面 gRPC 客户端实现——activator
// 永不触 Runtime、永不自拼载体名）。
package activator

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// Config 是唤醒面的行为参数（缺省值见 New；引擎设置面旋钮的注入点）。
type Config struct {
	// WakeDeadline 是扣留窗上限（超时 → 503 + Retry-After）。
	WakeDeadline time.Duration
	// MaxHeldPerFunction 是 per-function 并发扣留上限（超限 → 429）。
	MaxHeldPerFunction int
	// PollInterval 是就绪轮询节拍（控制面真值：ResolveFunctionBackend）。
	PollInterval time.Duration
	// BodyLimit 是扣留请求体上限（超体 → 413——大载荷函数应 min≥1，
	// 诚实降级文档化，设计 v2 §1.4）。
	BodyLimit int64
}

func (c *Config) fill() {
	if c.WakeDeadline <= 0 {
		c.WakeDeadline = 30 * time.Second
	}
	if c.MaxHeldPerFunction <= 0 {
		c.MaxHeldPerFunction = 64
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 500 * time.Millisecond
	}
	if c.BodyLimit <= 0 {
		c.BodyLimit = 4 << 20 // 4 MiB
	}
}

// HostResolver 把请求 Host 解析为函数 ID（public host → 行映射；控制面
// 面数据——activator 不自持函数表）。
type HostResolver interface {
	// FunctionIDForHost 返回该 host 对应的活跃函数 ID；未命中 = ""（404）。
	FunctionIDForHost(ctx context.Context, host string) (string, error)
}

// Waker 是唤醒窄面（控制面 WakeFunction 动词的本地投影——条件 CAS +
// Kick；幂等：已醒 no-op）。
type Waker interface {
	Wake(ctx context.Context, functionID string) error
}

// BackendResolver 是后端解析窄面（控制面 ResolveFunctionBackend 的本地
// 投影——parked→ready=false；醒后端点 host:port）。
type BackendResolver interface {
	Resolve(ctx context.Context, functionID string) (addr string, ready bool, err error)
}

// Handler 是唤醒组件的 HTTP 面。并发安全；零外部状态（扣留计数在内存）。
type Handler struct {
	cfg     Config
	hosts   HostResolver
	wake    Waker
	resolve BackendResolver
	client  *http.Client

	heldMu sync.Mutex
	held   map[string]int // functionID → 在途扣留数（429 门）
}

// New 构造唤醒面（cfg 零值字段取缺省）。
func New(cfg Config, hosts HostResolver, wake Waker, resolve BackendResolver) *Handler {
	cfg.fill()
	return &Handler{
		cfg:     cfg,
		hosts:   hosts,
		wake:    wake,
		resolve: resolve,
		client:  &http.Client{Timeout: 0}, // 转发超时归函数语义（调用方中断为准）
		held:    make(map[string]int),
	}
}

// ServeHTTP 是唤醒请求面：Host → 函数 → （已醒即直转；parked 即扣留
// 唤醒三段式）。出口四形态：透传响应（X-Fleetly-Wake 头）/429/413/503。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fnID, err := h.hosts.FunctionIDForHost(ctx, r.Host)
	if err != nil {
		http.Error(w, "activator: host lookup failed", http.StatusBadGateway)
		return
	}
	if fnID == "" {
		http.Error(w, "activator: no function for host", http.StatusNotFound)
		return
	}

	// 快径：已醒（解析面 ready）即直转——扣留计数不占、零冷路径头。
	if addr, ready, rerr := h.resolve.Resolve(ctx, fnID); rerr == nil && ready {
		h.forward(w, r, addr, false)
		return
	}

	// 体帽（413）：扣留前截断——大载荷占内存乘以扣留数是放大器。
	if r.ContentLength > h.cfg.BodyLimit {
		w.Header().Set("Retry-After", "0")
		http.Error(w, "activator: request body too large to hold during wake; keep min_replicas >= 1 for large payloads", http.StatusRequestEntityTooLarge)
		return
	}

	// 扣留帽（429）：per-function 在途上限——唤醒风暴防线（N 并发请求
	// 共享同一次唤醒，超出部分快速失败不占资源）。
	if !h.acquire(fnID) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "activator: wake already in progress for this function; retry shortly", http.StatusTooManyRequests)
		return
	}
	defer h.release(fnID)

	// 唤醒拍（幂等 CAS——N 个扣留请求共享同一次落账）。
	if werr := h.wake.Wake(ctx, fnID); werr != nil {
		http.Error(w, "activator: wake request failed", http.StatusServiceUnavailable)
		return
	}

	// 扣留窗：轮询控制面真值至就绪或 deadline（503 + Retry-After）。
	deadline := time.Now().Add(h.cfg.WakeDeadline)
	for {
		addr, ready, rerr := h.resolve.Resolve(ctx, fnID)
		if rerr == nil && ready {
			h.forward(w, r, addr, true)
			return
		}
		if time.Now().After(deadline) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "activator: function did not become ready in time; retry", http.StatusServiceUnavailable)
			return
		}
		select {
		case <-ctx.Done():
			return // 调用方断开：扣留无意义
		case <-time.After(h.cfg.PollInterval):
		}
	}
}

// forward 把请求原样代发到解析端点（method/path/body/header 逐字透传；
// cold=true 时加 X-Fleetly-Wake 自证头——e2e 冷/热路径断言锚）。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, addr string, cold bool) {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.Host = r.Host
		if cold {
			pr.Out.Header.Set("X-Fleetly-Wake", "1")
		}
	}}
	proxy.ServeHTTP(w, r)
}

func (h *Handler) acquire(fnID string) bool {
	h.heldMu.Lock()
	defer h.heldMu.Unlock()
	if h.held[fnID] >= h.cfg.MaxHeldPerFunction {
		return false
	}
	h.held[fnID]++
	return true
}

func (h *Handler) release(fnID string) {
	h.heldMu.Lock()
	defer h.heldMu.Unlock()
	if h.held[fnID] <= 1 {
		delete(h.held, fnID)
		return
	}
	h.held[fnID]--
}

// HeldCount 暴露 per-function 在途扣留数（观测/测试面）。
func (h *Handler) HeldCount(fnID string) int {
	h.heldMu.Lock()
	defer h.heldMu.Unlock()
	return h.held[fnID]
}
