package gitserver

import (
	"context"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Source 是 git 触发入口的核心：bare 仓库管理与 post-receive 钩子、
// DeployFromCommit 入队、webhook 验签/防重放/去重/拉源、SSH 服务器。
// 它聚合 state 层（git keys/tokens/apps/deployments）与平台密钥盒
// （webhook secret / 拉源认证材料的 envelope 加解密），对上暴露：
//
//   - SSH：ListenAndServe（lynx.Service 壳在 cmd/fleetlyd 驱动）
//   - HTTP：WebhookHandler（gateway 原生端点例外清单挂载）
//   - API：DeployFromGitPush（DeployFromGit RPC 的端口实现——端口接口
//     定义在 internal/api，方向纪律：api 不感知本包类型）
type Source struct {
	cfg    Config
	st     *state.Store
	box    *secrets.Box
	log    *slog.Logger
	replay *deliveryCache
}

// New 构造 Source（cfg 先经 Normalize；启用态完整性 Validate 由装配点
// 在 Init 阶段 fail-fast）。
func New(cfg Config, st *state.Store, box *secrets.Box, log *slog.Logger) *Source {
	norm := cfg.Normalize()
	if log == nil {
		log = slog.New(discardHandler{})
	}
	return &Source{
		cfg:    norm,
		st:     st,
		box:    box,
		log:    log,
		replay: newDeliveryCache(norm.ReplayTTL, time.Now),
	}
}

// Config 返回归一后的配置（只读投影）。
func (s *Source) Config() Config { return s.cfg }

// CheckHealth 报告 git 入口层健康：启用态配置完整性 + state 可达。SSH
// 监听失败属 Start 阶段显式失败，不在健康面二次上报（与 ingress 服务壳
// 同口径）。
func (s *Source) CheckHealth() error {
	if err := s.cfg.Validate(); err != nil {
		return err
	}
	return s.st.CheckHealth()
}

// hookTokenName 是仓库钩子 token 在 tokens 表的识别名（轮换 = 按名吊销
// 重建；明文只写进钩子文件并 0600，与 daemon 重建仓库时轮换语义配套）。
func hookTokenName(app string) string { return "git hook " + app }

// webhookEndpoint 拼钩子回调 URL（loopback REST → DeployFromGit RPC）。
func (s *Source) webhookEndpoint(app string) string {
	return s.cfg.HookEndpoint + "/v1/apps/" + app + "/deployments/git"
}

// discardHandler 兜底空日志（理论不可达：装配点恒注入）。
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
