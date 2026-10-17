// Package transact 是事务作用域面（ADR-0080 显式 bundle UoW）：Stores 是
// 本事务可见的目录（全部聚合 repo 的 tx 绑定形态 + tx 作用域 Anchor +
// seq 铸造），Binder 由组合根注入（唯一知道聚合 repo 构造的地方），Tx.Do
// 是唯一事务入口——开事务、绑定、提交/回滚集中一处。
//
// 纪律：①fn 收到的 Stores 只在 fn 内存活，不得逃出闭包（出界使用 =
// "transaction has already been committed" 响亮错）；②Do 不可嵌套
// （SQLite 嵌套 BEGIN 天然 fail-closed）；③读径不走本包——组合根构造
// 的 db 绑定 repo 直接调（CQRS-lite，ADR-0075 读写分离律）。
package transact

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/idempotency"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/catalog"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/invitation"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	configrepo "github.com/fleetlyrun/fleetly/internal/domain/structure/config"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/route"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/logcursor"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// Stores 是本事务可见的目录：全部聚合 repo 的 tx 绑定形态 + tx 作用域
// Anchor（归属解析进同一事务）+ NextSeq（ADR-0070 铸造单源随视图走——
// 事务内 MAX+1 读改写与聚合写同事务原子）。字段齐备是有意的：调用点按
// 需取用，不按需装配。
type Stores struct {
	// Anchor 是「聚合行 → Project → Team」归属解析图（tx 作用域重建）。
	Anchor *anchor.Anchor
	// NextSeq 铸表的下一创建序号（ADR-0070；table 由调用方以字面量传入）。
	NextSeq func(ctx context.Context, table string) (int64, error)

	Apps            app.Repo
	AppVariables    appvariable.Repo
	AlertRules      alertrule.Repo
	Audits          audit.Repo
	Backups         backup.Repo
	Browse          browse.Repo
	Builds          build.Repo
	Catalog         catalog.Repo
	Channels        channel.Repo
	Configs         configrepo.Repo
	Databases       database.Repo
	Deployments     deployment.Repo
	Freezes         freeze.Repo
	GitHubApps      githubapp.Repo
	Hooks           hook.Repo
	Idempotency     idempotency.Repo
	Invitations     invitation.Repo
	LogCursors      logcursor.Repo
	Memberships     membership.Repo
	Networks        network.Repo
	NetworkPeers    networkpeer.Repo
	Nodes           node.Repo
	Outbox          outbox.Repo
	Projects        project.Repo
	Revisions       revision.Repo
	Roles           role.Repo
	Routes          route.Repo
	Runs            run.Repo
	Schedules       schedule.Repo
	Secrets         secret.Repo
	SharedVariables sharedvariable.Repo
	SourceUploads   sourceupload.Repo
	Tasks           task.Repo
	Teams           team.Repo
	Tokens          token.Repo
	Users           user.Repo
	Volumes         volume.Repo
}

// Binder 以事务执行面造出 Stores（组合根注入：唯一知道聚合 repo 构造
// 形态的地方；run 是 *sql.Tx——满足 state.Runner）。
type Binder func(run state.Runner) Stores

// Tx 是事务入口句柄（组合根装配一次；db + bind 双注入）。
type Tx struct {
	db   state.DB
	bind Binder
}

// NewTx 装配事务入口。
func NewTx(db state.DB, bind Binder) Tx { return Tx{db: db, bind: bind} }

// Do 在单个写事务内执行 fn：开事务 → 以 tx 执行面造 Stores → 执行 fn →
// 提交（fn 返回错误即回滚）。四件一拍（ADR-0005）的机制承载：fn 内经
// s 触碰的一切落库同事务原子。
func (t Tx) Do(ctx context.Context, fn func(s Stores) error) error {
	return t.db.Tx(ctx, func(run state.Runner) error { return fn(t.bind(run)) })
}
