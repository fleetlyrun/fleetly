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

// ── events ───────────────────────────────────────────────────────────────

export type EventView = Schemas["v1EventView"];
export type CursorExpiredView = Schemas["v1CursorExpiredView"];

/** WatchEvents 的 oneof frame 投影（UseProtoNames → snake_case 成员名）。 */
export type WatchEventsFrame = Schemas["v1WatchEventsResponse"];
