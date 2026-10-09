import { Link } from "@tanstack/react-router";
import { useQueries } from "@tanstack/react-query";
import { LayersIcon } from "lucide-react";
import { apiFetch } from "@/api/client";
import type { components } from "@/api/delivery";
import { digestByRevisionId, latestDeploymentPerApp } from "@/features/registry/registry-model";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { useApps } from "@/lib/catalog";
import { useProjectDeployments } from "@/features/deployments/hooks";

type Revision = components["schemas"]["v1Revision"];

// Registry v1（IA v3 T5，§5.1 一期）：按 app 维度的"当前运行内容"视图——
// 最新部署的 revision digest（内容寻址锚）+ commit + 状态。完整 tag 清单/
// image ref 随二期 zot catalog 代理与 spec 读取通路点亮（§8）。
export function RegistryView({ projectId }: { projectId: string }) {
  const apps = useApps(projectId);
  const appIds = (apps.data ?? []).map((app) => app.id);
  const deployments = useProjectDeployments(appIds);
  const latest = latestDeploymentPerApp(deployments.data);

  // revisions fan-out（digest 解析）：与 deployments fan-out 同款客户端聚合
  //（ADR-0057 不变量：项目级聚合走客户端，服务端 per-App 轴不动）。
  const revisionLists = useQueries({
    queries: appIds.slice(0, 50).map((appId) => ({
      queryKey: ["resources", "revisions", appId],
      queryFn: async (): Promise<Revision[]> => {
        const res = await apiFetch<{ revisions?: Array<Revision | undefined> }>(`/v1/revisions?app_id=${encodeURIComponent(appId)}&limit=200`);
        return (res.revisions ?? []).flatMap((entry) => (entry != null ? [entry] : []));
      },
      enabled: appIds.length > 0,
      refetchInterval: 30_000,
    })),
  });
  const digests = digestByRevisionId(revisionLists.map((entry) => entry.data));
  const rows = (apps.data ?? []).map((app) => ({ app, deployment: latest.get(app.id) }));

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title="Registry"
        description="What each app runs right now — revision digests from the managed registry (zot)"
      />
      {rows.length === 0 ? (
        <EmptyState
          icon={LayersIcon}
          title="No apps in this project"
          description="Registry rows appear per app once apps exist — each row resolves the running revision digest."
        />
      ) : (
        <div className="overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-3 py-2">App</th>
                <th className="px-3 py-2">Latest deployment</th>
                <th className="px-3 py-2">Revision digest</th>
                <th className="px-3 py-2">Commit</th>
                <th className="px-3 py-2">Deployed</th>
                <th className="px-3 py-2 text-right">History</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ app, deployment }) => (
                <tr key={app.id} className="border-b last:border-b-0">
                  <td className="px-3 py-2">
                    <div className="flex items-center gap-2.5">
                      <ProjectAvatar seed={app.id} label={app.name} />
                      <div className="text-[13px] font-semibold">{app.name}</div>
                    </div>
                  </td>
                  <td className="px-3 py-2">
                    {deployment ? (
                      <div className="flex items-center gap-2">
                        <DeploymentStatusBadge state={deployment.state} />
                        <span className="font-mono text-xs text-muted-foreground">{deployment.id?.slice(0, 10)}…</span>
                      </div>
                    ) : (
                      <span className="text-xs text-muted-foreground">never deployed</span>
                    )}
                  </td>
                  <td className="px-3 py-2">
                    {deployment?.to_revision != null && digests.has(deployment.to_revision) ? (
                      <span className="flex items-center gap-1 font-mono text-xs">
                        {digests.get(deployment.to_revision)!.slice(0, 19)}…
                        <CopyButton value={digests.get(deployment.to_revision) ?? ""} />
                      </span>
                    ) : (
                      <span className="text-xs text-muted-foreground">—</span>
                    )}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{deployment?.commit_sha || "—"}</td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    <RelativeTime value={deployment?.finished_at ?? deployment?.created_at} />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Link
                      to="/p/$projectId/apps/$appId/deployments"
                      params={{ projectId, appId: app.id }}
                      className="text-xs text-info underline-offset-2 hover:underline"
                    >
                      deployments
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-3 text-[11.5px] text-muted-foreground">
        Phase 2 adds the full tag listing via a credentialed zot catalog proxy and image refs via the app spec read path
        (IA v3 §8). Digests are content-addressed — they identify exactly what is running.
      </p>
    </div>
  );
}
