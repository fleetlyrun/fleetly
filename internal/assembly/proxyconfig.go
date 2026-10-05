package assembly

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"time"

	"github.com/lynx-go/lynx"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// NewProxyConfigServer 装配受管 Proxy 配置拉取端点（GET /proxy/config 返回
// 全量动态配置；traefik 按 pollInterval 拉取——控制面是配置真源，端点
// 永不返回裸 {}，防配置清空事故）。监听地址经 config.server.proxy_config.addr
// 可配置（缺省 ":9082" = 现状，ADR-0036）；auth_token 非空时要求
// X-Fleetly-Proxy-Token 头常量时间比对（traefik 经 providers.http.headers
// 同头携带；空 = 无认证现状——收窄是显式动作，升级零扰动，ADR-0036 N2
// 兑现）。
// 数据面经 capability.ConfigSource 子面（providers 不被 assembly 直接
// import，守卫见 internal/guards）。
func NewProxyConfigServer(app lynx.App, cfg *config.AppConfig, proxy capability.Proxy) (*ProxyConfigServer, error) {
	if proxy == nil {
		return nil, nil // Proxy 未装配：无拉取端点（受管面停用的诚实降级）
	}
	src := capability.FacesOf(proxy).ConfigSource // 配置源子面（FacesOf 协商点）
	if src == nil {
		return nil, fmt.Errorf("assembly: proxy provider %s does not expose a config snapshot", proxy.Describe().Name)
	}
	authToken := cfg.ProxyConfigAuthToken()
	srv := lynxhttp.NewServer(newProxyConfigMux(src, authToken),
		lynxhttp.WithAddr(cfg.ProxyConfigAddr()),
		lynxhttp.WithLogger(app.Logger()),
		lynxhttp.WithServerOptions(func(s *http.Server) {
			s.ReadTimeout = 10 * time.Second
			s.WriteTimeout = 10 * time.Second
		}),
		lynxhttp.WithMiddleware(lynxhttp.Recovery()),
	)
	return &ProxyConfigServer{Server: srv}, nil
}

// newProxyConfigMux 构造拉取端点 handler（hermetic 可测面：src 是配置源
// 子面，authToken 空 = 无认证）。空快照回退非裸 {} 形态——防配置清空
// 事故的端点侧不变量。
func newProxyConfigMux(src capability.ConfigSource, authToken string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if authToken != "" {
			got := r.Header.Get(proxyAuthTokenHeader)
			if subtle.ConstantTimeCompare([]byte(got), []byte(authToken)) != 1 {
				http.Error(w, "proxy config endpoint requires a valid token (X-Fleetly-Proxy-Token)", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		body := src.ConfigSnapshot()
		if len(body) == 0 {
			body = []byte("{\n  \"http\": {\n    \"routers\": {},\n    \"services\": {}\n  }\n}\n")
		}
		_, _ = w.Write(body) //nolint:errcheck // 只读快照写出，错误无处置面
	})
	return mux
}

// proxyAuthTokenHeader 是拉取端点共享令牌头（traefik 受管实例的
// --providers.http.headers 同名键；值域约定两端同源）。
const proxyAuthTokenHeader = "X-Fleetly-Proxy-Token" //nolint:gosec // G101 误报：头名非机密（值经 config 注入运行时比对）

// ProxyConfigServer 是拉取端点的服务形态（wire 类型键：与 gateway 同为
// *lynxhttp.Server，独立类型避免 wire 参数歧义）。
type ProxyConfigServer struct {
	*lynxhttp.Server
}

// NewProxyProvider 经工厂注册表构造 Proxy Provider（cmd/fleetlyd blank
// import 触发 traefik 自注册）。令牌经装配 ctx 注入
// （config.server.proxy_config.auth_token 优先，env FLEETLY_PROXY_AUTH_TOKEN
// 同键兜底，ADR-0036 形态）。无在册者或未配置端点时返回 nil——Proxy 面
// 停用是诚实降级（存量路由语义不适用 N0 骨架；安装引导批配置端点后
// 启用），不拖死启动。
func NewProxyProvider(app lynx.App, cfg *config.AppConfig) (capability.Proxy, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindProxy]) == 0 {
		return nil, func() {}, nil
	}
	ctx := capability.WithProxyAuthToken(context.Background(), cfg.ProxyConfigAuthToken())
	p, err := capability.Build(ctx, capability.KindProxy, "")
	if err != nil {
		app.Logger().Warn("proxy provider unavailable; route publishing disabled", "err", err)
		return nil, func() {}, nil
	}
	proxy, ok := p.(capability.Proxy)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Proxy port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "proxy", proxy)
	return proxy, func() {}, nil
}
