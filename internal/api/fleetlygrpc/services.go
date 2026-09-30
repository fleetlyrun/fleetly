// Package fleetlygrpc 实现五上下文 API 面（structure/delivery/runtime/
// edge/telemetry）。读路径直读聚合 repo；写路径四件一拍（engine 或 repo
// 事务内组合 outbox/audit）；错误一律 apperr 信封（映射 helper 在
// mapping.go）。scope 注解已在 proto 写好；enforcement 随账号批接管。
package fleetlygrpc

import (
	"log/slog"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// Services 聚合全部 API 依赖（装配注入；Runtime 面收窄为 Enrollment 消费）。
type Services struct {
	DB      *state.DB
	Engine  *engine.Engine
	Cipher  *material.Cipher
	Runtime capability.Runtime

	Projects     *project.Repo
	Apps         *app.Repo
	Deployments  *deployment.Repo
	Revisions    *revision.Repo
	Builds       *build.Repo
	OutboxEvents *outbox.Repo
	Secrets      *secret.Repo
	Configs      *configrepo.Repo
	Volumes      *volume.Repo
	Networks     *networkrepo.Repo
	Routes       *route.Repo
	Nodes        *node.Repo
	Audits       *audit.Repo

	Log *slog.Logger
}

// NewServices 构造（repos 从 DB 时钟派生）。
func NewServices(db *state.DB, e *engine.Engine, c *material.Cipher, rt capability.Runtime, log *slog.Logger) *Services {
	clock := db.Clock()
	return &Services{
		DB:           db,
		Engine:       e,
		Cipher:       c,
		Runtime:      rt,
		Projects:     project.New(clock),
		Apps:         app.New(clock),
		Deployments:  deployment.New(clock),
		Revisions:    revision.New(clock),
		Builds:       build.New(clock),
		OutboxEvents: outbox.New(clock),
		Secrets:      secret.New(clock),
		Configs:      configrepo.New(clock),
		Volumes:      volume.New(clock),
		Networks:     networkrepo.New(clock),
		Routes:       route.New(clock),
		Nodes:        node.New(clock),
		Audits:       audit.New(clock),
		Log:          log,
	}
}
