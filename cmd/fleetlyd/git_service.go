package main

// git push(SSH) 入口服务的 lynx.Service 装配壳（T2.19，参考 ingress/
// 节点身份的服务壳模式）：Init 阶段做配置完整性 fail-fast（Validate），
// Start 阶段装载/生成 host key 并监听（阻塞至 ctx 取消），Stop 由 ctx
// 取消驱动监听收口。webhook 入口不走本服务——它是 gateway 原生 HTTP
// handler（见 gateway.go 的原生端点例外清单），随 HTTP 面启停。
//
// enabled=false 时为 no-op 服务壳（Name 不变；Start/Stop 立即返回——
// 显式关闭位，不静默改端口）。

import (
	"context"
	"log/slog"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/gitserver"
)

// gitService 是 SSH git 面服务壳。
type gitService struct {
	src     *gitserver.Source
	enabled bool
	addr    string
	log     *slog.Logger
	started chan struct{}
}

func newGitService(src *gitserver.Source, app lynx.App, enabled bool, addr string) lynx.Service {
	return &gitService{
		src:     src,
		enabled: enabled,
		addr:    addr,
		log:     app.Logger(),
		started: make(chan struct{}),
	}
}

func (s *gitService) Name() string { return "git.ssh" }

// Init 配置完整性 fail-fast（启用态 Root/HookEndpoint 非空）。
func (s *gitService) Init(_ lynx.AppContext) error {
	return s.src.Config().Validate()
}

// Start 监听 SSH git 面（阻塞语义与 ingress 服务壳一致）。
func (s *gitService) Start(ctx context.Context) error {
	if !s.enabled {
		s.log.Info("gitserver: ssh git endpoint disabled (git.enabled=false)")
		close(s.started)
		<-ctx.Done()
		return nil
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.src.ListenAndServe(ctx, s.addr) }()
	close(s.started)
	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErr:
		if err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
}

// Stop 等待 Start 收口（监听关闭由 ctx 取消驱动——lynx 先 cancel 再
// 调 Stop）。
func (s *gitService) Stop(_ context.Context) error {
	<-s.started
	return nil
}

// CheckHealth 委托核心（启用态配置完整性 + state 可达；禁用态恒健康）。
func (s *gitService) CheckHealth() error {
	if !s.enabled {
		return nil
	}
	return s.src.CheckHealth()
}
