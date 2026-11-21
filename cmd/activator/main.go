// activator 是唤醒组件守护进程（P2-1a 部署切片，设计 v2 §1.2/§1.6）：
// 缩零期函数的请求扣留面。部署形态 = 平台受管 Workload（manageds-decl：
// 系统域单载体、attachNetwork 挂全部活跃项目网），三窄接口经 9082 内网
// 面（wake face 三端点——免认证内网面与 proxy config 同 posture）对接
// 控制面。activator 永不触 Runtime、永不自拼载体名——转发目标经引擎
// 解析面下发。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/activator"
)

func main() {
	// 控制面地址 = 9082 内网面（wake face 三端点宿主——免认证内网面与
	// proxy config 同 posture）。FLEETLY_ADDR 经受管投影 env 注入，装配侧
	// 已派生为带 scheme 的完整 URL（http://<host>:9082）——兼容裸 host:port
	// 形态（双 scheme 前缀曾致 DNS lookup "http"——真机实证 2026-11-21）。
	cpAddr := os.Getenv("FLEETLY_ADDR")
	if cpAddr == "" {
		cpAddr = "127.0.0.1:9082"
	}
	inner := cpAddr
	if !strings.Contains(inner, "://") {
		inner = "http://" + inner
	}
	if !strings.HasSuffix(inner, ":9082") {
		inner = strings.TrimSuffix(inner, "/") + ":9082"
	}
	listen := os.Getenv("ACTIVATOR_LISTEN")
	if listen == "" {
		listen = ":8090"
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	faces := newControlPlaneFaces(inner)
	h := activator.New(activator.Config{}, faces, faces, faces)

	srv := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("activator listening", "addr", listen, "control", inner)
		if serr := srv.ListenAndServe(); serr != nil && serr != http.ErrServerClosed {
			logger.Error("serve", "err", serr)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// splitHostPort 是净 split（Host 头可能携端口；无端口原样返回）。
func splitHostPort(host string) (string, string, error) {
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i], host[i+1:], nil
		}
	}
	return host, "", nil
}

// controlPlaneFaces 是三窄接口的 9082 内网面实现（wake face 三端点的
// HTTP 客户端——免认证内网面与 proxy config 同 posture）。
type controlPlaneFaces struct {
	base string
	http *http.Client
}

func newControlPlaneFaces(base string) *controlPlaneFaces {
	return &controlPlaneFaces{base: base, http: &http.Client{Timeout: 10 * time.Second}}
}

func (f *controlPlaneFaces) get(path string, out interface{}) error {
	resp, err := f.http.Get(f.base + path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("wake face %s: %s (%s)", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (f *controlPlaneFaces) post(path string, out interface{}) error {
	resp, err := f.http.Post(f.base+path, "application/json", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("wake face %s: %s (%s)", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// FunctionIDForHost 实现 HostResolver：public_host → 活跃函数行 id。
func (f *controlPlaneFaces) FunctionIDForHost(ctx context.Context, host string) (string, error) {
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	var out struct {
		FunctionID string `json:"function_id"`
	}
	if err := f.get("/activator/host/"+host, &out); err != nil {
		return "", err
	}
	return out.FunctionID, nil
}

// Wake 实现 Waker：控制面 WakeFunction 条件 CAS（幂等——已醒 no-op）。
func (f *controlPlaneFaces) Wake(ctx context.Context, functionID string) error {
	var out struct {
		Woken bool `json:"woken"`
	}
	return f.post("/activator/wake/"+functionID, &out)
}

// Resolve 实现 BackendResolver：ready=true = 函数载体端点；ready=false =
// parked（扣留唤醒面）。
func (f *controlPlaneFaces) Resolve(ctx context.Context, functionID string) (string, bool, error) {
	var out struct {
		Ready bool   `json:"ready"`
		Addr  string `json:"addr"`
	}
	if err := f.get("/activator/backend/"+functionID, &out); err != nil {
		return "", false, err
	}
	return out.Addr, out.Ready, nil
}
