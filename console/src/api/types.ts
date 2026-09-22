// REST 面类型：与 proto 消息直接对应的接口一律引用 schema.d.ts 的生成类型
// （openapi-typescript 从 genproto/fleetly/server/v1/*.swagger.json 生成，
// `pnpm gen:api` 再生成，CI 以"再生成无 diff"门禁拦漂移——D4-②）。
//
// 生成类型的两个口径（与 gateway 实际输出一致，勿按直觉"纠正"）：
//   - 字段全部可选：gateway JSON marshaler EmitUnpopulated=false，proto3
//     零值字段不出现在 JSON 输出；
//   - int64 一律字符串（proto3 JSON 映射），Timestamp 是 RFC3339 字符串。
//
// 仅 gateway 传输形态投影（NDJSON result 包裹，stream-types.ts）与页面局部
// 视图模型保留手写——它们不在 proto 消息面内。

import type { components } from "./schema";

type Schemas = components["schemas"];

// ── apps ────────────────────────────────────────────────────────────────

export type AppView = Schemas["v1AppView"];
export type ListAppsResponse = Schemas["v1ListAppsResponse"];

// ── placement ────────────────────────────────────────────────────────────

export type PlacementView = Schemas["v1PlacementView"];
export type VolumeView = Schemas["v1VolumeView"];

// ── join wizard（E1-8，multi-node §2.3）──────────────────────────────────

export type JoinGuideView = Schemas["v1JoinGuideView"];
export type GetJoinGuideResponse = Schemas["v1GetJoinGuideResponse"];
export type RotateJoinTokenResponse = Schemas["v1RotateJoinTokenResponse"];
export type FirewallRule = Schemas["v1FirewallRule"];

// ── deployments ──────────────────────────────────────────────────────────

export type DeploymentView = Schemas["v1DeploymentView"];
export type GetAppResponse = Schemas["v1GetAppResponse"];
export type ComposeWarning = Schemas["v1ComposeWarning"];
export type DeployResponse = Schemas["v1DeployResponse"];
export type ListDeploymentsResponse = Schemas["v1ListDeploymentsResponse"];
export type CancelDeploymentResponse = Schemas["v1CancelDeploymentResponse"];
export type RollbackDeploymentResponse = Schemas["v1RollbackDeploymentResponse"];

// ── revisions ────────────────────────────────────────────────────────────

export type RevisionView = Schemas["v1RevisionView"];
export type ListRevisionsResponse = Schemas["v1ListRevisionsResponse"];
export type GetRevisionSpecResponse = Schemas["v1GetRevisionSpecResponse"];

// ── env ──────────────────────────────────────────────────────────────────

export type EnvVarView = Schemas["v1EnvVarView"];
export type ListEnvResponse = Schemas["v1ListEnvResponse"];
export type GetEnvResponse = Schemas["v1GetEnvResponse"];
export type SetEnvResponse = Schemas["v1SetEnvResponse"];
export type RemoveEnvResponse = Schemas["v1RemoveEnvResponse"];

// ── domains ──────────────────────────────────────────────────────────────

export type DomainView = Schemas["v1DomainView"];
export type ListAppDomainsResponse = Schemas["v1ListAppDomainsResponse"];
export type DomainCheckView = Schemas["v1DomainCheckView"];
export type VerifyAppDomainsResponse = Schemas["v1VerifyAppDomainsResponse"];

// ── logs ─────────────────────────────────────────────────────────────────

export type LogEntryView = Schemas["v1LogEntryView"];
export type ListHistoryLogsResponse = Schemas["v1ListHistoryLogsResponse"];
export type SearchLogRow = Schemas["v1SearchLogRow"];
export type SearchLogsResponse = Schemas["v1SearchLogsResponse"];

/** 检索来源过滤词表（proto sources repeated string 的消费侧词表）。 */
export type SearchSource = "container" | "build" | "access";

// ── metrics（E6 W5-S3，D-W5-2 opt-in）────────────────────────────────────

export type MetricsComponentView = Schemas["v1MetricsComponentView"];
export type MetricsPoint = Schemas["v1MetricsPoint"];
export type MetricsSeries = Schemas["v1MetricsSeries"];
export type SearchMetricsResponse = Schemas["v1SearchMetricsResponse"];
export type GetMetricsStatusResponse = Schemas["v1GetMetricsStatusResponse"];
export type SetMetricsModeResponse = Schemas["v1SetMetricsModeResponse"];

/** metrics.mode 词表（proto SetMetricsModeRequest.mode 消费侧词表）。 */
export type MetricsMode = "unset" | "on";

// ── notifications（E6 W5-S4 通知 Webhook；observability §5）──────────────

export type WebhookEndpointView = Schemas["v1WebhookEndpointView"];
export type ListWebhookEndpointsResponse = Schemas["v1ListWebhookEndpointsResponse"];
export type GetWebhookEndpointResponse = Schemas["v1GetWebhookEndpointResponse"];
export type CreateWebhookEndpointResponse = Schemas["v1CreateWebhookEndpointResponse"];
export type UpdateWebhookEndpointResponse = Schemas["v1UpdateWebhookEndpointResponse"];
export type DeleteWebhookEndpointResponse = Schemas["v1DeleteWebhookEndpointResponse"];
export type RotateWebhookSecretResponse = Schemas["v1RotateWebhookSecretResponse"];
export type TestWebhookResponse = Schemas["v1TestWebhookResponse"];
export type WebhookDeliveryView = Schemas["v1WebhookDeliveryView"];
export type ListWebhookDeliveriesResponse = Schemas["v1ListWebhookDeliveriesResponse"];

/** 投递状态词表（proto status 过滤消费侧词表；failed = 终态）。 */
export type WebhookDeliveryStatus = "pending" | "ok" | "failed";

// ── terminal（E7 W5-S6 Web 终端；web-terminal §2.5）──────────────────────

export type CreateTerminalTicketResponse = Schemas["v1CreateTerminalTicketResponse"];
export type GetTerminalStatusResponse = Schemas["v1GetTerminalStatusResponse"];

// ── system ───────────────────────────────────────────────────────────────

export type ComponentHealth = Schemas["v1ComponentHealth"];
export type GetSystemStatusResponse = Schemas["v1GetSystemStatusResponse"];
export type NodeView = Schemas["v1NodeView"];
export type ListNodesResponse = Schemas["v1ListNodesResponse"];
export type TraefikView = Schemas["v1TraefikView"];
export type CertLedgerView = Schemas["v1CertLedgerView"];
export type GetIngressStatusResponse = Schemas["v1GetIngressStatusResponse"];

// ── backups（T2.22 台账 + E3-3 上传轨）───────────────────────────────────

export type BackupView = Schemas["v1BackupView"];
export type ListBackupsResponse = Schemas["v1ListBackupsResponse"];
export type TriggerBackupResponse = Schemas["v1TriggerBackupResponse"];

// ── S3 设置面（E3 对象存储 §5.1/E3-2）────────────────────────────────────

export type S3SettingsView = Schemas["v1S3SettingsView"];
export type GetS3SettingsResponse = Schemas["v1GetS3SettingsResponse"];
export type UpdateS3SettingsRequest = Schemas["v1UpdateS3SettingsRequest"];
export type UpdateS3SettingsResponse = Schemas["v1UpdateS3SettingsResponse"];
export type TestS3ConnectionRequest = Schemas["v1TestS3ConnectionRequest"];
export type TestS3ConnectionResponse = Schemas["v1TestS3ConnectionResponse"];
export type S3ConnectionTestResult = Schemas["v1S3ConnectionTestResult"];
export type S3ProbeStep = Schemas["v1S3ProbeStep"];

// ── cron（E5 Cron，架构 §4.3）────────────────────────────────────────────

export type CronRunView = Schemas["v1CronRunView"];
export type ListCronRunsResponse = Schemas["v1ListCronRunsResponse"];
export type TriggerCronRunResponse = Schemas["v1TriggerCronRunResponse"];

// ── databases（E4 数据库托管，managed-databases §5.1）────────────────────

export type DatabaseView = Schemas["v1DatabaseView"];
export type DatabaseVolumeView = Schemas["v1DatabaseVolumeView"];
export type DatabaseConnectionView = Schemas["v1DatabaseConnectionView"];
export type DatabaseLimits = Schemas["v1DatabaseLimits"];
export type DatabaseBackupPlan = Schemas["v1DatabaseBackupPlan"];
export type ListDatabasesResponse = Schemas["v1ListDatabasesResponse"];
export type GetDatabaseResponse = Schemas["v1GetDatabaseResponse"];
export type CreateDatabaseResponse = Schemas["v1CreateDatabaseResponse"];
export type DeleteDatabaseResponse = Schemas["v1DeleteDatabaseResponse"];
export type SuspendDatabaseResponse = Schemas["v1SuspendDatabaseResponse"];
export type ResumeDatabaseResponse = Schemas["v1ResumeDatabaseResponse"];
export type RetryDatabaseResponse = Schemas["v1RetryDatabaseResponse"];
export type RotateDatabaseCredentialsResponse = Schemas["v1RotateDatabaseCredentialsResponse"];
export type RevealDatabaseCredentialsResponse = Schemas["v1RevealDatabaseCredentialsResponse"];
export type DatabaseBackupView = Schemas["v1DatabaseBackupView"];
export type ListDatabaseBackupsResponse = Schemas["v1ListDatabaseBackupsResponse"];
export type TriggerDatabaseBackupResponse = Schemas["v1TriggerDatabaseBackupResponse"];
export type RestoreDatabaseBackupResponse = Schemas["v1RestoreDatabaseBackupResponse"];
export type UpgradeDatabaseResponse = Schemas["v1UpgradeDatabaseResponse"];

// ── platform secrets（E4 §2.7 平台密钥库；无值读回面）────────────────────

export type SecretView = Schemas["v1SecretView"];
export type ListSecretsResponse = Schemas["v1ListSecretsResponse"];
export type SetSecretResponse = Schemas["v1SetSecretResponse"];
export type RemoveSecretResponse = Schemas["v1RemoveSecretResponse"];

// ── events ───────────────────────────────────────────────────────────────

export type EventView = Schemas["v1EventView"];
export type CursorExpiredView = Schemas["v1CursorExpiredView"];

/** WatchEvents 的 oneof frame 投影（UseProtoNames → snake_case 成员名）。 */
export type WatchEventsFrame = Schemas["v1WatchEventsResponse"];
