package datagrpc

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"google.golang.org/grpc"
	"io"
	"sort"
	"strings"
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

// ===== 三类型同构段共享面 =====
// 以下 helper 是 database/pubsub/bucket store 三胞胎 handler 的逐字节同构段
// 单源收编（类型词/票面动词两轴随调用方分立，其余行为零漂移）。

// bindDataUpdateTicket 是三类型 Update 的票面内容绑定（utilityMu 锁内调用）：
// Peek（不烧）核对 Verb/目标/RequestDigest（RequestDigest 绑锁内行的合并后
// 期望态——预览后世界漂移即 MISMATCH），返回烧票闭包（执行成功后仍持锁时
// 调用；失败路径不调用——票保留可重试）。过期/漂移腿 STALE，伪造/错面/已烧
// 腿 MISMATCH（teardown 先例）；六条错误文案逐字单源，kindWord 即名词位
// （"database"/"pubsub"/"bucket store"）。
func bindDataUpdateTicket(tickets Tickets, ticket, verb, kindWord, id, name, digest string) (func(), error) {
	payload, status := tickets.PeekConfirm(ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return nil, apperr.New("E_CONFIRMATION_STALE",
			"the confirmation ticket for updating %s %s has expired", kindWord, name).
			WithSuggestion("Run the update preview again and confirm the fresh ticket.")
	default:
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation (unknown or already redeemed)").
			WithSuggestion("Mint a fresh ticket from the update preview; tickets are single-use.")
	}
	if payload.Verb != verb || payload.TargetApp != id {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different update target").
			WithSuggestion("Mint a fresh preview ticket for this " + kindWord + ".")
	}
	if payload.RequestDigest != digest {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the current desired state of %s %s", kindWord, name).
			WithSuggestion("The " + kindWord + " row moved since the preview (settings or resources). Run the preview again and confirm the fresh ticket.")
	}
	return func() { tickets.DiscardConfirm(ticket) }, nil
}

// dataUpdatePreview 是 dataUpdatePreviewDiff 的回执面：三类型的预演响应消息
// 分立但字段同名同型（SettingsChanges/ResourcesFrom/ResourcesTo 共用
// DatabaseSettingChange/DatabaseResources 消息），proto 组装留在各 handler
// （字段赋值序不变）。
type dataUpdatePreview struct {
	settingsChanges []*structurev1.DatabaseSettingChange
	resourcesFrom   *structurev1.DatabaseResources
	resourcesTo     *structurev1.DatabaseResources
	dynamicKeys     []string
	staticKeys      []string
	rollout         string
	ticket          string
	ticketExpiresIn int32
}

// dataUpdatePreviewDiff 承载三类型 Update 预览的同构计算段：旋钮级 diff
// 循环（变更键集 + 三处排序）、资源面 diff、rollout 判定（dynamic 全集 =
// zero_touch——容器不重启；含 static 或资源变更 = replace——受监督滚动重
// 启发）、diffLines 拼接与 MintConfirm 铸票（票面仅 Verb/TargetApp 两轴随
// 类型分立）。digestOf 在原时点求值（diffLines 之后、铸票之前——受理段
// digest 公式单源在调用方）。
func dataUpdatePreviewDiff(ctx context.Context, tickets Tickets, verb, targetApp string, knobs []dataservicetemplate.Knob,
	rowSettings, nextSettings map[string]string, rowCPU, rowMemory *int32, resourcesRequest *contract.ResourceUpdate,
	digestOf func() (string, error)) (dataUpdatePreview, error) {
	out := dataUpdatePreview{}
	// 旋钮级 diff（变更键集；未变键不进场——声明式重放零扰动的预演面）。
	var dynamicKeys, staticKeys []string
	for key, class := range knobClassesOf(knobs) {
		old, oldOK := rowSettings[key]
		next, nextOK := nextSettings[key]
		if oldOK == nextOK && old == next {
			continue // 未变键不进场。
		}
		out.settingsChanges = append(out.settingsChanges, &structurev1.DatabaseSettingChange{
			Key: key, From: old, To: next, Class: string(class),
		})
		if class == dataservicetemplate.KnobDynamic {
			dynamicKeys = append(dynamicKeys, key)
		} else {
			staticKeys = append(staticKeys, key)
		}
	}
	sort.Strings(dynamicKeys)
	sort.Strings(staticKeys)
	sort.Slice(out.settingsChanges, func(i, j int) bool {
		return out.settingsChanges[i].GetKey() < out.settingsChanges[j].GetKey()
	})
	// 资源面 diff + 滚动后果（ADR-0095 决策 9）。
	nextCPU, nextMemory := rowCPU, rowMemory
	if resourcesRequest != nil {
		nextCPU, nextMemory = resourcesRequest.CPUMillis, resourcesRequest.MemoryMB
	}
	out.resourcesFrom = resourcesMsg(rowCPU, rowMemory)
	out.resourcesTo = resourcesMsg(nextCPU, nextMemory)
	resourcesChanged := !resourcesFaceEqual(rowCPU, rowMemory, nextCPU, nextMemory)
	out.dynamicKeys = dynamicKeys
	out.staticKeys = staticKeys
	out.rollout = "zero_touch"
	if len(staticKeys) > 0 || resourcesChanged {
		out.rollout = "replace"
	}
	// 铸票：RequestDigest 绑合并后期望态（执行面锁内同参数复算——双源
	// 漂移即 MISMATCH）；PreviewDigest 绑预演载荷本体；Operator 烙印操作者。
	diffLines := make([]string, 0, len(out.settingsChanges))
	for _, ch := range out.settingsChanges {
		diffLines = append(diffLines, fmt.Sprintf("%s:%s=%s:%s", ch.GetKey(), ch.GetFrom(), ch.GetTo(), ch.GetClass()))
	}
	digest, derr := digestOf()
	if derr != nil {
		return dataUpdatePreview{}, derr
	}
	ticket, ttl, err := tickets.MintConfirm(governance.ConfirmTicketPayload{
		Verb:          verb,
		TargetApp:     targetApp,
		RequestDigest: digest,
		PreviewDigest: grpcapi.SHA256Hex([]byte(strings.Join(diffLines, "\n"))),
		Operator:      grpcapi.ConfirmOperator(ctx),
	})
	if err != nil {
		return dataUpdatePreview{}, grpcapi.MapStateError(err, "ticket")
	}
	out.ticket = ticket
	//nolint:gosec // TTL 是 600s 常量（confirmTicketTTL），int32 安全。
	out.ticketExpiresIn = int32(ttl.Seconds())
	return out, nil
}

// restoreCompatFn 是版本轴恢复兼容判定的窄函数面（databasetemplate 与
// pubsubtemplate 的 RestoreCompat 同签名——受理 helper 按类型注入）。
type restoreCompatFn func(engine, fromVersion, toVersion string) error

// restoreNoteUntrackedVersion 是恢复源空版本门放行的注记串单源（design
// §2.4 红队 A10：空值门放行 major 降级会在运行时才炸、留半删态；诚实披露
// 优于假验证——database/pubsub 两 Create 面同一字面量）。
const restoreNoteUntrackedVersion = "restore source backup predates engine version tracking; compatibility is unverified - downgrade risk is yours (red team A10)"

// admitRestoreSource 是 Create 恢复源受理块的单源（database/pubsub 同律）：
// 同 Project + kind 一致 + 引擎一致 + 成功有产物 + 版本轴 RestoreCompat（同
// 版本放行、只升不降、pgvector 分轴）；空源版本（旧行「版本未记录」）=
// 同引擎放行 + 注记降级风险自负。返回 (源行, 注记)——注记非空即降级披露。
// 引擎级能力位门（无备份面引擎无恢复源可受）留在调用方——helper 调用之前。
func admitRestoreSource(ctx context.Context, backups backup.Repo, kind string, kindWord string,
	restoreCompat restoreCompatFn, backupID, projectID, engine, toVersion string) (*backup.Backup, string, error) {
	src, err := backups.Get(ctx, backupID)
	if err != nil {
		return nil, "", grpcapi.MapStateError(err, "backup")
	}
	if src.ProjectID != projectID {
		return nil, "", apperr.New("E_INVALID_ARGUMENT",
			"restore_from_backup: backup %s belongs to another project", src.ID)
	}
	if src.Subject.Kind != kind {
		return nil, "", apperr.New("E_INVALID_ARGUMENT",
			"restore_from_backup: backup %s is a %q kind artifact, not a %s backup", src.ID, src.Subject.Kind, kindWord)
	}
	if src.Engine != engine {
		return nil, "", apperr.New("E_INVALID_ARGUMENT",
			"restore_from_backup: backup engine %q does not match the requested engine %q", src.Engine, engine)
	}
	if src.Status != backup.StatusSucceeded || src.ObjectKey == "" {
		return nil, "", apperr.New("E_INVALID_ARGUMENT",
			"restore_from_backup: backup %s is not a succeeded backup with an object", src.ID)
	}
	if src.EngineVersion == "" {
		// 旧行（migration 00039 前）：版本未记录——同引擎放行，受理文案标注。
		return src, restoreNoteUntrackedVersion, nil
	}
	if err := restoreCompat(engine, src.EngineVersion, toVersion); err != nil {
		return nil, "", apperr.New("E_INVALID_ARGUMENT", "restore_from_backup: %s", err)
	}
	return src, "", nil
}

// mapDataTeardownError 折 teardown 的错误面（E_NOT_FOUND/E_INTERNAL 两腿；
// 名词位随类型分立——"database"/"pubsub"/"bucket store"）。
func mapDataTeardownError(err error, kindWord, id string) error {
	if errors.Is(err, state.ErrNotFound) {
		return apperr.New("E_NOT_FOUND", "%s %s not found", kindWord, id)
	}
	return apperr.New("E_INTERNAL", "%s teardown failed", kindWord).WithCause(err)
}

// mapRotateError 折凭证轮换的错误面（E_NOT_FOUND/E_DATABASE_ROTATE_FAILED/
// E_INTERNAL 三腿；名词位随类型分立，E_INTERNAL 文案三类型共用；在服检查
// 门文案留调用方）。
func mapRotateError(err error, kindWord, id string) error {
	if errors.Is(err, state.ErrNotFound) {
		return apperr.New("E_NOT_FOUND", "%s %s not found", kindWord, id)
	}
	if errors.Is(err, api.ErrRotateRejected) {
		return apperr.New("E_DATABASE_ROTATE_FAILED", "%s", err.Error())
	}
	return apperr.New("E_INTERNAL", "credential rotation failed").WithCause(err)
}

// listAllPages 是级联枚举面的分页循环单源（三类型 listActive* 共用）：
// page=200 与 repo maxListLimit 对齐（超出值被钳回）、after 回填末行 Seq、
// len(rows)<page 终止——对任意存量面收敛；list 错误按 kindWord 折状态面。
func listAllPages[T any](kindWord string, fetch func(after int64, limit int) ([]T, error), seqOf func(T) int64) ([]T, error) {
	var out []T
	const page = 200 // 与 repo maxListLimit 对齐（超出值被钳回）
	var after int64
	for {
		rows, err := fetch(after, page)
		if err != nil {
			return nil, grpcapi.MapStateError(err, kindWord)
		}
		out = append(out, rows...)
		if len(rows) < page {
			return out, nil
		}
		after = seqOf(rows[len(rows)-1])
	}
}

// streamChunks 把只读流按 256KiB 分块下发（备份下载流式面 ×2 单源）：每块
// 独立拷贝（append([]byte(nil), chunk[:n]...)——防缓冲复用改写已交付帧）、
// 读尽（含读错）即止——reader 侧错误不外泄、以截断流承载（gateway 帧化，
// 消费端重组）。
func streamChunks(reader io.Reader, send func(data []byte) error) error {
	chunk := make([]byte, 256*1024)
	for {
		n, rerr := reader.Read(chunk)
		if n > 0 {
			if serr := send(append([]byte(nil), chunk[:n]...)); serr != nil {
				return serr
			}
		}
		if rerr != nil {
			break
		}
	}
	return nil
}
