package gitserver

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// D1（S17 类 D）：webhook 受理与执行分离——GitTriggers 额外承载 webhook
// 异步执行面（带界队列 + 单 worker，webhook_worker.go），生命周期挂
// git.ssh 服务壳（StartWebhookWorker/StopWebhookWorker）。

// GitTriggers 是 git 触发入口的门面（命名口径 UBIQUITOUS_LANGUAGE §flagged-2：
// 承载触发面+拉源+部署入队，不只是一个「来源」，故弃 Source 旧名）：
// bare 仓库管理与 post-receive 钩子、DeployFromCommit 入队、webhook
// 验签/防重放/去重/拉源、SSH 服务器。
// 它聚合 state 层（git keys/tokens/apps/deployments）与平台密钥盒
// （webhook secret / 拉源认证材料的 envelope 加解密），对上暴露：
//
//   - SSH：ListenAndServe（lynx.Service 壳在 cmd/fleetlyd 驱动）
//   - HTTP：WebhookHandler（gateway 原生端点例外清单挂载）
//   - API：DeployFromGitPush（DeployFromGit RPC 的端口实现——端口接口
//     定义在 internal/api，方向纪律：api 不感知本包类型）
type GitTriggers struct {
	cfg    Config
	st     *state.Store
	box    *secrets.Box
	log    *slog.Logger
	replay *deliveryCache
	// fetchFn 是拉源执行步骤的注入缝（NewGitTriggers 恒设为 runFetch；测试替换后可
	// 不真实触网驱动 TOFU 审计链——与 statebackup.Manager.verifyFn 同款
	// 接缝形态）。
	fetchFn func(ctx context.Context, plan fetchPlan) error
	// hooks 是 webhook 异步执行面（D1，S17 类 D）：受理队列 + 单 worker，
	// 见 webhook_worker.go。
	hooks webhookRunner
	// repoMu 保护 repoLocks；repoLocks 是 per-app 的仓库/钩子写入互斥
	// （E7③，S19：EnsureBareRepo 的 stat→init→writeHook 与 writeHook 内
	// rotateHookToken 的「list→revoke→create」存在 TOCTOU——并发同 app
	// 下交错可致钩子内 token 与在册 token 失配（push 回调永久 401）或多
	// 条活 token 残留）。
	repoMu    sync.Mutex
	repoLocks map[string]*sync.Mutex
}

// New 构造 Source（cfg 先经 Normalize；启用态完整性 Validate 由装配点
// 在 Init 阶段 fail-fast）。
func NewGitTriggers(cfg Config, st *state.Store, box *secrets.Box, log *slog.Logger) *GitTriggers {
	norm := cfg.Normalize()
	if log == nil {
		log = slog.New(discardHandler{})
	}
	src := &GitTriggers{
		cfg:       norm,
		st:        st,
		box:       box,
		log:       log,
		replay:    newDeliveryCache(norm.ReplayTTL, time.Now),
		fetchFn:   runFetch,
		repoLocks: map[string]*sync.Mutex{}, // E7③：per-app 分段锁
	}
	src.hooks.init() // D1：webhook 受理队列（worker 由服务壳 Start 启动）
	return src
}

// Config 返回归一后的配置（只读投影）。
func (s *GitTriggers) Config() Config { return s.cfg }

// CheckHealth 报告 git 入口层健康：启用态配置完整性 + state 可达。SSH
// 监听失败属 Start 阶段显式失败，不在健康面二次上报（与 ingress 服务壳
// 同口径）。
func (s *GitTriggers) CheckHealth() error {
	if err := s.cfg.Validate(); err != nil {
		return err
	}
	return s.st.CheckHealth()
}

// hookTokenName 是仓库钩子 token 在 tokens 表的识别名（轮换 = 按名吊销
// 重建；明文只写进钩子文件并 0600，与 daemon 重建仓库时轮换语义配套）。
func hookTokenName(app string) string { return "git hook " + app }

// webhookEndpoint 拼钩子回调 URL（loopback REST → DeployFromGit RPC）。
func (s *GitTriggers) webhookEndpoint(app string) string {
	return s.cfg.HookEndpoint + "/v1/apps/" + app + "/deployments/git"
}

// discardHandler 兜底空日志（理论不可达：装配点恒注入）。
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
