package main

// ingress 服务的 lynx.Service 装配壳（T2.15/T2.16）：配置端点（独立内部
// 端口——取舍见 internal/ingress/provider.go）+ Traefik 收敛/续期扫描
// 周期任务（Run，后台降级语义——swarm 未就绪/镜像拉取失败只日志告警，
// 下轮 sweep 重试；入口故障不拖垮控制面 readiness）。
//
// Start 顺序：先起配置端点监听（Traefik 首次拉取的前置），再后台跑
// Run（收敛 + 续期）。Stop 关停 HTTP server。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/ingress"
)

// ingressService 是入口服务壳。
type ingressService struct {
	mgr  *ingress.Manager
	log  *slog.Logger
	srv  *http.Server
	addr string
}

func newIngressService(m *ingress.Manager, app lynx.App, addr string) lynx.Service {
	return &ingressService{mgr: m, log: app.Logger(), addr: addr}
}

func (s *ingressService) Name() string { return "ingress.traefik" }

// Init 无动作（token/handler 构造延迟到 Start 的监听路径内——装配期不碰
// 文件系统生成的 token）。
func (s *ingressService) Init(_ lynx.AppContext) error { return nil }

func (s *ingressService) Start(ctx context.Context) error {
	handler, err := s.mgr.Handler(ctx)
	if err != nil {
		return fmt.Errorf("ingress: construct provider handler: %w", err)
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("ingress: listen config endpoint %s: %w", s.addr, err)
	}
	// 回填端口（ConfigAddr 端口位为 0 的动态端口形态；挑战应答 URL 依赖）。
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.mgr.SetConfigPort(tcp.Port)
	}
	s.srv = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() {
		if serr := s.srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			serveErr <- serr
		}
		close(serveErr)
	}()
	s.log.Info("ingress: config endpoint listening", "addr", ln.Addr().String())

	// 后台收敛与续期（Run 随 ctx 取消返回；sweep 内部降级——不阻塞
	// 服务 Start，Traefik 未就绪不影响控制面 readiness）。
	go func() { _ = s.mgr.Run(ctx) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("ingress: config endpoint serve: %w", err)
		}
		<-ctx.Done()
		return nil
	}
}

// Stop 关停配置端点（预算 5s；Traefik 侧「配置服务不可达保留旧配置」
// 语义保证入口不坏，Spike B d 态实测）。
func (s *ingressService) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.srv.Shutdown(shutdownCtx)
}

// CheckHealth 恒健康（配置端点启动失败 = Start 阶段显式失败，readiness
// 不需要二次上报；Traefik 收敛态由 sweep 日志与 fleetly ingress status
// 呈现，不进 readiness——入口是降级设计）。
func (s *ingressService) CheckHealth() error { return nil }
