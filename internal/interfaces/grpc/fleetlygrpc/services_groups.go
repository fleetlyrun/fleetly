package fleetlygrpc

// services_groups.go 按限界上下文分组 Services/Deps 的字段：Services 仍是
// 组合根（持有全部依赖），但字段面以上下文命名，避免 60+ 平铺字段成为
// 不可导航的大 bag；handler 包仍只消费各自 Deps 的窄接口。

import (
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketresource"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/data/topic"
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
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
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
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// Core 是跨上下文共享的装配核心：库/事务/密钥/运行时/观测/归属解析与
// 写原语、确认票据店。字段由组合根注入，handler 不直接消费本组。
type Core struct {
	DB state.DB
	// Tx 是事务入口（组合根装配 db + Binder，ADR-0080）。
	Tx transact.Tx
	// Cipher 是材料加解封面（nil = 主密钥未配置的显式关态）。
	Cipher *material.Cipher
	// Runtime 是编排器运行时面（受管组件/节点/升级等）。
	Runtime capability.Runtime
	// Logging/Metrics 是受管观测后端（可空 = 面停用）。
	Logging capability.Logging
	Metrics capability.Metrics
	// Anchor 是「聚合行 → Project → Team」归属解析图（freeze 与行级授权
	// 共用单源；组装在组合根，anchor 零持久化依赖）。
	Anchor *anchor.Anchor
	Log    *slog.Logger
	// writes 是受理位写原语（acceptance.go commit 的唯一消费面）。
	writes writeAPI
	// eventTickets 是 SSE/确认/浏览的一次性短时票据面（进程内）。
	eventTickets *eventTicketStore
}

// ModuleAPIs 是 engine 按业务模块收窄的消费面（DIP/ISP；api.go）。
type ModuleAPIs struct {
	Deploy   DeployAPI
	Tasks    TaskAPI
	Exec     ExecAPI
	Browse   BrowseAPI
	Backup   BackupAPI
	Registry RegistryAPI
	Data     DataAPI
	Nodes    NodeAPI
	System   SystemAPI
}

// StructureDeps 是结构上下文（Project/App/Secret/Config/变量/卷/网络）
// 的 repo 面。
type StructureDeps struct {
	Projects        project.Repo
	Apps            app.Repo
	Secrets         secret.Repo
	Configs         configrepo.Repo
	SharedVariables sharedvariable.Repo
	AppVariables    appvariable.Repo
	Volumes         volume.Repo
	Networks        network.Repo
	NetworkPeers    networkpeer.Repo
}

// DataDeps 是托管数据服务上下文（database/pubsub/bucketstore 及子资源、
// 浏览会话、备份台账）的 repo 面。
type DataDeps struct {
	Databases    database.Repo
	PubSubs      pubsub.Repo
	BucketStores bucketstore.Repo
	Topics       topic.Repo
	Streams      stream.Repo
	Buckets      bucketresource.Repo
	BrowseRepo   browse.Repo
	Backups      backup.Repo
}

// DeliveryDeps 是交付上下文（部署/修订/构建/上传/hook/GitHub App）的
// repo 面。
type DeliveryDeps struct {
	Deployments deployment.Repo
	Revisions   revision.Repo
	Builds      build.Repo
	Uploads     sourceupload.Repo
	Hooks       hook.Repo
	GitHubApps  githubapp.Repo
}

// TemplatesDeps 是 App 模板目录上下文的 repo 面。
type TemplatesDeps struct {
	Catalog catalog.Repo
}

// IdentityDeps 是身份与访问上下文的 repo 面。
type IdentityDeps struct {
	Users       user.Repo
	Teams       team.Repo
	Roles       role.Repo
	Memberships membership.Repo
	Tokens      token.Repo
	Invitations invitation.Repo
	Audits      audit.Repo
}

// AutomationDeps 是自动化上下文（task/run/schedule）的 repo 面。
type AutomationDeps struct {
	TaskRepo  task.Repo
	Runs      run.Repo
	Schedules schedule.Repo
}

// PlatformDeps 是平台/运行/治理/遥测上下文的 repo 面（事件流、冻结、
// 路由、节点）。
type PlatformDeps struct {
	OutboxEvents outbox.Repo
	Freezes      freeze.Repo
	Routes       route.Repo
	NodeRepo     node.Repo
}

// Clients 是外部客户端注入面（GitHub App 客户端、上传 blob 存储）。
type Clients struct {
	GitHub      githubapp.Client
	UploadStore sourceupload.Store
}

// Config 是运行配置面（组合根在构造后注入的装配参数）。
type Config struct {
	// DataRoot 是数据根（控制面日志镜像等本机文件面的定位锚，ADR-0091）。
	DataRoot string
	// ScopeVocabulary 是 scope 词表（CreateRole 校验用；assembly 单一源）。
	ScopeVocabulary []string
	// GatewayPort 是 REST gateway 端口（H10：EnrollNode 定向中继材料）。
	GatewayPort string
	// LoginTokenTTL 是 Login 会话 Token 时效（0 = 永不过期）。
	LoginTokenTTL time.Duration
	// TemplatesCatalogURL 是目录刷新源（空 = 刷新停用）。
	TemplatesCatalogURL string
	// GitHubManifestBase/APIBase 是 manifest 自动建流的公网/API 基址。
	GitHubManifestBase    string
	GitHubManifestAPIBase string
}
