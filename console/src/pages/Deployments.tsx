import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { components } from "../api/delivery";
import { apiFetch } from "../api/client";
import { appNameOf, useApps, useProjects } from "../lib/catalog";
import { EmptyNote, ErrorNote, LoadingNote, PageShell, formatTime, shortId } from "../components/ui";

// 部署状态页（F2.6/ADR-0044 决策 1）：部署列表是 per-App 轴（API 语义：
// app_id 必填）——页面 = 项目过滤 + App 必选（自动选首个）+ 轮询 5s；
// 行展开看 from/to Revision、generation 与失败详情（纯只读面）。

type Deployment = components["schemas"]["v1Deployment"];

interface DeploymentsResponse {
  deployments?: Array<Deployment | undefined>;
}

const POLL_MS = 5_000;

// STATE_STYLES 是 Deployment.state 值域的徽章配色（未知态灰）。
const STATE_STYLES: Record<string, string> = {
  queued: "bg-slate-800 text-slate-300",
  running: "bg-sky-950 text-sky-300 border border-sky-800",
  observing: "bg-sky-950 text-sky-300 border border-sky-800",
  releasing: "bg-sky-950 text-sky-300 border border-sky-800",
  succeeded: "bg-emerald-950 text-emerald-300 border border-emerald-800",
  failed: "bg-red-950 text-red-300 border border-red-900",
  canceled: "bg-amber-950 text-amber-300 border border-amber-900",
  superseded: "bg-slate-800 text-slate-400",
};

function stateBadgeClass(state: string | undefined): string {
  return STATE_STYLES[state ?? ""] ?? "bg-slate-800 text-slate-400";
}

export function DeploymentsPage() {
  const [projectId, setProjectId] = useState("");
  const [appId, setAppId] = useState("");
  const projects = useProjects();
  const apps = useApps(projectId);

  // App 目录到达后自动选首个（无选择 = 无列表——per-App 轴语义）。
  const effectiveAppId = appId || apps.data?.[0]?.id || "";

  const deployments = useQuery({
    queryKey: ["deployments", effectiveAppId],
    queryFn: async (): Promise<Deployment[]> => {
      const query = `app_id=${encodeURIComponent(effectiveAppId)}&limit=50`;
      const res = await apiFetch<DeploymentsResponse>(`/v1/deployments?${query}`);
      return (res.deployments ?? []).flatMap((item) => (item?.id ? [item] : []));
    },
    enabled: effectiveAppId !== "",
    refetchInterval: POLL_MS,
  });

  return (
    <PageShell
      title="Deployments"
      hint="per-app list, refreshes every 5 s (GET /v1/deployments)"
      toolbar={
        <>
          <select
            value={projectId}
            onChange={(event) => {
              setProjectId(event.target.value);
              setAppId("");
            }}
            className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-sm text-slate-200"
          >
            <option value="">All projects</option>
            {(projects.data ?? []).map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </select>
          <select
            value={effectiveAppId}
            onChange={(event) => setAppId(event.target.value)}
            className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1 text-sm text-slate-200"
          >
            {(apps.data ?? []).length === 0 ? <option value="">no apps yet</option> : null}
            {(apps.data ?? []).map((app) => (
              <option key={app.id} value={app.id}>
                {app.name}
              </option>
            ))}
          </select>
        </>
      }
    >
      {projects.isPending || apps.isPending ? (
        <LoadingNote label="Loading catalog…" />
      ) : projects.isError ? (
        <ErrorNote error={projects.error} hint="GET /v1/projects failed — check the API token in the header." />
      ) : apps.isError ? (
        <ErrorNote error={apps.error} hint="GET /v1/apps failed — check the API token in the header." />
      ) : (apps.data ?? []).length === 0 ? (
        <EmptyNote label="No apps yet — deploy something first (fleetly deploy or POST /v1/deployments)." />
      ) : deployments.isError ? (
        <ErrorNote error={deployments.error} hint="GET /v1/deployments failed — check the API token in the header." />
      ) : deployments.isPending ? (
        <LoadingNote label="Loading deployments…" />
      ) : deployments.data.length === 0 ? (
        <EmptyNote label="No deployments for this app yet." />
      ) : (
        <div className="overflow-x-auto rounded-lg border border-slate-800">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-800 bg-slate-900/60 text-left text-xs uppercase tracking-wide text-slate-500">
                <th className="px-3 py-2 font-medium">App</th>
                <th className="px-3 py-2 font-medium">State</th>
                <th className="px-3 py-2 font-medium">Revision</th>
                <th className="px-3 py-2 font-medium">Generation</th>
                <th className="px-3 py-2 font-medium">Updated</th>
              </tr>
            </thead>
            <tbody>
              {deployments.data.map((item) => (
                <DeploymentRow key={item.id} item={item} apps={apps.data} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </PageShell>
  );
}

// DeploymentRow：一行部署；error/详情经 details 展开（无 JS 状态面）。
function DeploymentRow({ item, apps }: { item: Deployment; apps: ReturnType<typeof useApps>["data"] }) {
  const active = item.state === "running" || item.state === "observing" || item.state === "releasing" || item.state === "queued";
  return (
    <tr className="border-b border-slate-800/60 align-top hover:bg-slate-900/40">
      <td className="px-3 py-2">
        <div className="font-medium text-slate-200">{appNameOf(apps, item.app_id)}</div>
        <div className="font-mono text-xs text-slate-500" title={item.id ?? ""}>
          {shortId(item.id)}
        </div>
      </td>
      <td className="px-3 py-2">
        <span className={`inline-block rounded px-1.5 py-0.5 text-xs font-medium ${stateBadgeClass(item.state)}`}>
          {item.state ?? "unknown"}
          {active ? <span className="ml-1 animate-pulse">●</span> : null}
        </span>
        {item.error ? (
          <div className="mt-1 max-w-md truncate font-mono text-xs text-red-400" title={item.error}>
            {item.error}
          </div>
        ) : null}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-slate-400">
        {item.from_revision ? <div title={item.from_revision}>{shortId(item.from_revision)} →</div> : <div>∅ →</div>}
        <div title={item.to_revision ?? ""} className="text-slate-300">
          {shortId(item.to_revision)}
        </div>
      </td>
      <td className="px-3 py-2 font-mono text-xs text-slate-400">{item.generation ?? "—"}</td>
      <td className="px-3 py-2 text-xs text-slate-400">
        <div>{formatTime(item.updated_at)}</div>
        <details className="mt-1">
          <summary className="cursor-pointer text-slate-500 hover:text-slate-300">details</summary>
          <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 font-mono text-xs text-slate-500">
            <dt>created_at</dt>
            <dd>{formatTime(item.created_at)}</dd>
            <dt>finished_at</dt>
            <dd>{item.finished_at ? formatTime(item.finished_at) : "—"}</dd>
            <dt>commit</dt>
            <dd>{item.commit_sha || "—"}</dd>
            <dt>idempotency</dt>
            <dd>{item.idempotency_key || "—"}</dd>
            <dt>superseded_by</dt>
            <dd>{item.superseded_by || "—"}</dd>
            <dt>first_boot_task</dt>
            <dd>{item.first_boot_task_id || "—"}</dd>
          </dl>
        </details>
      </td>
    </tr>
  );
}
