// 端点封装：页面只经此消费平台 REST 面（端点清单 = proto google.api.http
// 派生，见 proto/fleetly/server/v1/*.proto）。Console 不新增/绕过端点。

import { api, utf8ToBase64 } from "./client";
import type {
  CancelDeploymentResponse,
  DeployResponse,
  DeploymentView,
  GetAppResponse,
  GetEnvResponse,
  GetIngressStatusResponse,
  GetJoinGuideResponse,
  GetRevisionSpecResponse,
  GetSystemStatusResponse,
  ListAppDomainsResponse,
  ListDeploymentsResponse,
  ListEnvResponse,
  ListHistoryLogsResponse,
  ListNodesResponse,
  ListRevisionsResponse,
  ListAppsResponse,
  PlacementView,
  RemoveEnvResponse,
  RollbackDeploymentResponse,
  RotateJoinTokenResponse,
  SetEnvResponse,
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

// ── logs（历史检索；实时跟随走 stream.ts 的 NDJSON 流）─────────────────

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

export function getPlacement(app: string) {
  return api<{
    app: string;
    placement?: PlacementView;
    volumes?: VolumeView[];
  }>(`/apps/${encodeURIComponent(app)}/placement`);
}
