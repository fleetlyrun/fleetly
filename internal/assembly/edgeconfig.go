package assembly

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/lynx-go/lynx"
	lynxhttp "github.com/lynx-go/lynx/server/http"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// DefaultEdgeConfigAddr 是受管 Edge 拉取动态配置的内部端点（traefik
// --providers.http.endpoint 的目标；独立于公共 REST 面——这是平台与受管
// 组件的私有通道，不进公共 API 契约。配置面接入 config.proto 后可覆盖）。
const DefaultEdgeConfigAddr = ":9082"

// NewEdgeConfigServer 装配受管 Edge 配置拉取端点（GET /edge/config 返回
// 全量动态配置；traefik 按 pollInterval 拉取——控制面是配置真源，端点
// 永不返回裸 {}，防配置清空事故）。数据面经 capability.ConfigSource
// 子面（providers 不被 assembly 直接 import，守卫见 internal/guards）。
func NewEdgeConfigServer(app lynx.App, cfg *config.AppConfig, edge capability.Edge) (*EdgeConfigServer, error) {
	if edge == nil {
		return nil, nil // Edge 未装配：无拉取端点（受管面停用的诚实降级）
	}
	src, ok := edge.(capability.ConfigSource)
	if !ok {
		return nil, fmt.Errorf("assembly: edge provider %s does not expose a config snapshot", edge.Describe().Name)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/edge/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body := src.ConfigSnapshot()
		if len(body) == 0 {
			body = []byte("{\n  \"http\": {\n    \"routers\": {},\n    \"services\": {}\n  }\n}\n")
		}
		_, _ = w.Write(body) //nolint:errcheck // 只读快照写出，错误无处置面
	})
	srv := lynxhttp.NewServer(mux,
		lynxhttp.WithAddr(DefaultEdgeConfigAddr),
		lynxhttp.WithLogger(app.Logger()),
		lynxhttp.WithServerOptions(func(s *http.Server) {
			s.ReadTimeout = 10 * time.Second
			s.WriteTimeout = 10 * time.Second
		}),
		lynxhttp.WithMiddleware(lynxhttp.Recovery()),
	)
	return &EdgeConfigServer{Server: srv}, nil
}

// EdgeConfigServer 是拉取端点的服务形态（wire 类型键：与 gateway 同为
// *lynxhttp.Server，独立类型避免 wire 参数歧义）。
type EdgeConfigServer struct {
	*lynxhttp.Server
}

// NewEdgeProvider 经工厂注册表构造 Edge Provider（cmd/fleetlyd blank
// import 触发 traefik 自注册；端点/邮箱经环境变量注入，配置面随 API 批
// 次落 config.proto）。无在册者或未配置端点时返回 nil——Edge 面停用是
// 诚实降级（存量路由语义不适用 N0 骨架；安装引导批配置端点后启用），
// 不拖死启动。
func NewEdgeProvider(app lynx.App) (capability.Edge, func(), error) {
	providers := capability.RegisteredFactories()
	if len(providers[capability.KindEdge]) == 0 {
		return nil, func() {}, nil
	}
	p, err := capability.Build(context.Background(), capability.KindEdge, "")
	if err != nil {
		app.Logger().Warn("edge provider unavailable; route publishing disabled", "err", err)
		return nil, func() {}, nil
	}
	edge, ok := p.(capability.Edge)
	if !ok {
		return nil, nil, fmt.Errorf("assembly: provider %s does not implement the Edge port", p.Describe().Name)
	}
	return edge, func() {}, nil
}
