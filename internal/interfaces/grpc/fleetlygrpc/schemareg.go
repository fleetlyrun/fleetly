package fleetlygrpc

// 事件 payload schema 自描述注册（F1.4，架构 §7"扩展面各自注入合并"）：
// api 层拥有结构面/身份面/触发面事件 payload 形状，在本 init 反射进
// internal/schema 注册表。身份面 payload 原为 ad-hoc map——schema 钉扎
// 要求形状具名单源（下方结构体），发射点共用同一类型，wire 字节不变。
// summary 单源取 eventcode 注册表；完备性由 internal/assembly 守卫对账。

import (
	"github.com/fleetlyrun/fleetly/internal/application/schema"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/alertinggrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/execgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/githubappsgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/governancegrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/hooksgrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/identitygrpc"
)

// ---- 身份面 payload（字段只增；与原 map 构造的字节形态逐一等同） ----

// registerEventPayload 登记一个事件名的 payload schema（summary 取
// eventcode 单源；未在册事件名 panic）。
func registerEventPayload(name string, payload any) {
	e, ok := eventcode.Get(name)
	if !ok {
		panic("fleetlygrpc: schema registration for event " + name + " which is not in the eventcode registry")
	}
	if payload == nil {
		schema.Register(schema.KindEvent, name, e.Summary, schema.Null())
		return
	}
	schema.Register(schema.KindEvent, name, e.Summary, schema.Reflect(payload))
}

func init() {
	// 结构面（structureEventPayload，structure.go 单源）。
	for _, name := range []string{
		eventProjectCreated,
		eventProjectDeleted,
		eventAppCreated,
		eventAppDeleted,
		eventAppTeardownAborted,
		eventSecretUpdated,
		eventSecretDeleted,
		eventConfigUpdated,
		eventVariableUpdated,
		eventVariableDeleted,
		eventAppVarUpdated,
		eventAppVarDeleted,
		eventVolumeCreated,
		eventVolumeDeleted,
		eventVolumePurged,
		eventNetworkCreated,
	} {
		registerEventPayload(name, structureEventPayload{})
	}
	// 跨 Project peer 三拍（networkPeerEventPayload，structure.go 单源）。
	for _, name := range []string{
		eventNetworkPeerDeclared,
		eventNetworkPeerApproved,
		eventNetworkPeerRevoked,
	} {
		registerEventPayload(name, networkPeerEventPayload{})
	}
	// 网络重建（ADR-0046；networkRebuiltEventPayload，structure.go 单源）。
	registerEventPayload(eventNetworkRebuilt, networkRebuiltEventPayload{})
	// 身份面。
	registerEventPayload("user.created", identitygrpc.NameEventPayload{})
	registerEventPayload("team.created", identitygrpc.NameEventPayload{})
	registerEventPayload("role.created", identitygrpc.RoleEventPayload{})
	registerEventPayload("token.created", identitygrpc.NameEventPayload{})
	registerEventPayload("token.revoked", nil) // 恒 null 载荷
	registerEventPayload("invitation.created", identitygrpc.InvitationCreatedPayload{})
	registerEventPayload("invitation.accepted", identitygrpc.InvitationAcceptedPayload{})
	// 触发面。
	registerEventPayload("hook.push_accepted", hooksgrpc.HookPushAcceptedPayload{})
	registerEventPayload("hook.refresh_accepted", hooksgrpc.HookRefreshAcceptedPayload{})
	// 治理面（FreezeEventPayload，governancegrpc 单源）。
	registerEventPayload(governancegrpc.EventFreezeSet, governancegrpc.FreezeEventPayload{})
	registerEventPayload(governancegrpc.EventFreezeLifted, governancegrpc.FreezeEventPayload{})
	// 上传产物面（uploadStoredPayload，uploads.go 单源；F1.10）。
	registerEventPayload(eventUploadStored, uploadStoredPayload{})
	// 数据库面（structureEventPayload，databases.go 单源；F1.12/ADR-0029）。
	registerEventPayload(eventDatabaseCreated, structureEventPayload{})
	registerEventPayload(eventDatabaseDeleted, structureEventPayload{})
	registerEventPayload(eventDatabaseRotated, structureEventPayload{})
	// 期望态变更（B2d，ADR-0095 决策 5/9；database_update.go 单源）。
	registerEventPayload(eventDatabaseUpdated, structureEventPayload{})
	// pubsub 类型四件（B4b，ADR-0096 决策 8；载荷同 structureEventPayload）。
	registerEventPayload(eventPubSubCreated, structureEventPayload{})
	registerEventPayload(eventPubSubUpdated, structureEventPayload{})
	registerEventPayload(eventPubSubDeleted, structureEventPayload{})
	registerEventPayload(eventPubSubRotated, structureEventPayload{})
	// bucket_store 类型四件（B5，ADR-0096 决策 8；载荷同 structureEventPayload）。
	registerEventPayload(eventBucketStoreCreated, structureEventPayload{})
	registerEventPayload(eventBucketStoreUpdated, structureEventPayload{})
	registerEventPayload(eventBucketStoreDeleted, structureEventPayload{})
	registerEventPayload(eventBucketStoreRotated, structureEventPayload{})
	// 子资源三类型九件（B8，ADR-0096 决策 10 / design §4.7；载荷同
	// structureEventPayload——id 字段携带子资源行 id，配置明细在审计 Detail）。
	registerEventPayload(eventTopicCreated, structureEventPayload{})
	registerEventPayload(eventTopicUpdated, structureEventPayload{})
	registerEventPayload(eventTopicDeleted, structureEventPayload{})
	registerEventPayload(eventStreamCreated, structureEventPayload{})
	registerEventPayload(eventStreamUpdated, structureEventPayload{})
	registerEventPayload(eventStreamDeleted, structureEventPayload{})
	registerEventPayload(eventBucketCreated, structureEventPayload{})
	registerEventPayload(eventBucketUpdated, structureEventPayload{})
	registerEventPayload(eventBucketDeleted, structureEventPayload{})
	// Browse 会话受理（F3.6，ADR-0051 决策 1：安全可见性——谁在何时打开了
	// 哪个 Database 的浏览器；载荷同 structureEventPayload（id 字段携带
	// 库 ID；会话明细在审计 Detail）。
	registerEventPayload(eventDatabaseBrowserOpened, structureEventPayload{})
	// Exec 会话受理（ExecEventPayload，execgrpc 单源；F3.2/ADR-0049——
	// 安全可见性：谁在何时进入了哪个进程）。
	registerEventPayload("exec.session_opened", execgrpc.ExecEventPayload{})
	// App 模板面（templates.go 单源；F3.3，ADR-0050——实例化摘要与目录刷新台账。
	// refreshed 前缀归一：templates → template，P2 批二裁决）。
	registerEventPayload(eventTemplateInstantiated, templateEventPayload{})
	registerEventPayload(eventTemplateRefreshed, templateRefreshedPayload{})

	// 四族 CRUD 补事件（P2 批二，声明级遗漏收口）：hook/github_app/
	// alert_rule/channel 的写面此前只有审计无事件——payload 各自单源。
	registerEventPayload(hooksgrpc.EventHookCreated, hooksgrpc.HookEventPayload{})
	registerEventPayload(hooksgrpc.EventHookUpdated, hooksgrpc.HookEventPayload{})
	registerEventPayload(hooksgrpc.EventHookRotated, hooksgrpc.HookEventPayload{})
	registerEventPayload(hooksgrpc.EventHookDeleted, hooksgrpc.HookEventPayload{})
	registerEventPayload(githubappsgrpc.EventGitHubAppConnected, githubappsgrpc.GitHubAppEventPayload{})
	registerEventPayload(githubappsgrpc.EventGitHubAppDeleted, githubappsgrpc.GitHubAppEventPayload{})
	registerEventPayload(alertinggrpc.EventAlertRuleCreated, alertinggrpc.AlertRuleEventPayload{})
	registerEventPayload(alertinggrpc.EventAlertRuleDeleted, alertinggrpc.AlertRuleEventPayload{})
	registerEventPayload(alertinggrpc.EventChannelCreated, alertinggrpc.ChannelEventPayload{})
	registerEventPayload(alertinggrpc.EventChannelDeleted, alertinggrpc.ChannelEventPayload{})
}
