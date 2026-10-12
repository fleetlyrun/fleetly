import { Link } from "@tanstack/react-router";
import { useQueries, useQuery } from "@tanstack/react-query";
import { ChevronDownIcon, ChevronRightIcon, LayersIcon } from "lucide-react";
import { useState } from "react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { apiFetch } from "@/api/client";
import type { components } from "@/api/delivery";
import { digestByRevisionId, latestDeploymentPerApp } from "@/features/registry/registry-model";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { ListCard } from "@/components/domain/list-page";
import { ModulePage } from "@/components/domain/module-page";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { useApps } from "@/lib/catalog";
import { useProjectDeployments } from "@/features/deployments/hooks";
import { formatBytes } from "@/lib/format";

type Revision = components["schemas"]["v1Revision"];

// Registry v1（IA v3 T5，§5.1 一期；模板化收编 ModulePage+ListCard）：
// 按 app 维度的"当前运行内容"视图——最新部署的 revision digest（内容寻
//址锚）+ commit + 状态。二期（⑤b）：镜像仓内容视图点亮——catalog/tags
// 经受管仓只读代理（按项目前纲）。
export function RegistryView({
  projectId,
  tab,
  onTabChange,
}: {
  projectId: string;
  tab: "apps" | "catalog";
  onTabChange: (value: "apps" | "catalog") => void;
}) {
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

  const columns: LegacyColumnDef<(typeof rows)[number], any>[] = [
    {
      id: "app",
      header: "App",
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.app.id} label={row.original.app.name} />
          <div className="text-sm font-semibold">{row.original.app.name}</div>
        </div>
      ),
    },
    {
      id: "deployment",
      header: "Latest deployment",
      cell: ({ row }) =>
        row.original.deployment ? (
          <div className="flex items-center gap-2">
            <DeploymentStatusBadge state={row.original.deployment.state} />
            <span className="font-mono text-xs text-muted-foreground">{row.original.deployment.id?.slice(0, 10)}…</span>
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">never deployed</span>
        ),
    },
    {
      id: "digest",
      header: "Revision digest",
      cell: ({ row }) => {
        const revision = row.original.deployment?.to_revision;
        if (revision == null || !digests.has(revision)) {
          return <span className="text-xs text-muted-foreground">—</span>;
        }
        return (
          <span className="flex items-center gap-1 font-mono text-xs">
            {digests.get(revision)!.slice(0, 19)}…
            <CopyButton value={digests.get(revision) ?? ""} />
          </span>
        );
      },
    },
    {
      id: "commit",
      header: "Commit",
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.deployment?.commit_sha || "—"}</span>,
    },
    {
      id: "deployed",
      header: "Deployed",
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          <RelativeTime value={row.original.deployment?.finished_at ?? row.original.deployment?.created_at} />
        </span>
      ),
    },
    {
      id: "history",
      header: () => <span className="sr-only">History</span>,
      cell: ({ row }) => (
        <div className="text-right">
          <Link
            to="/p/$projectId/apps/$appId/deployments"
            params={{ projectId, appId: row.original.app.id }}
            className="text-xs text-info underline-offset-2 hover:underline"
          >
            deployments
          </Link>
        </div>
      ),
    },
  ];

  return (
    <ModulePage
      title="Registry"
      description="What each app runs right now — revision digests from the managed registry (zot)"
      tabs={[
        { value: "apps", label: "Apps" },
        { value: "catalog", label: "Image catalog" },
      ]}
      current={tab}
      onTabChange={onTabChange}
    >
      {tab === "catalog" ? (
        <RegistryCatalog projectId={projectId} apps={apps.data ?? []} />
      ) : (
        <>
          <ListCard
            label="apps"
            placeholder="Filter apps..."
            rows={rows}
            fieldsOf={(row) => [row.app.name ?? "", row.app.id ?? ""]}
            loading={apps.isPending}
            error={apps.isError ? apps.error : null}
            onRetry={() => void apps.refetch()}
            columns={columns}
            empty={
              <EmptyState
                icon={LayersIcon}
                title="No apps in this project"
                description="Registry rows appear per app once apps exist — each row resolves the running revision digest."
              />
            }
          />
          <p className="mt-3 text-2xs text-muted-foreground">
            Digests are content-addressed — they identify exactly what is running.
          </p>
        </>
      )}
    </ModulePage>
  );
}

// RegistryCatalog 镜像仓内容视图（IA v3 二期⑤b）：项目前纲下的仓名清单
//（受管仓只读代理），逐仓名展开 tag 事实（digest/压缩大小/推送时刻）。
// 上游缺席诚实呈现（代理面未配置 = 明确的不可用文案，不伪装空清单）。
function RegistryCatalog({ projectId, apps }: { projectId: string; apps: Array<{ id: string; name: string }> }) {
  const [openRepo, setOpenRepo] = useState<string | null>(null);
  const catalog = useQuery({
    queryKey: ["registry", "catalog", projectId],
    queryFn: async (): Promise<string[]> => {
      const res = await apiFetch<{ repositories?: Array<string | undefined> }>(
        `/v1/registry/catalog?project_id=${encodeURIComponent(projectId)}`,
      );
      return (res.repositories ?? []).flatMap((entry) => (entry != null ? [entry] : []));
    },
    retry: false,
    refetchInterval: 60_000,
  });
  const appName = (repo: string) => apps.find((app) => app.id.toLowerCase() === repo.split("/")[1])?.name ?? repo.split("/")[1];
  return (
    <>
      {catalog.isPending ? (
        <p className="text-xs text-muted-foreground">Loading repositories…</p>
      ) : catalog.isError ? (
        <p className="rounded-xl border bg-card p-4 text-xs text-muted-foreground">
          Registry content is unavailable on this platform ({catalog.error instanceof Error ? catalog.error.message : "unknown error"}).
        </p>
      ) : (catalog.data ?? []).length === 0 ? (
        <p className="rounded-xl border bg-card p-4 text-xs text-muted-foreground">
          No images pushed under this project yet — repositories appear after the first build.
        </p>
      ) : (
        <div className="overflow-hidden rounded-xl border">
          {(catalog.data ?? []).map((repo) => (
            <div key={repo} className="border-b last:border-b-0">
              <button
                type="button"
                className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-muted/40"
                onClick={() => setOpenRepo(openRepo === repo ? null : repo)}
              >
                {openRepo === repo ? (
                  <ChevronDownIcon className="size-4 text-muted-foreground" />
                ) : (
                  <ChevronRightIcon className="size-4 text-muted-foreground" />
                )}
                <span className="font-mono text-xs">{repo}</span>
                <span className="text-sm font-semibold">{appName(repo)}</span>
              </button>
              {openRepo === repo ? <RegistryTags projectId={projectId} repo={repo} /> : null}
            </div>
          ))}
        </div>
      )}
      <CliEquivalent command={`fleetly registry catalog --project ${projectId}`} />
    </>
  );
}

// RegistryTags 单仓名的 tag 事实表（展开时拉取；错误行内诚实呈现）。
function RegistryTags({ projectId, repo }: { projectId: string; repo: string }) {
  const tags = useQuery({
    queryKey: ["registry", "tags", projectId, repo],
    queryFn: async () => {
      const res = await apiFetch<{
        repository?: string;
        tags?: Array<{ name?: string; digest?: string; size_bytes?: string | number; pushed_at?: string } | undefined>;
      }>(`/v1/registry/${encodeURIComponent(repo)}/tags?project_id=${encodeURIComponent(projectId)}`);
      return (res.tags ?? []).flatMap((entry) => (entry != null ? [entry] : []));
    },
    retry: false,
  });
  if (tags.isPending) {
    return <p className="px-3 pb-2 pl-9 text-xs text-muted-foreground">Loading tags…</p>;
  }
  if (tags.isError) {
    return <p className="px-3 pb-2 pl-9 text-xs text-destructive">{tags.error instanceof Error ? tags.error.message : "tag listing failed"}</p>;
  }
  return (
    <table className="mb-1 ml-6 w-[calc(100%-1.5rem)] text-xs">
      <thead className="text-left text-2xs font-semibold tracking-wide text-muted-foreground uppercase">
        <tr>
          <th className="px-2 py-1">Tag</th>
          <th className="px-2 py-1">Digest</th>
          <th className="px-2 py-1">Size</th>
          <th className="px-2 py-1">Pushed</th>
        </tr>
      </thead>
      <tbody>
        {tags.data.map((tag) => (
          <tr key={tag.name} className="border-t">
            <td className="px-2 py-1 font-mono">{tag.name}</td>
            <td className="px-2 py-1 font-mono">
              <span className="flex items-center gap-1">
                {tag.digest ? `${tag.digest.slice(0, 19)}…` : "—"}
                {tag.digest ? <CopyButton value={tag.digest} /> : null}
              </span>
            </td>
            <td className="px-2 py-1">{tag.size_bytes ? formatBytes(tag.size_bytes) : "—"}</td>
            <td className="px-2 py-1">
              <RelativeTime value={tag.pushed_at} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
