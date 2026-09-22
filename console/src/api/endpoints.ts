// 端点封装：页面只经此消费平台 REST 面（端点清单 = proto google.api.http
// 派生，见 proto/fleetly/server/v1/*.proto）。Console 不新增/绕过端点。

import { api, utf8ToBase64 } from "./client";
import type {
  CancelDeploymentResponse,
  CreateDatabaseResponse,
  DeleteDatabaseResponse,
  DeployResponse,
  DeploymentView,
  GetAppResponse,
  GetDatabaseResponse,
  GetEnvResponse,
  GetIngressStatusResponse,
  GetJoinGuideResponse,
  GetRevisionSpecResponse,
  GetS3SettingsResponse,
  GetSystemStatusResponse,
  ListAppDomainsResponse,
  ListBackupsResponse,
  ListCronRunsResponse,
  ListDatabaseBackupsResponse,
  ListDeploymentsResponse,
  ListDatabasesResponse,
  ListEnvResponse,
  ListHistoryLogsResponse,
  ListNodesResponse,
  ListRevisionsResponse,
  ListSecretsResponse,
  ListAppsResponse,
  PlacementView,
  RemoveEnvResponse,
  RemoveSecretResponse,
  RestoreDatabaseBackupResponse,
  ResumeDatabaseResponse,
  RetryDatabaseResponse,
  RevealDatabaseCredentialsResponse,
  RollbackDeploymentResponse,
  RotateDatabaseCredentialsResponse,
  RotateJoinTokenResponse,
  SearchLogsResponse,
  SearchSource,
  SetEnvResponse,
  SetSecretResponse,
  SuspendDatabaseResponse,
  TestS3ConnectionRequest,
  TestS3ConnectionResponse,
  TriggerBackupResponse,
  TriggerCronRunResponse,
  TriggerDatabaseBackupResponse,
  UpgradeDatabaseResponse,
  UpdateS3SettingsRequest,
  UpdateS3SettingsResponse,
  VerifyAppDomainsResponse,
  VolumeView,
} from "./types";

// ── apps ────────────────────────────────────────────────────────────────

export function listApps() {
  return api<ListAppsResponse>("/apps");
}

export function getApp(name: string) {
  return api<GetAppResponse>(`/apps/${encodeURIComponent(name)}`);
}

export function deleteApp(name: string) {
  return api<{ name: string; lifecycle: string }>(
    `/apps/${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
}

// ── deployments ─────────────────────────────────────────────────────────

export function listDeployments(app: string, limit = 20) {
  return api<ListDeploymentsResponse>(
    `/apps/${encodeURIComponent(app)}/deployments?limit=${limit}`,
  );
}

export function getDeployment(id: string) {
  return api<{ deployment: DeploymentView }>(
    `/deployments/${encodeURIComponent(id)}`,
  );
}

/** Deploy：compose 内容字节按 proto bytes 契约 base64 上行。 */
export function deploy(app: string, composeText: string) {
  return api<DeployResponse>(`/apps/${encodeURIComponent(app)}/deployments`, {
    method: "POST",
    rawBody: { compose: utf8ToBase64(composeText) },
  });
}

export function cancelDeployment(id: string) {
  return api<CancelDeploymentResponse>(
    `/deployments/${encodeURIComponent(id)}/cancel`,
    { method: "POST", json: {} },
  );
}

export function rollbackDeployment(app: string, targetRevisionId?: string) {
  return api<RollbackDeploymentResponse>(
    `/apps/${encodeURIComponent(app)}/rollbacks`,
    {
      method: "POST",
      json: targetRevisionId ? { target_revision_id: targetRevisionId } : {},
    },
  );
}

// ── revisions ───────────────────────────────────────────────────────────

export function listRevisions(app: string) {
  return api<ListRevisionsResponse>(
    `/apps/${encodeURIComponent(app)}/revisions`,
  );
}

export function getRevisionSpec(app: string, revisionId: string) {
  return api<GetRevisionSpecResponse>(
    `/apps/${encodeURIComponent(app)}/revisions/${encodeURIComponent(revisionId)}/spec`,
  );
}

// ── env ─────────────────────────────────────────────────────────────────

export function listEnv(app: string) {
  return api<ListEnvResponse>(`/apps/${encodeURIComponent(app)}/env`);
}

export function getEnv(app: string, key: string) {
  return api<GetEnvResponse>(
    `/apps/${encodeURIComponent(app)}/env/${encodeURIComponent(key)}`,
  );
}

export function setEnv(app: string, key: string, value: string) {
  return api<SetEnvResponse>(
    `/apps/${encodeURIComponent(app)}/env/${encodeURIComponent(key)}`,
    { method: "PUT", json: { value } },
  );
}

export function removeEnv(app: string, key: string) {
  return api<RemoveEnvResponse>(
    `/apps/${encodeURIComponent(app)}/env/${encodeURIComponent(key)}`,
    { method: "DELETE" },
  );
}

// ── domains ─────────────────────────────────────────────────────────────

export function listDomains(app: string) {
  return api<ListAppDomainsResponse>(
    `/apps/${encodeURIComponent(app)}/domains`,
  );
}

export function verifyDomains(app: string) {
  return api<VerifyAppDomainsResponse>(
    `/apps/${encodeURIComponent(app)}/domains/verify`,
    { method: "POST", json: {} },
  );
}

// ── logs（历史检索走 /logs；实时跟随走 stream.ts 的 NDJSON 流；日志库
// 统一检索走 /logs/search——W5-S2）──────────────────────────────────────

export function listHistoryLogs(
  app: string,
  opts: {
    service?: string;
    since?: string;
    until?: string;
    limit?: number;
    source?: "container" | "build" | "";
  } = {},
) {
  const query: Record<string, string> = {};
  if (opts.service) query.service = opts.service;
  if (opts.since) query.since = opts.since;
  if (opts.until) query.until = opts.until;
  if (opts.limit !== undefined) query.limit = String(opts.limit);
  if (opts.source) query.source = opts.source;
  const qs = new URLSearchParams(query).toString();
  return api<ListHistoryLogsResponse>(
    `/apps/${encodeURIComponent(app)}/logs${qs ? `?${qs}` : ""}`,
  );
}

/**
 * 日志库统一检索（VictoriaLogs LogsQL 后端）：时间倒序 + 游标分页（服务端
 * 签发 next_cursor）。sources/services 为可选过滤集（重复 query key 形态
 * ——gateway 对 repeated 字段同时接受重复键与 CSV）。VL 不可达 / jsonl 模
 * 式 → E_LOGS_BACKEND_UNAVAILABLE 信封（诚实报错，不返回空列表冒充）。
 */
export function searchLogs(
  app: string,
  opts: {
    keyword?: string;
    services?: string[];
    sources?: SearchSource[];
    since?: string;
    until?: string;
    limit?: number;
    cursor?: string;
  } = {},
) {
  const query: Record<string, string> = {};
  if (opts.keyword) query.keyword = opts.keyword;
  if (opts.since) query.time_start = opts.since;
  if (opts.until) query.time_end = opts.until;
  if (opts.limit !== undefined) query.limit = String(opts.limit);
  if (opts.cursor) query.cursor = opts.cursor;
  const params = new URLSearchParams(query);
  for (const s of opts.services ?? []) params.append("services", s);
  for (const s of opts.sources ?? []) params.append("sources", s);
  const qs = params.toString();
  return api<SearchLogsResponse>(
    `/apps/${encodeURIComponent(app)}/logs/search${qs ? `?${qs}` : ""}`,
  );
}

// ── system ──────────────────────────────────────────────────────────────

export function getSystemStatus() {
  return api<GetSystemStatusResponse>("/system/status");
}

export function listNodes() {
  return api<ListNodesResponse>("/system/nodes");
}

/** join 向导（E1-8；admin scope——响应含 join token 材料）。 */
export function getJoinGuide(workerIp?: string, managerAddr?: string) {
  const query: Record<string, string> = {};
  if (workerIp) query.worker_ip = workerIp;
  if (managerAddr) query.manager_addr = managerAddr;
  const qs = new URLSearchParams(query).toString();
  return api<GetJoinGuideResponse>(
    `/system/nodes/join-guide${qs ? `?${qs}` : ""}`,
  );
}

/** 轮换 swarm join token（E1-8，D-MN-1；旧 token 即刻失效）。 */
export function rotateJoinToken(role: "worker" | "manager" = "worker") {
  return api<RotateJoinTokenResponse>("/system/nodes/join-token:rotate", {
    method: "POST",
    json: { role },
  });
}

export function getIngressStatus() {
  return api<GetIngressStatusResponse>("/system/ingress");
}

// ── backups（状态备份台账，T2.22/E3-3）──────────────────────────────────

/** 台账只读面（created_at 倒序）。 */
export function listBackups() {
  return api<ListBackupsResponse>("/system/backups");
}

/** 手动触发一次状态备份（响应即落账后的台账行）。 */
export function triggerBackup() {
  return api<TriggerBackupResponse>("/system/backups", {
    method: "POST",
    json: { kind: "manual" },
  });
}

// ── S3 设置面（E3 对象存储 §5.1/E3-2，admin scope）──────────────────────

/** 设置只读投影：secret 只回 fingerprint，读面永无明文。 */
export function getS3Settings() {
  return api<GetS3SettingsResponse>("/system/s3");
}

/** 全量保存（PUT 语义：请求即新状态；secret 明文只写）。 */
export function updateS3Settings(req: UpdateS3SettingsRequest) {
  return api<UpdateS3SettingsResponse>("/system/s3", { method: "PUT", json: req });
}

/**
 * 连接探针（put→get→delete 单轮真实读写）：传候选配置即「先测后存」；
 * 全空 = 测已存配置。
 */
export function testS3Connection(req: TestS3ConnectionRequest) {
  return api<TestS3ConnectionResponse>("/system/s3:test", {
    method: "POST",
    json: req,
  });
}

// ── cron（E5 Cron，架构 §4.3）───────────────────────────────────────────

/**
 * 手动触发一次（与到点触发同链路：重叠/节点不可用不报错——响应携带
 * skipped 行与原因）。
 */
export function triggerCronRun(app: string, service: string) {
  return api<TriggerCronRunResponse>(
    `/apps/${encodeURIComponent(app)}/services/${encodeURIComponent(service)}/trigger`,
    { method: "POST", json: {} },
  );
}

/** 运行台账（scheduled_at 倒序；service 空 = 该 app 全部 schedule 的行）。 */
export function listCronRuns(app: string, service?: string, limit = 20) {
  const query: Record<string, string> = {};
  if (service) query.service = service;
  if (limit !== undefined) query.limit = String(limit);
  const qs = new URLSearchParams(query).toString();
  return api<ListCronRunsResponse>(
    `/apps/${encodeURIComponent(app)}/cron-runs${qs ? `?${qs}` : ""}`,
  );
}

export function getPlacement(app: string) {
  return api<{
    app: string;
    placement?: PlacementView;
    volumes?: VolumeView[];
  }>(`/apps/${encodeURIComponent(app)}/placement`);
}

// ── databases（E4 数据库托管，managed-databases §5.1/§5.2）───────────────

export function listDatabases() {
  return api<ListDatabasesResponse>("/databases");
}

export function getDatabase(name: string) {
  return api<GetDatabaseResponse>(`/databases/${encodeURIComponent(name)}`);
}

export function createDatabase(req: {
  name: string;
  template: string;
  limits?: { cpu_seconds?: number; memory_bytes?: string };
}) {
  return api<CreateDatabaseResponse>("/databases", { method: "POST", json: req });
}

export function deleteDatabase(name: string, opts: { confirm: string; delete_volumes: boolean }) {
  return api<DeleteDatabaseResponse>(`/databases/${encodeURIComponent(name)}`, {
    method: "DELETE",
    json: opts,
  });
}

export function suspendDatabase(name: string) {
  return api<SuspendDatabaseResponse>(`/databases/${encodeURIComponent(name)}/suspend`, {
    method: "POST",
    json: {},
  });
}

export function resumeDatabase(name: string) {
  return api<ResumeDatabaseResponse>(`/databases/${encodeURIComponent(name)}/resume`, {
    method: "POST",
    json: {},
  });
}

export function retryDatabase(name: string) {
  return api<RetryDatabaseResponse>(`/databases/${encodeURIComponent(name)}/retry`, {
    method: "POST",
    json: {},
  });
}

export function rotateDatabaseCredentials(name: string, confirm: string) {
  return api<RotateDatabaseCredentialsResponse>(`/databases/${encodeURIComponent(name)}/rotate`, {
    method: "POST",
    json: { confirm },
  });
}

/**
 * 连接信息显式展开（admin 面动作；密码明文只出现在本响应——显式 reveal
 * 才取，取到即前台展示、隐藏即弃，不做任何持久化）。
 */
export function revealDatabaseCredentials(name: string) {
  return api<RevealDatabaseCredentialsResponse>(
    `/databases/${encodeURIComponent(name)}/credentials`,
  );
}

export function listDatabaseBackups(name: string, limit = 20) {
  return api<ListDatabaseBackupsResponse>(
    `/databases/${encodeURIComponent(name)}/backups?limit=${limit}`,
  );
}

export function triggerDatabaseBackup(name: string) {
  return api<TriggerDatabaseBackupResponse>(`/databases/${encodeURIComponent(name)}/backups`, {
    method: "POST",
    json: { kind: "manual" },
  });
}

export function restoreDatabaseBackup(name: string, snapshot: string, confirm: string) {
  return api<RestoreDatabaseBackupResponse>(`/databases/${encodeURIComponent(name)}/restore`, {
    method: "POST",
    json: { snapshot, confirm },
  });
}

export function upgradeDatabase(name: string, confirm: string) {
  return api<UpgradeDatabaseResponse>(`/databases/${encodeURIComponent(name)}/upgrade`, {
    method: "POST",
    json: { confirm },
  });
}

// ── platform secrets（E4 §2.7 平台密钥库；无值读回）─────────────────────

export function listSecrets(app: string) {
  return api<ListSecretsResponse>(`/apps/${encodeURIComponent(app)}/secrets`);
}

export function setSecret(app: string, name: string, value: string) {
  return api<SetSecretResponse>(`/apps/${encodeURIComponent(app)}/secrets`, {
    method: "POST",
    json: { app, name, value },
  });
}

export function removeSecret(app: string, name: string) {
  return api<RemoveSecretResponse>(
    `/apps/${encodeURIComponent(app)}/secrets/${encodeURIComponent(name)}`,
    { method: "DELETE" },
  );
}
