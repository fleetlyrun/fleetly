// REST 面类型：与 proto/fleetly/server/v1/*.proto 的消息一一对应——字段名
// 按 gateway 的 UseProtoNames 输出（snake_case，proto 声明名）。google.
// protobuf.Timestamp 在 JSON 中是 RFC3339 字符串。

export interface AppView {
  id: string;
  name: string;
  /** active | deleting | deleted */
  lifecycle: string;
  /** running | degraded | blocked | down（读面即时推导） */
  derived_state: string;
  created_at?: string;
  updated_at?: string;
}

export interface ListAppsResponse {
  apps: AppView[];
}

export interface PlacementView {
  platform_node_id: string;
  label_ref: string;
  /** bound | blocked | unresolved */
  state: string;
  reason: string;
  source: string;
  pinned_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface VolumeView {
  key: string;
  name: string;
  kind: string;
  node_id: string;
  mount_path: string;
  status: string;
}

export interface DeploymentView {
  id: string;
  app: string;
  /** deploy | rollback */
  kind: string;
  /** queued/preparing/building/releasing/observing/succeeded/failed/cancelled */
  status: string;
  /** 子状态（blocked_waiting 或空） */
  phase: string;
  revision_id: string;
  error_code: string;
  verdict: string;
  recovery: string;
  substrate_halted: boolean;
  first_healthy_at?: string;
  /** proto int64 → JSON 字符串 */
  downtime_ms: number | string;
  created_at?: string;
  updated_at?: string;
  source_git_sha?: string;
  source_git_ref?: string;
}

export interface GetAppResponse {
  id: string;
  name: string;
  lifecycle: string;
  derived_state: string;
  created_at?: string;
  updated_at?: string;
  placement?: PlacementView;
  recent_deployments?: DeploymentView[];
}

export interface ComposeWarning {
  field: string;
  warning: string;
}

export interface DeployResponse {
  deployment_id: string;
  app: string;
  status: string;
  warnings?: ComposeWarning[];
}

export interface ListDeploymentsResponse {
  deployments: DeploymentView[];
}

export interface CancelDeploymentResponse {
  id: string;
  status: string;
}

export interface RollbackDeploymentResponse {
  deployment_id: string;
  app: string;
  status: string;
}

export interface RevisionView {
  id: string;
  /** proto int64 → JSON 字符串 */
  seq: string;
  desired_hash: string;
  /** active = 可回滚选项；superseded = 淘汰存档 */
  status: string;
  verified: boolean;
  created_at?: string;
}

export interface ListRevisionsResponse {
  revisions: RevisionView[];
}

export interface GetRevisionSpecResponse {
  revision_id: string;
  seq: number;
  compose: string;
}

export interface EnvVarView {
  key: string;
  /** platform | system */
  source: string;
  /** pending | effective */
  status: string;
  created_at?: string;
  updated_at?: string;
}

export interface ListEnvResponse {
  env_vars: EnvVarView[];
}

export interface GetEnvResponse {
  app: string;
  key: string;
  value: string;
  status: string;
}

export interface SetEnvResponse {
  app: string;
  key: string;
  status: string;
}

export interface RemoveEnvResponse {
  app: string;
  key: string;
  status: string;
}

export interface DomainView {
  service: string;
  domain: string;
  /** '' = 未同步 */
  port: string;
  /** '' = 尚无证书 */
  cert_sha256: string;
  cert_not_after?: string;
  created_at?: string;
}

export interface ListAppDomainsResponse {
  domains: DomainView[];
}

export interface DomainCheckView {
  domain: string;
  ips: string[];
  resolved: boolean;
  http_80: string;
  https_443: string;
  cert_subject: string;
  cert_dns_names: string[];
  cert_not_after?: string;
  error: string;
}

export interface VerifyAppDomainsResponse {
  checks: DomainCheckView[];
}

export interface LogEntryView {
  app: string;
  service: string;
  at?: string;
  stderr: boolean;
  line: string;
  /** container | build */
  source: string;
}

export interface ListHistoryLogsResponse {
  entries: LogEntryView[];
}

export interface ComponentHealth {
  name: string;
  ok: boolean;
  error: string;
}

export interface GetSystemStatusResponse {
  service: string;
  version: string;
  components: ComponentHealth[];
}

export interface NodeView {
  swarm_node_id: string;
  platform_id: string;
  hostname: string;
  state: string;
  availability: string;
  is_manager: boolean;
  observed_at?: string;
  stale: boolean;
  labels: Record<string, string>;
}

export interface ListNodesResponse {
  nodes: NodeView[];
}

export interface TraefikView {
  exists: boolean;
  image: string;
  static_args: number;
  error: string;
}

export interface CertLedgerView {
  app: string;
  domain: string;
  cert_sha256: string;
  cert_not_after?: string;
}

export interface GetIngressStatusResponse {
  traefik: TraefikView;
  config_addr: string;
  advertise_ip: string;
  responder: string;
  healthz: string;
  auth: string;
  certificates: CertLedgerView[];
  cert_dir: string;
  cert_dir_apps: string[];
  cert_dir_error: string;
}

export interface EventView {
  /** proto int64 → JSON 字符串（proto3 JSON 映射） */
  seq: string;
  at?: string;
  name: string;
  subject: string;
  payload: string;
}

export interface CursorExpiredView {
  /** proto int64 → JSON 字符串 */
  oldest_seq: string;
  message: string;
}

/** WatchEvents 的 oneof frame 投影（UseProtoNames → snake_case 成员名）。 */
export interface WatchEventsFrame {
  event?: EventView;
  cursor_expired?: CursorExpiredView;
}
