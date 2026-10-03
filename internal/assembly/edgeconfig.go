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

// NewEdgeConfigServer 装配受管 Edge 配置拉取端点（GET /edge/config 返回
// 全量动态配置；traefik 按 pollInterval 拉取——控制面是配置真源，端点
// 永不返回裸 {}，防配置清空事故）。监听地址经 config.server.edge_config.addr
// 可配置（缺省 ":9082" = 现状，ADR-0036）；auth_token 非空时要求
// X-Fleetly-Edge-Token 头常量时间比对（traefik 经 providers.http.headers
// 同头携带；空 = 无认证现状——收窄是显式动作，升级零扰动，ADR-0036 N2
// 兑现）。
// 数据面经 capability.ConfigSource 子面（providers 不被 assembly 直接
// import，守卫见 internal/guards）。
func NewEdgeConfigServer(app lynx.App, cfg *config.AppConfig, edge capability.Edge) (*EdgeConfigServer, error) {
	if edge == nil {
		return nil, nil // Edge 未装配：无拉取端点（受管面停用的诚实降级）
	}
	src := capability.FacesOf(edge).ConfigSource // 配置源子面（FacesOf 协商点）
	if src == nil {
		return nil, fmt.Errorf("assembly: edge provider %s does not expose a config snapshot", edge.Describe().Name)
	}
	authToken := cfg.EdgeConfigAuthToken()
	srv := lynxhttp.NewServer(newEdgeConfigMux(src, authToken),
		lynxhttp.WithAddr(cfg.EdgeConfigAddr()),
		lynxhttp.WithLogger(app.Logger()),
		lynxhttp.WithServerOptions(func(s *http.Server) {
			s.ReadTimeout = 10 * time.Second
			s.WriteTimeout = 10 * time.Second
		}),
		lynxhttp.WithMiddleware(lynxhttp.Recovery()),
	)
	return &EdgeConfigServer{Server: srv}, nil
}

// newEdgeConfigMux 构造拉取端点 handler（hermetic 可测面：src 是配置源
// 子面，authToken 空 = 无认证）。空快照回退非裸 {} 形态——防配置清空
// 事故的端点侧不变量。
func newEdgeConfigMux(src capability.ConfigSource, authToken string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/edge/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if authToken != "" {
			got := r.Header.Get(edgeAuthTokenHeader)
			if subtle.ConstantTimeCompare([]byte(got), []byte(authToken)) != 1 {
				http.Error(w, "edge config endpoint requires a valid token (X-Fleetly-Edge-Token)", http.StatusUnauthorized)
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

// edgeAuthTokenHeader 是拉取端点共享令牌头（traefik 受管实例的
// --providers.http.headers 同名键；值域约定两端同源）。
const edgeAuthTokenHeader = "X-Fleetly-Edge-Token" //nolint:gosec // G101 误报：头名非机密（值经 config 注入运行时比对）

// EdgeConfigServer 是拉取端点的服务形态（wire 类型键：与 gateway 同为
// *lynxhttp.Server，独立类型避免 wire 参数歧义）。
type EdgeConfigServer struct {
	*lynxhttp.Server
}

// NewEdgeProvider 经工厂注册表构造 Edge Provider（cmd/fleetlyd blank
// import 触发 traefik 自注册）。令牌经装配 ctx 注入
// （config.server.edge_config.auth_token 优先，env FLEETLY_EDGE_AUTH_TOKEN
// 同键兜底，ADR-0036 形态）。无在册者或未配置端点时返回 nil——Edge 面
// 停用是诚实降级（存量路由语义不适用 N0 骨架；安装引导批配置端点后
// 启用），不拖死启动。
func NewEdgeProvider(app lynx.App, cfg *config.AppConfig) (capability.Edge, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindEdge]) == 0 {
		return nil, func() {}, nil
	}
	ctx := capability.WithEdgeAuthToken(context.Background(), cfg.EdgeConfigAuthToken())
	p, err := capability.Build(ctx, capability.KindEdge, "")
	if err != nil {
		app.Logger().Warn("edge provider unavailable; route publishing disabled", "err", err)
		return nil, func() {}, nil
	}
	edge, ok := p.(capability.Edge)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Edge port", p.Describe().Name)
	}
	logCapabilityFaces(app.Logger(), "edge", edge)
	return edge, func() {}, nil
}
