import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ChevronDownIcon, ChevronRightIcon } from "lucide-react";
import { useState } from "react";
import { useCollection } from "@lynx-go/uikit-core";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { apiFetch } from "@/api/client";
import { registryResource, registrySearch, type RegistryRow } from "@/kit/registry";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { CopyButton } from "@/components/domain/copy-button";
import { ModulePage } from "@/components/domain/module-page";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { GitHubAppsView } from "@/features/githubapps/github-apps-view";
import { useApps } from "@/lib/catalog";
import { formatBytes } from "@/lib/format";

// Registry v1（IA v3 T5，§5.1 一期；模板化收编 ModulePage+ListCard →
// useCollection 化批 3 收编 kit 块）：按 app 维度的"当前运行内容"视图
// ——最新部署的 revision digest（内容寻址）+ commit + 状态。Apps tab 的
// apps→deployments→revisions 三级合并并轨进 kit/registry 的单条集合查
// 询（deployment 轴 15s 轮询）。二期（⑤b）：镜像仓内容视图点亮——
// catalog/tags 经受管仓只读代理（按项目前纲；本批不动）。
export function RegistryView({
  projectId,
  routeId,
  tab,
  onTabChange,
}: {
  projectId: string;
  routeId: string;
  tab: "apps" | "catalog" | "github";
  onTabChange: (value: "apps" | "catalog" | "github") => void;
}) {
  // useApps 供 catalog tab 的仓名反查（apps tab 已并轨 kit 集合）。
  const apps = useApps(projectId);

  return (
    <ModulePage
      title="Registry"
      description="What each app runs right now — revision digests from the managed registry (zot)"
      tabs={[
        { value: "apps", label: "Apps" },
        { value: "catalog", label: "Image catalog" },
        { value: "github", label: "GitHub Apps" },
      ]}
      current={tab}
      onTabChange={onTabChange}
    >
      {tab === "github" ? (
        <GitHubAppsView projectId={projectId} from={routeId} />
      ) : tab === "catalog" ? (
        <RegistryCatalog projectId={projectId} apps={apps.data ?? []} />
      ) : (
        <RegistryAppsTab projectId={projectId} routeId={routeId} />
      )}
    </ModulePage>
  );
}

// RegistryAppsTab：三级合并列表的 kit 消费面（行 = RegistryRow，digest
// 已在 fetch 内解析——列不再持 per-App 反查索引）。
function RegistryAppsTab({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(registryResource, {
    from: routeId,
    search: registrySearch,
    scope: { projectId },
    // 并轨取 deployment 轴 15s（旧三级 60s/15s/30s 的最鲜轴；registry
    // digest 只随部署变化，apps/revisions 无需更高频）。
    query: { refetchInterval: 15_000 },
  });

  const columns: LegacyColumnDef<RegistryRow, any>[] = [
    {
      id: "app",
      header: "App",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id} label={row.original.name} />
          <div className="text-sm font-semibold">{row.original.name}</div>
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
      cell: ({ row }) =>
        row.original.digest ? (
          <span className="flex items-center gap-1 font-mono text-xs">
            {row.original.digest.slice(0, 19)}…
            <CopyButton value={row.original.digest} />
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
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
            params={{ projectId, appId: row.original.id }}
            className="text-xs text-info underline-offset-2 hover:underline"
          >
            deployments
          </Link>
        </div>
      ),
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <FleetListPage
      list={list}
      toolbar={<FilterBar list={list} schema={registrySearch} textPlaceholder="Filter apps..." />}
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No apps match the filter" : "No apps in this project"}
      emptyDescription={
        filtered
          ? "Adjust or clear the filter to see rows again."
          : "Registry rows appear per app once apps exist — each row resolves the running revision digest."
      }
      appendix={
        <p className="text-2xs text-muted-foreground">Digests are content-addressed — they identify exactly what is running.</p>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id} />
    </FleetListPage>
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
                className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-panel-2/40"
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
