package gitserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DeployFromCommit 是两条 git 触发入口的汇合点：compose 字节由服务端从
// bare 仓库 `git show <sha>:compose.{yaml,yml}` 自取（真源在 git 对象库，
// 不信任客户端传字节），落临时文件走受控子集校验（与 API Deploy 同路径）
// 后入队部署，deployments.source_git_* 记录来源。
//
// 幂等口径（绑定）：git push 是显式用户动作——每次调用都建部署记录（引擎
// 对同 spec 重放安全，Spike B2）；(app, sha) 去重仅属 webhook 入口（在其
// 上游承载）。事件流复用既有 deployment.queued；审计 action 由入参区分
// （git.push_deploy / git.webhook_deploy），actor 恒 system（机器动作）。
type DeployInput struct {
	// App 是仓库对应的 app 名（compose name 与它不一致 → 拒绝——仓库
	// 路径即应用身份，跨名部署会被静默错置）。
	App string
	// SHA 是 40 位 commit。
	SHA string
	// Ref 是来源引用（refs/heads/<branch>；仅记录）。
	Ref string
	// AuditAction 是部署入队审计 action 词根。
	AuditAction string
	// ActorTokenID 是入队调用方 token（钩子 token id；webhook 进程内路径
	// 为空）。
	ActorTokenID string
}

// DeployFromCommit 执行读源 → 校验 → 入队；返回 queued 部署记录与校验
// 期警告（非阻断标注，与 API Deploy 同面）。compose 违约（受控子集之外）
// 经 compose.Load 原样透传 E_COMPOSE_* 信封。
func (s *GitTriggers) DeployFromCommit(ctx context.Context, in DeployInput) (state.DeployRecord, []compose.Warning, error) {
	if !ValidAppName(in.App) {
		return state.DeployRecord{}, nil, fmt.Errorf("gitserver: invalid app name %q", in.App)
	}
	if !ValidSHA(in.SHA) {
		return state.DeployRecord{}, nil, fmt.Errorf("gitserver: invalid sha %q", in.SHA)
	}
	if _, _, err := s.EnsureBareRepo(ctx, in.App); err != nil {
		return state.DeployRecord{}, nil, err
	}
	composeBytes, err := s.composeFromCommit(ctx, in.App, in.SHA)
	if err != nil {
		return state.DeployRecord{}, nil, composeReject(err)
	}

	// compose 落临时文件（引擎 preparing 重载复核同路径；临时文件生命周期
	// = 引擎消费完成，v0.1 不做清理回收——与 API Deploy 同口径遗留记录）。
	dir, err := os.MkdirTemp("", "fleetly-compose-")
	if err != nil {
		return state.DeployRecord{}, nil, fmt.Errorf("gitserver: create compose temp dir: %w", err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, composeBytes, 0o600); err != nil { //nolint:gosec // G306：compose 内容非密钥，0600 保守
		return state.DeployRecord{}, nil, fmt.Errorf("gitserver: write compose temp file: %w", err)
	}
	spec, warnings, err := compose.Load(ctx, path)
	if err != nil {
		return state.DeployRecord{}, nil, err // apperr（E_COMPOSE_*）原样透传
	}
	if spec.Name != in.App {
		return state.DeployRecord{}, nil, apperr.New("E_COMPOSE_UNSUPPORTED",
			"compose name %q 与仓库 app 名 %q 不一致：仓库路径即应用身份，请对齐 compose 的 name 字段",
			spec.Name, in.App)
	}

	// 应用行（首次 push = 应用的第一个平台动作，随部署自动创建）。
	app, err := ensureAppRow(ctx, s.st, spec.Name)
	if err != nil {
		return state.DeployRecord{}, nil, err
	}
	rec, err := s.st.CreateDeployment(ctx, state.DeployRecord{
		AppID:        app.ID,
		AppName:      spec.Name,
		Kind:         "deploy",
		SpecHash:     spec.SpecHash,
		ComposePath:  path,
		SourceGitSHA: in.SHA,
		SourceGitRef: in.Ref,
	})
	if err != nil {
		return state.DeployRecord{}, nil, err
	}
	via := "git_push"
	if in.AuditAction == "git.webhook_deploy" {
		via = "webhook"
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if _, err := tx.AppendEvent(ctx, state.Event{
			Name:    "deployment.queued",
			Subject: "deployment:" + rec.ID,
			Payload: `{"deployment":"` + rec.ID + `","app":"` + spec.Name + `","source":"` + via + `"}`,
		}); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:        "system",
			ActorTokenID: in.ActorTokenID,
			Action:       in.AuditAction,
			Target:       "deployment:" + rec.ID,
			Result:       "ok",
			DiffSummary:  `{"app":"` + spec.Name + `","sha":"` + in.SHA + `","ref":"` + in.Ref + `","via":"` + via + `"}`,
		})
	})
	if err != nil {
		return state.DeployRecord{}, nil, err
	}
	s.log.Info("gitserver: deployment enqueued from git",
		"app", in.App, "sha", in.SHA, "ref", in.Ref, "deployment", rec.ID, "via", via)
	return rec, warnings, nil
}

// DeployFromGitPush 是 DeployFromGit RPC 的端口实现（git push 路径；每次
// push 都建部署记录）。actorTokenID 记录钩子回调 token（可追溯）。
func (s *GitTriggers) DeployFromGitPush(ctx context.Context, app, sha, ref, actorTokenID string) (state.DeployRecord, []compose.Warning, error) {
	return s.DeployFromCommit(ctx, DeployInput{
		App:          app,
		SHA:          sha,
		Ref:          ref,
		AuditAction:  "git.push_deploy",
		ActorTokenID: actorTokenID,
	})
}

// composeReject 把 composeFromCommit 的哨兵错误映射为 E_COMPOSE_UNSUPPORTED
// 信封（errcode 零新增——compose 族复用；message 携带 git 侧原文）。
func composeReject(err error) error {
	if errors.Is(err, errComposeRejected) {
		return apperr.New("E_COMPOSE_UNSUPPORTED", "%s", err.Error())
	}
	return err
}

// ensureAppRow 取应用行；不存在则创建（与 api 包 ensureApp 同语义——本包
// 自持一份，避免反向依赖 api）。
func ensureAppRow(ctx context.Context, st *state.Store, name string) (state.App, error) {
	app, err := st.GetAppByName(ctx, name)
	if err == nil {
		return app, nil
	}
	if !errors.Is(err, state.ErrAppNotFound) {
		return state.App{}, err
	}
	created, err := st.CreateApp(ctx, "", name)
	if err != nil {
		return state.App{}, err
	}
	return created, nil
}
