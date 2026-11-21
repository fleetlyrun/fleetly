// activator 是唤醒组件守护进程（P2-1a 第四刀，设计 v2 §1.2/§1.6）：
// 缩零期函数的请求扣留面。部署形态 = 平台受管 Workload（挂全部活跃项目
// 网——traefik 同款挂靠面），三窄接口经 SDK 对接控制面（activator 永不
// 触 Runtime、永不自拼载体名——转发目标经 ResolveFunctionBackend 平台
// 解析）。
//
// 凭据面：FLEETLY_ADDR/FLEETLY_TOKEN（CLI 同族环境变量——受管 Workload
// 投影经平台事实 env 注入），ACTIVATOR_LISTEN 缺省 :8090。
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/activator"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func main() {
	addr := os.Getenv("FLEETLY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:9080"
	}
	token := os.Getenv("FLEETLY_TOKEN")
	listen := os.Getenv("ACTIVATOR_LISTEN")
	if listen == "" {
		listen = ":8090"
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	ctx := context.Background()
	if token != "" {
		ctx = fleetly.WithToken(ctx, token)
	}
	client, err := fleetly.Dial(addr)
	if err != nil {
		logger.Error("dial control plane", "addr", addr, "err", err)
		os.Exit(1)
	}
	faces := &controlPlaneFaces{ctx: ctx, client: client}
	h := activator.New(activator.Config{}, faces, faces, faces)

	srv := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("activator listening", "addr", listen)
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

// splitHostPort 是净 split（net.SplitHostPort 对无端口 host 报错——容
// 错形态：无端口原样返回）。
func splitHostPort(host string) (string, string, error) {
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i], host[i+1:], nil
		}
		if host[i] == ']' {
			return host, "", nil
		}
	}
	return host, "", nil
}

// controlPlaneFaces 是三窄接口的 SDK 实现（HostResolver/Waker/
// BackendResolver 单实现三面——控制面数据单源；函数目录按名索引大页拉取
// ——host 映射与 parked 判定都走活跃行集，无 per-host 点查）。
type controlPlaneFaces struct {
	ctx    context.Context
	client *fleetly.Client
}

// FunctionIDForHost 实现 HostResolver：public_host → 活跃函数行 id。
// 函数目录经 ListFunctions 大页拉取（对照活跃行集——与收敛环同源；未命中
// = ""（404 诚实），跨项目目录全量遍历（activator 服务全平台）。
func (f *controlPlaneFaces) FunctionIDForHost(ctx context.Context, host string) (string, error) {
	if host == "" {
		return "", nil
	}
	// host 形态规范化（port 剥离——Host 头可能带端口）。
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	resp, err := f.client.Functions.ListFunctions(ctx, &deliveryv1.ListFunctionsRequest{})
	if err != nil {
		return "", err
	}
	for _, fn := range resp.GetFunctions() {
		if fn.GetPublicHost() == host {
			return fn.GetId(), nil
		}
	}
	return "", nil
}

// Wake 实现 Waker：控制面 WakeFunction 条件 CAS（幂等——已醒 no-op）。
func (f *controlPlaneFaces) Wake(ctx context.Context, functionID string) error {
	_, err := f.client.Functions.WakeFunction(f.ctx, &deliveryv1.WakeFunctionRequest{FunctionId: functionID})
	return err
}

// Resolve 实现 BackendResolver：ready=true = 函数载体端点（透传目标）；
// ready=false = parked（扣留唤醒面）。
func (f *controlPlaneFaces) Resolve(ctx context.Context, functionID string) (string, bool, error) {
	resp, err := f.client.Functions.ResolveFunctionBackend(f.ctx, &deliveryv1.ResolveFunctionBackendRequest{FunctionId: functionID})
	if err != nil {
		return "", false, err
	}
	return resp.GetAddr(), resp.GetReady(), nil
}
