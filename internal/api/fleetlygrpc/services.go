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
	"github.com/fleetlyrun/fleetly/internal/state/freeze"
	"github.com/fleetlyrun/fleetly/internal/state/hook"
	"github.com/fleetlyrun/fleetly/internal/state/invitation"
	"github.com/fleetlyrun/fleetly/internal/state/membership"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/node"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
	"github.com/fleetlyrun/fleetly/internal/state/role"
	"github.com/fleetlyrun/fleetly/internal/state/route"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/state/task"
	"github.com/fleetlyrun/fleetly/internal/state/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	"github.com/fleetlyrun/fleetly/internal/state/user"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
	"github.com/fleetlyrun/fleetly/internal/upload"
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
	Tasks        *task.Repo
	Runs         *run.Repo
	Schedules    *schedule.Repo
	OutboxEvents *outbox.Repo
	Secrets      *secret.Repo
	Configs      *configrepo.Repo
	Volumes      *volume.Repo
	Networks     *networkrepo.Repo
	NetworkPeers *networkpeer.Repo
	Freezes      *freeze.Repo
	Routes       *route.Repo
	Nodes        *node.Repo
	Audits       *audit.Repo
	Users        *user.Repo
	Teams        *team.Repo
	Roles        *role.Repo
	Memberships  *membership.Repo
	Tokens       *tokenrepo.Repo
	Invitations  *invitation.Repo
	Hooks        *hook.Repo

	// Uploads 是上传产物行 repo；UploadStore 是 blob 面（内容寻址落盘，
	// ADR-0019 附录 A）。限额走缺省（512MiB/4GiB，ADR 钉值）。
	Uploads     *sourceupload.Repo
	UploadStore *upload.Store

	// eventTickets 是 SSE 订阅路径的一次性短时票据面（ADR-0026；铸造经
	// IssueEventTicket，兑换限 SSE 原生入口）。
	eventTickets *eventTicketStore

	// ScopeVocabulary 是 scope 词表（CreateRole 校验用；assembly 单一源
	// 注入——服务面不自带词表）。
	ScopeVocabulary []string

	Log *slog.Logger
}

// NewServices 构造（repos 从 DB 时钟派生；vocab 是 scope 词表单一源；
// dataRoot 是上传产物 blob 根）。
func NewServices(db *state.DB, e *engine.Engine, c *material.Cipher, rt capability.Runtime, dataRoot string, vocab []string, log *slog.Logger) *Services {
	clock := db.Clock()
	return &Services{
		DB:              db,
		Engine:          e,
		Cipher:          c,
		Runtime:         rt,
		Projects:        project.New(clock),
		Apps:            app.New(clock),
		Deployments:     deployment.New(clock),
		Revisions:       revision.New(clock),
		Builds:          build.New(clock),
		Tasks:           task.New(clock),
		Runs:            run.New(clock),
		Schedules:       schedule.New(clock),
		OutboxEvents:    outbox.New(clock),
		Secrets:         secret.New(clock),
		Configs:         configrepo.New(clock),
		Volumes:         volume.New(clock),
		Networks:        networkrepo.New(clock),
		NetworkPeers:    networkpeer.New(clock),
		Freezes:         freeze.New(clock),
		Routes:          route.New(clock),
		Nodes:           node.New(clock),
		Audits:          audit.New(clock),
		Users:           user.New(clock),
		Teams:           team.New(clock),
		Roles:           role.New(clock),
		Memberships:     membership.New(clock),
		Tokens:          tokenrepo.New(clock),
		Invitations:     invitation.New(clock),
		Hooks:           hook.New(clock),
		Uploads:         sourceupload.New(clock),
		UploadStore:     upload.NewStore(dataRoot, 0, 0),
		eventTickets:    newEventTicketStore(clock),
		ScopeVocabulary: vocab,
		Log:             log,
	}
}
