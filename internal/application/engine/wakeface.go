package engine

// wakeface.go 是唤醒组件的内部控制面（P2-1a 部署切片；9082 HTTP 服务器
// 同族的免认证内网面——auth posture 与 proxy config 端点一致：overlay 内
// 端点，auth_token 收窄为显式动作）。三端点：host→函数映射、唤醒 CAS、
// 后端解析。宿主 = 装配侧 NewWakeFaceServer（proxyconfig.go 同族）。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// WakeFaceServer 是唤醒组件内部控制面的 handler 集（装配侧挂 mux）。
type WakeFaceServer struct {
	wake    func(ctx context.Context, functionID string) (bool, error)
	resolve func(ctx context.Context, functionID string) (addr string, ready bool, err error)
	hostOf  func(ctx context.Context, host string) (string, error)
}

// NewWakeFaceServer 装配三端点（核门面闭包注入——activator 永不自拼载体名，
// 解析面走引擎单源）。
func NewWakeFaceServer(
	wake func(ctx context.Context, functionID string) (bool, error),
	resolve func(ctx context.Context, functionID string) (string, bool, error),
	hostOf func(ctx context.Context, host string) (string, error),
) *WakeFaceServer {
	return &WakeFaceServer{wake: wake, resolve: resolve, hostOf: hostOf}
}

// Register 挂载三端点到 mux（装配侧 mux 复用——9082 服务器同族）。
func (s *WakeFaceServer) Register(mux *http.ServeMux) {
	mux.HandleFunc("/activator/host/", s.handleHost)
	mux.HandleFunc("/activator/wake/", s.handleWake)
	mux.HandleFunc("/activator/backend/", s.handleResolve)
}

func (s *WakeFaceServer) handleHost(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimPrefix(r.URL.Path, "/activator/host/")
	if host == "" {
		http.Error(w, "host: must not be empty", http.StatusBadRequest)
		return
	}
	fnID, err := s.hostOf(r.Context(), host)
	if err != nil {
		http.Error(w, "host mapping failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"function_id": fnID})
}

func (s *WakeFaceServer) handleWake(w http.ResponseWriter, r *http.Request) {
	fnID := strings.TrimPrefix(r.URL.Path, "/activator/wake/")
	if fnID == "" {
		http.Error(w, "function_id: must not be empty", http.StatusBadRequest)
		return
	}
	woken, err := s.wake(r.Context(), fnID)
	if err != nil {
		http.Error(w, "wake failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"woken": woken})
}

func (s *WakeFaceServer) handleResolve(w http.ResponseWriter, r *http.Request) {
	fnID := strings.TrimPrefix(r.URL.Path, "/activator/backend/")
	if fnID == "" {
		http.Error(w, "function_id: must not be empty", http.StatusBadRequest)
		return
	}
	addr, ready, err := s.resolve(r.Context(), fnID)
	if err != nil {
		http.Error(w, "resolve failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ready": ready, "addr": addr})
}
