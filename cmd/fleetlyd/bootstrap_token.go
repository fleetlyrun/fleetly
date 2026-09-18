package main

// API token 种子（T2.17 安装引导语义）：首次启动无任何 token（含已吊销
// ——「存在过」即不算首次）时生成 bootstrap admin token，**打印一次**并
// 入审计（token.create，ActorTokenID 空 = bootstrap 自举）。
//
// 打印通道 = fleetlyd 启动日志（Info 级、单行、仅此一次）——后续启动
// HasAnyToken 恒真，不再出现。token 明文只存日志与用户剪贴板，库内恒为
// sha256 哈希。

import (
	"context"
	"log/slog"

	"github.com/lynx-go/lynx"

	"github.com/fleetlyrun/fleetly/internal/api"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// NewAuthenticator 构造 API 认证器并承载 bootstrap 种子（装配期执行——
// 失败 fail-fast 拒绝启动：鉴权面就绪是 API 面可用的前置）。
func NewAuthenticator(app lynx.App, st *state.Store) (*api.Authenticator, error) {
	if err := bootstrapAdminToken(app.Logger(), st); err != nil {
		return nil, err
	}
	return api.NewAuthenticator(st), nil
}

// bootstrapAdminToken 首启种子（logger 注入，测试可断言「只显示一次」）。
func bootstrapAdminToken(log *slog.Logger, st *state.Store) error {
	any, err := st.HasAnyToken(context.Background())
	if err != nil {
		return err
	}
	if any {
		return nil // 已有 token（含历史吊销）：不重复引导
	}
	plaintext, err := api.GenerateBootstrapAdminToken(context.Background(), st, "bootstrap admin (initial install)")
	if err != nil {
		return err
	}
	log.Info("bootstrap admin token generated（仅此一次显示，请妥善保存）: " + plaintext)
	return nil
}
