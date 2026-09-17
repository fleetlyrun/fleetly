package main

import (
	"context"
	"io"
	"log/slog"
	gohttp "net/http"
	"testing"
	"time"

	"github.com/lynx-go/lynx"
	lynxhttp "github.com/lynx-go/lynx/server/http"
)

// TestHealthzEndpointsAndGracefulStop 是 T0.1 冒烟测试：按 provides.go 的
// NewHTTPServer 同一形态构建 lynx HTTP 服务（仅框架 healthz，随机端口），
// 验证 ① /healthz/liveness 与 /healthz/readiness 返回 200；② Stop 优雅
// 关停返回 nil 且此后端口不再接受连接（等价进程收到退出信号后的
// 服务侧排水路径；信号监听与退出码由 lynx Runner 托管）。
func TestHealthzEndpointsAndGracefulStop(t *testing.T) {
	srv := lynxhttp.NewServer(newRootMux(),
		lynxhttp.WithAddr("127.0.0.1:0"),
		// 与装配形态一致：框架健康检查器取值函数；骨架阶段无检查器，
		// readiness 未配置检查器时恒 200（lynx 语义）。
		lynxhttp.WithHealthCheckers(func() []lynx.Checker { return nil }),
		lynxhttp.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err := srv.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}

	startErr := make(chan error, 1)
	go func() { startErr <- srv.Start(context.Background()) }()

	select {
	case <-srv.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("http server not ready within 5s")
	}
	addr := "http://" + srv.Addr()
	if srv.Addr() == "" {
		t.Fatal("server did not report a listen address")
	}

	client := &gohttp.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/healthz/liveness", "/healthz/readiness"} {
		resp, err := client.Get(addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := resp.StatusCode
		_ = resp.Body.Close()
		if body != gohttp.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, body)
		}
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("graceful Stop: %v", err)
	}
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start returned after Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return within 5s after Stop")
	}

	if _, err := client.Get(addr + "/healthz/liveness"); err == nil {
		t.Fatal("request after Stop succeeded, want connection refused")
	}
}
