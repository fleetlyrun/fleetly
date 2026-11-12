package datagrpc

import (
	"context"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketresource"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/data/topic"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"google.golang.org/grpc"
	"io"
	"time"
)

// DataAPI 是数据服务面的窄消费接口（engine 门面实现）。
type DataAPI interface {
	CheckVolumeOrigin(ctx context.Context, kind, projectID, name, engineName string) error
	KickDatabases()
	TeardownDatabase(ctx context.Context, id string) error
	RotateDatabasePassword(ctx context.Context, databaseID string) error
	UpdateDatabase(ctx context.Context, databaseID string, req contract.UpdateRequest) (contract.UpdateResult, error)
	KickPubSubs()
	TeardownPubSub(ctx context.Context, id string) error
	RotatePubSubPassword(ctx context.Context, pubsubID string) error
	UpdatePubSub(ctx context.Context, pubsubID string, req contract.PubSubUpdateRequest) (contract.UpdateResult, error)
	TopicLiveInspect(ctx context.Context, pubsubID, topicID string) (pubsubtemplate.TopicLive, error)
	StreamLiveInspect(ctx context.Context, pubsubID, streamID string) (pubsubtemplate.StreamLive, error)
	KickBucketStores()
	TeardownBucketStore(ctx context.Context, id string) error
	RotateBucketStorePassword(ctx context.Context, bucketStoreID string) error
	UpdateBucketStore(ctx context.Context, bucketStoreID string, req contract.BucketStoreUpdateRequest) (contract.UpdateResult, error)
	BucketLiveInspect(ctx context.Context, bucketStoreID, bucketID string) (bucketstoretemplate.BucketLive, error)
}

// BackupAPI 是备份面的窄消费接口。
type BackupAPI interface {
	VerifyBackup(ctx context.Context, backupID string) (bool, string, error)
	DownloadBackup(ctx context.Context, databaseID, backupID string) (*backup.Backup, io.ReadCloser, error)
	KickBackups()
}

// BrowseAPI 是数据浏览器面的窄消费接口。
type BrowseAPI interface {
	BrowseConfigured() bool
	BrowseCheckQuota(teamID string) error
	RegisterBrowseSession(in api.BrowseInput) (api.BrowseSessionInfo, error)
	BrowseEntryURL(sessionID string) string
}

// Anchor 是 Project → Team 解析面的窄消费接口。
type Anchor interface {
	TeamOfProjectID(ctx context.Context, projectID string) (string, error)
}

// Authorizer 是行级授权消费面（fleetlygrpc.Services 实现）。
type Authorizer interface {
	AuthorizeProjectID(ctx context.Context, projectID string) error
}

// Tickets 是确认票与 browse 票据的窄消费接口（fleetlygrpc.Services 实现）。
type Tickets interface {
	MintConfirm(payload governance.ConfirmTicketPayload) (string, time.Duration, error)
	PeekConfirm(ticket string) (governance.ConfirmTicketPayload, governance.ConfirmPeekStatus)
	DiscardConfirm(ticket string) bool
	IssueBrowse(sessionID string, ttl time.Duration) (string, time.Duration, error)
}

// Deps 是数据服务面的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Data         DataAPI
	Backup       BackupAPI
	Browse       BrowseAPI
	Anchor       Anchor
	Databases    database.Repo
	PubSubs      pubsub.Repo
	BucketStores bucketstore.Repo
	Topics       topic.Repo
	Streams      stream.Repo
	Buckets      bucketresource.Repo
	Backups      backup.Repo
	Secrets      secret.Repo
	Projects     project.Repo
	Cipher       *material.Cipher
	Auth         Authorizer
	Tickets      Tickets
	Commit       func(ctx context.Context, fact grpcapi.WriteFact) error
	Clock        state.Clock
}

// DatabasesService 满足 structurev1.DatabasesServiceServer。
type DatabasesService struct {
	structurev1.UnimplementedDatabasesServiceServer
	deps Deps
}

// PubSubsService 满足 structurev1.PubSubsServiceServer。
type PubSubsService struct {
	structurev1.UnimplementedPubSubsServiceServer
	deps Deps
}

// BucketStoresService 满足 structurev1.BucketStoresServiceServer。
type BucketStoresService struct {
	structurev1.UnimplementedBucketStoresServiceServer
	deps Deps
}

// NewDatabases 构造数据库服务（复合编排面内层直调用）。
func NewDatabases(deps Deps) *DatabasesService { return &DatabasesService{deps: deps} }

// Register 注册三个数据服务。
func Register(srv *grpc.Server, deps Deps) {
	structurev1.RegisterDatabasesServiceServer(srv, &DatabasesService{deps: deps})
	structurev1.RegisterPubSubsServiceServer(srv, &PubSubsService{deps: deps})
	structurev1.RegisterBucketStoresServiceServer(srv, &BucketStoresService{deps: deps})
}

var (
	_ structurev1.DatabasesServiceServer    = (*DatabasesService)(nil)
	_ structurev1.PubSubsServiceServer      = (*PubSubsService)(nil)
	_ structurev1.BucketStoresServiceServer = (*BucketStoresService)(nil)
)

// parentProjectAlive 是 Project 存活受理检查。
func parentProjectAlive(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		return grpcapi.RequireActiveProject(ctx, st, projectID)
	}
}

// projectHasNetwork 是数据服务可达性前置（零网项目的库不可达）。
func projectHasNetwork(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		nets, err := st.Networks.ListByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if len(nets) == 0 {
			return apperr.New("E_CONFLICT",
				"project %s has no networks; a database attaches the project's active networks and would be unreachable - create a network first ('fleetly networks create --project %s default')",
				projectID, projectID)
		}
		return nil
	}
}

// databaseQuota 是 per-Project 数据库数量配额。
func databaseQuota(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.Databases.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= databasetemplate.Kind.Quota {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d databases (limit %d); delete unused ones first",
				projectID, n, databasetemplate.Kind.Quota)
		}
		return nil
	}
}

// pubsubQuota 是 per-Project PubSub 数量配额。
func pubsubQuota(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.PubSubs.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= pubsubtemplate.Kind.Quota {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d pubsubs (limit %d); delete unused ones first",
				projectID, n, pubsubtemplate.Kind.Quota)
		}
		return nil
	}
}

// bucketStoreQuota 是 per-Project Bucket Store 数量配额。
func bucketStoreQuota(projectID string) grpcapi.Check {
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.BucketStores.CountByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if n >= bucketstoretemplate.Kind.Quota {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d bucket stores (limit %d); delete unused ones first",
				projectID, n, bucketstoretemplate.Kind.Quota)
		}
		return nil
	}
}

// 数据库面事件名（usage 反扫的字面量锚点）。
const (
	eventDatabaseCreated       = "database.created"
	eventDatabaseBrowserOpened = "database.browser_opened"
	eventDatabaseDeleted       = "database.deleted"
	// 轮换事实（IA v3 二期⑤b）：engine 改密 + Secret 重写后由受理位与
	// 审计同事务落账（DeleteDatabase 同款先变更后留痕序）。
	eventDatabaseRotated = "database.credentials_rotated"
	// 期望态变更事实（B2d，ADR-0095 决策 5/9）：Update 双车道执行后由
	// 受理位与审计同事务落账（先变更后留痕；载荷只携键集不携值——旋钮
	// 值经 Database 行回显面可见，事件面保持最小披露）。
	eventDatabaseUpdated = "database.updated"
)

// per-Project 活跃数据库上限（ADR-0017 配额族；库是长驻真实资源，保守缺省
// 16）自 B2e 起随kind descriptor 自述——真源 databasetemplate.Kind.Quota
//（ADR-0096 决策 1），受理门 acceptance.go databaseQuota 直读，本文件不再
// 持第二份常量。
