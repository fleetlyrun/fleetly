// dataservice.go 是托管数据服务应用模块的核/模块契约面（engine 去应用模块
// import 第九批，2026-11-10）：engine 核只持 DataService 接口与构造端口袋，
// 具体装配器由组合根经 engine.Deps.DataServiceFactory 注入——核不再 import
// dataservices 应用模块包。共享值类型/哨兵/纯公式随契约面下沉（gRPC 与
// assembly 构造面的既名引用经 engine/dataservices 别名零改）；端口形态全部
// 落在 domain 面（capability/database/pubsub/bucketstore 签名），契约叶子
// 保持零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// 数据服务族哨兵（API 层 errors.Is 判定锚；engine/dataservices 经 var 别名
// 零漂移，词面与历史逐字节一致）。
var (
	// ErrRotateRejected 是「数据面改密被数据库拒绝」的哨兵（API 层 map 为
	// E_DATABASE_ROTATE_FAILED 的判别锚）。
	ErrRotateRejected = errors.New("database rejected the credential change")
	// ErrSettingsApplyRejected 是「数据面热应用被数据库拒绝」的哨兵（API 层
	// map 为 E_SETTINGS_APPLY_FAILED 的判别锚；包裹报文携带工具容器
	// stderr/stdout 尾部）。
	ErrSettingsApplyRejected = errors.New("database rejected the settings change")
	// ErrVolumeForeignOrigin 是「数据服务自动卷携带异源实例标记」哨兵（api
	// 面 map 为 E_CONFLICT 带清理/换名指引；ensure 链 = 拒绝确保、载体保持
	// 现状）。
	ErrVolumeForeignOrigin = errors.New("managed data service volume carries a foreign instance marker")
)

// ResourceUpdate 是 Update 的资源面输入（nil 指针 = 该轴无限制；外层 nil
// 消息 = 资源面不变——与 settings 的 nil 语义同构）。
type ResourceUpdate struct {
	CPUMillis *int32
	MemoryMB  *int32
}

// UpdateRequest 是 database Update 动词的域输入（声明式全量替换语义：nil
// 字段 = 不变面；settings 非 nil = 全量替换期望态，空 map = 清空全部旋钮）。
type UpdateRequest struct {
	Settings  map[string]string
	Resources *ResourceUpdate
	// Admit 是受理临界区钩子（B2d，ADR-0089/0095 决策 9 确认闭环）：在
	// utilityMu 内、读行之后执行之前调用（锁内行 = 执行真源）——调用方在
	// 此复核 status 门并完成确认票内容绑定。返回的 redeem 在执行成功后、
	// 仍持锁时调用（烧票）；失败路径不调用——票保留可重试。返回错误 =
	// 整体拒绝（行与引擎零变更），原样上抛。nil = 无受理钩子（应用模块内部
	// 调用面）。
	Admit func(row *database.Database) (redeem func(), err error)
}

// UpdateResult 是两车道执行注记（受理位响应与审计 detail 的数据源；键集
// 排序稳定——确定性序列化纪律）。
type UpdateResult struct {
	// DynamicApplied 是本拍同步热应用成功的 dynamic 键集（引擎已生效并
	// 自持久；含移除复位键；空 = 无 dynamic 变更或未变重放的 no-op）。
	DynamicApplied []string
	// StaticRolled 是落行并触发滚动的 static 键集（异步收敛——受监督
	// 重启发；空 = 无 static 变更）。
	StaticRolled []string
}

// PubSubUpdateRequest 是 pubsub Update 的域输入（UpdateRequest 同形；Admit
// 钩子按 pubsub 行签名——锁内行 = 执行真源）。
type PubSubUpdateRequest struct {
	Settings  map[string]string
	Resources *ResourceUpdate
	Admit     func(row *pubsub.PubSub) (redeem func(), err error)
}

// BucketStoreUpdateRequest 是 bucketstore Update 的域输入（PubSubUpdateRequest
// 同形；Admit 钩子按 bucketstore 行签名——锁内行 = 执行真源）。
type BucketStoreUpdateRequest struct {
	Settings  map[string]string
	Resources *ResourceUpdate
	Admit     func(row *bucketstore.BucketStore) (redeem func(), err error)
}

// 子资源实况解析面别名（删除披露行消费；真源在 domain 模板包——契约叶子
// 只做既名可达，不复制结构）。
type (
	TopicLive  = pubsubtemplate.TopicLive
	StreamLive = pubsubtemplate.StreamLive
	BucketLive = bucketstoretemplate.BucketLive
)

// IsDatabaseCredentialSecret 报告 Secret 名是否落在数据库凭证保留前缀
// （API PutSecret 的受理契约测试消费：用户覆写会破坏连接串单真源——值必须
// 恒为完整连接 URL，ADR-0029 决策 6）。
func IsDatabaseCredentialSecret(name string) bool {
	return strings.HasPrefix(name, DBCredentialSecretPrefix)
}

// DatabaseConnectionURL 铸连接串（API 创建面消费的单源公式；组合 =
// 模板查询 + 铸名 + 模板 ConnURL）。host 是网内 DNS 名 db-<id>——只在
// 项目网内可解析，这是诚实的连接面。B2c 起按行值绑版（version 来自
// databases 行；URL 方言跨版本无分叉，行为不变——绑版是纪律不是分叉面）。
func DatabaseConnectionURL(engineName, version, databaseID, password string) (string, error) {
	tpl, err := databasetemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return tpl.ConnURL(DatabaseDNSName(databaseID), password), nil
}

// IsPubSubCredentialSecret 报告 Secret 名是否落在 pubsub 凭证保留前缀
// （API PutSecret 受理契约测试消费：用户覆写会破坏连接材料单真源）。
func IsPubSubCredentialSecret(name string) bool {
	return strings.HasPrefix(name, PubCredentialSecretPrefix)
}

// PubSubConnMaterial 铸连接材料方言串（API 创建面消费的单源公式：模板
// 查询 + 铸名 + 模板 ConnMaterial → RenderConnMaterial——Secret 值 = 类型
// 方言串，nats URL 形 / kafka k=v 串，B6a 起双方言单源）。host = pub-<id>
// （只在项目网内可解析）。
func PubSubConnMaterial(engineName, version, pubsubID, password string) (string, error) {
	tpl, err := pubsubtemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return pubsubtemplate.RenderConnMaterial(tpl.ConnMaterial(PubSubDNSName(pubsubID), password)), nil
}

// IsBucketStoreCredentialSecret 报告 Secret 名是否落在 bucketstore 凭证保留
// 前缀（API PutSecret 受理契约测试消费：用户覆写会破坏连接材料单真源）。
func IsBucketStoreCredentialSecret(name string) bool {
	return strings.HasPrefix(name, BKTCredentialSecretPrefix)
}

// BucketStoreConnMaterial 铸连接材料方言串（API 创建面消费的单源公式：模板
// 查询 + 铸名 + 模板 ConnMaterial）。host = bkt-<id>（只在项目网内可解析）；
// access key = 平台合成用户（SiloPlatformAccessKey）。
func BucketStoreConnMaterial(engineName, version, bucketStoreID, secretKey string) (string, error) {
	tpl, err := bucketstoretemplate.For(engineName, version)
	if err != nil {
		return "", err
	}
	return tpl.ConnMaterial(BucketStoreDNSName(bucketStoreID), bucketstoretemplate.SiloPlatformAccessKey, secretKey), nil
}

// DataService 是托管数据服务应用模块的核消费面（ADR-0096 决策 1 单应用
// 模块包）：engine 循环表三条目经 Module.Loops 取分类型步，gRPC Database/
// PubSub/BucketStore 受理面、materialize/task 的 env 注入面、render/task/
// bluegreen 的 egress 投影事实面经其余方法（消费点零改）。
type DataService interface {
	Module
	// EgressNetworkMap 返回 Project 活跃网络的 egress:none 名集（投影期
	// 网络事实面；查询失败如实返回空集）。
	EgressNetworkMap(ctx context.Context, projectID string) map[string]bool
	TeardownDatabase(ctx context.Context, id string) error
	RotateDatabasePassword(ctx context.Context, databaseID string) error
	UpdateDatabase(ctx context.Context, databaseID string, req UpdateRequest) (UpdateResult, error)
	CheckVolumeOriginForCreate(ctx context.Context, kind, projectID, name, engineName string) error
	TeardownPubSub(ctx context.Context, id string) error
	RotatePubSubPassword(ctx context.Context, pubsubID string) error
	UpdatePubSub(ctx context.Context, pubsubID string, req PubSubUpdateRequest) (UpdateResult, error)
	TeardownBucketStore(ctx context.Context, id string) error
	RotateBucketStorePassword(ctx context.Context, bucketStoreID string) error
	UpdateBucketStore(ctx context.Context, bucketStoreID string, req BucketStoreUpdateRequest) (UpdateResult, error)
	TopicLiveInspect(ctx context.Context, pubsubID, topicID string) (TopicLive, error)
	StreamLiveInspect(ctx context.Context, pubsubID, streamID string) (StreamLive, error)
	BucketLiveInspect(ctx context.Context, bucketStoreID, bucketID string) (BucketLive, error)
	InjectDatabaseEnv(ctx context.Context, spec *specv1.AppSpec, ws []capability.Workload, projectID string) error
	InjectPubSubEnv(ctx context.Context, spec *specv1.AppSpec, ws []capability.Workload, projectID string) error
	InjectBucketStoreEnv(ctx context.Context, spec *specv1.AppSpec, ws []capability.Workload, projectID string) error
	ApplyDatabaseEnv(ctx context.Context, w *capability.Workload, p *specv1.ProcessSpec, projectID string) error
	ApplyPubSubEnv(ctx context.Context, w *capability.Workload, p *specv1.ProcessSpec, projectID string) error
	ApplyBucketStoreEnv(ctx context.Context, w *capability.Workload, p *specv1.ProcessSpec, projectID string) error
}

// DataServicePorts 是数据服务装配器的构造端口袋（组合根在 engine
// construct.go 绑定核方法值/闭包与 repo；形态与 dataservices.Deps 逐字段
// 同形，契约叶子只依赖 domain + transact——应用模块包类型不得穿过本叶子）。
type DataServicePorts struct {
	Runtime capability.Runtime
	Repos   transact.Stores
	Cipher  *material.Cipher
	Utility capability.RuntimeUtility
	Log     *slog.Logger
	Clock   state.Clock

	LockMaintenance              func() func()
	BoundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh                  EnsureFresh
	EnsureRemember               EnsureRemember
	EnsureForget                 EnsureForget
	AppliedRemember              AppliedRemember
	AppliedForget                AppliedForget
	AppliedDigestOf              AppliedDigestOf
	ProjectTeam                  func(ctx context.Context, projectID string) (string, error)
	ProjectTeamOf                func(projectID string) string
	ProjectNetworkFacts          func(ctx context.Context, projectID string) ([]string, map[string]bool)
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	EnsureDatabaseVolume         func(ctx context.Context, row *database.Database) (*volume.Volume, error)
	DatabasePassword             func(ctx context.Context, row *database.Database) (string, error)
	PinVolumes                   func(ctx context.Context, ws []capability.Workload, projectID string) error
	ApplyVolumePinning           func(ctx context.Context, ws []capability.Workload, projectID string) error
	GuardVolumeCarriers          func(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, projectID string) error
	ControlPlaneNode             func(ctx context.Context) (string, error)
	KickDatabases                func()
	// RecordOwners/RecordExpected/ForgetExpected/ForgetObservation/
	// ObservationOf 是观测窄接口的 database 域窄化端口（核绑定闭包内铸造
	// databaseOwner——域判别留核）。
	RecordOwners      func(databaseID string, gen uint64, ws []capability.Workload)
	RecordExpected    func(databaseID string, gen uint64)
	ForgetExpected    func(databaseID string)
	ForgetObservation func(databaseID string)
	ObservationOf     func(databaseID string) (capability.WorkloadEvent, bool)

	// pubsub 类型端口（B4b）。
	EnsurePubSubVolume   func(ctx context.Context, row *pubsub.PubSub) (*volume.Volume, error)
	PubSubPassword       func(ctx context.Context, row *pubsub.PubSub) (string, error)
	KickPubSubs          func()
	RecordPubSubOwners   func(pubsubID string, gen uint64, ws []capability.Workload)
	RecordPubSubExpected func(pubsubID string, gen uint64)
	ForgetPubSubExpected func(pubsubID string)
	// EnsurePubSubMemo 是 per-PubSub Ensure 确保缓存表本体（组合根 make 后注入）。
	EnsurePubSubMemo map[string]Memo

	// bucketstore 类型端口（B5；观测读/忘口复用 database 轴的
	// ObservationOf/ForgetObservation——缓存键 = 投影 Workload ID = 行 ID 的
	// 恒等跨类型成立，pubsub 类型同款复用）。
	EnsureBucketStoreVolume   func(ctx context.Context, row *bucketstore.BucketStore) (*volume.Volume, error)
	BucketStoreSecretKey      func(ctx context.Context, row *bucketstore.BucketStore) (string, error)
	KickBucketStores          func()
	RecordBucketStoreOwners   func(bucketStoreID string, gen uint64, ws []capability.Workload)
	RecordBucketStoreExpected func(bucketStoreID string, gen uint64)
	ForgetBucketStoreExpected func(bucketStoreID string)
	// EnsureBucketStoreMemo 是 per-BucketStore Ensure 确保缓存表本体（组合根
	// make 后注入）。
	EnsureBucketStoreMemo map[string]Memo
	// SubResourceMemo 是子资源对账段确保缓存表本体（B8，组合根 make 后注入；
	// pubsub/bucketstore 两类型共用——键带类型前缀分持）。
	SubResourceMemo map[string]Memo

	// EnsureMemo 是 per-Database Ensure 确保缓存表本体（组合根 make 后注入）。
	EnsureMemo map[string]Memo
	// AppliedMemo 是 per-Database dynamic 热应用摘要表本体（B2d，ADR-0095
	// 决策 6；组合根 make 后注入——metastatewrite D3 同纪律）。
	AppliedMemo map[string]string
}

// DataServiceFactory 由组合根注入（engine.Deps.DataServiceFactory），把端口
// 袋装配成 DataService。engine 核不 import 应用模块包；工厂缺席（手动测试
// 装配）时数据服务面停用：三条目环退化为 no-op 步，门面 nil-safe。
type DataServiceFactory func(DataServicePorts) DataService
