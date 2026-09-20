package main

// git push(SSH) 入口服务的 lynx.Service 装配壳（T2.19，参考 ingress/
// 节点身份的服务壳模式）：Init 阶段做配置完整性 fail-fast（Validate），
// Start 阶段装载/生成 host key 并监听（阻塞至 ctx 取消），Stop 由 ctx
// 取消驱动监听收口。webhook 的 HTTP 入口不走本服务——它是 gateway 原生
// handler（见 gateway.go 的原生端点例外清单），随 HTTP 面启停；但其
// 受理后的后台 worker（D1，S17 类 D）挂本服务生命周期（见 Start/Stop）。
//
// enabled=false 时为 no-op 服务壳（Name 不变；Start/Stop 立即返回——
// 显式关闭位，不静默改端口）。

import (
	"context"
	"log/slog"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/gitserver"
)

// gitService 是 SSH git 面服务壳；D1（S17 类 D）起同时承载 webhook 后台
// worker 的生命周期（webhook 面是 gateway 原生端点、独立于 SSH enabled
// 开关，服务壳恒随 Start 启动 worker、Stop 排空退出）。
type gitService struct {
	src     *gitserver.GitTriggers
	enabled bool
	addr    string
	log     *slog.Logger
	started chan struct{}
}

func newGitService(src *gitserver.GitTriggers, app lynx.App, enabled bool, addr string) lynx.Service {
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
	// D1：webhook 后台 worker 随本服务生命周期启动（幂等；ctx 取消进入
	// 排空，StopWebhookWorker 是等待点）。
	s.src.StartWebhookWorker(ctx)
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
// 调 Stop）。D1：同时等待 webhook worker 排空退出（在处理项的 per-item
// 预算独立于取消，排空不打断；等待本身受 Stop ctx 时限约束，超时让位给
// lynx 停机时限，进程退出兜底）。X-6/MG-4：排空预算尽（webhookDrainBudget，
// 3s < lynx StopTimeout 5s）时队列剩余项在 worker 侧落披露审计
// （app.webhook_interrupted）后丢弃——停机丢失可对账、可手动 redeliver。
func (s *gitService) Stop(ctx context.Context) error {
	<-s.started
	return s.src.StopWebhookWorker(ctx)
}

// CheckHealth 委托核心（启用态配置完整性 + state 可达；禁用态恒健康）。
func (s *gitService) CheckHealth() error {
	if !s.enabled {
		return nil
	}
	return s.src.CheckHealth()
}
