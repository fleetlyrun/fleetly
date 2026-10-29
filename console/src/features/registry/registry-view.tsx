import { Link } from "@tanstack/react-router";
import { ChevronDownIcon, ChevronRightIcon } from "lucide-react";
import { useState } from "react";
import { useCollection, useCollectionData } from "@lynx-go/uikit-core";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { describeApiError } from "@/kit/errors";
import { registryCatalogResource, registryResource, registrySearch, registryTagsResource, type RegistryRow } from "@/kit/registry";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { CopyButton } from "@/components/domain/copy-button";
import { ModulePage } from "@/components/domain/module-page";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { GitHubAppsView } from "@/features/githubapps/github-apps-view";
import { appsResource } from "@/kit/apps";
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
  // apps 目录伴读供 catalog tab 的仓名反查（apps tab 已并轨 kit 集合）。
  const apps = useCollectionData(appsResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 60_000,
    enabled: projectId !== "",
  });

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
        <RegistryCatalog projectId={projectId} apps={apps.rows} />
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
            <span className="font-mono text-xs text-muted">{row.original.deployment.id?.slice(0, 10)}…</span>
          </div>
        ) : (
          <span className="text-xs text-muted">never deployed</span>
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
          <span className="text-xs text-muted">—</span>
        ),
    },
    {
      id: "commit",
      header: "Commit",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.deployment?.commit_sha || "—"}</span>,
    },
    {
      id: "deployed",
      header: "Deployed",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
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
    <ListPage
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
        <p className="text-2xs text-muted">Digests are content-addressed — they identify exactly what is running.</p>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id} />
    </ListPage>
  );
}

// RegistryCatalog 镜像仓内容视图（IA v3 二期⑤b）：项目前纲下的仓名清单
//（受管仓只读代理），逐仓名展开 tag 事实（digest/压缩大小/推送时刻）。
// 上游缺席诚实呈现（代理面未配置 = 明确的不可用文案，不伪装空清单）。
function RegistryCatalog({ projectId, apps }: { projectId: string; apps: ReadonlyArray<{ id: string; name: string }> }) {
  const [openRepo, setOpenRepo] = useState<string | null>(null);
  // catalog 台账伴读（M1 useCollectionData）：60s retry:false 同旧。
  const catalog = useCollectionData(registryCatalogResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 60_000,
    enabled: projectId !== "",
    retry: false,
  });
  const appName = (repo: string) => apps.find((app) => app.id.toLowerCase() === repo.split("/")[1])?.name ?? repo.split("/")[1];
  return (
    <>
      {catalog.status === "loading" ? (
        <p className="text-xs text-muted">Loading repositories…</p>
      ) : catalog.status === "error" ? (
        <p className="rounded-xl border bg-panel p-4 text-xs text-muted">
          Registry content is unavailable on this platform ({catalog.error ? describeApiError(catalog.error) : "unknown error"}).
        </p>
      ) : catalog.rows.length === 0 ? (
        <p className="rounded-xl border bg-panel p-4 text-xs text-muted">
          No images pushed under this project yet — repositories appear after the first build.
        </p>
      ) : (
        <div className="overflow-hidden rounded-xl border">
          {catalog.rows.map((repo) => (
            <div key={repo} className="border-b last:border-b-0">
              <button
                type="button"
                className="flex w-full items-center gap-2 px-3 py-2 text-left hover:bg-panel-2/40"
                onClick={() => setOpenRepo(openRepo === repo ? null : repo)}
              >
                {openRepo === repo ? (
                  <ChevronDownIcon className="size-4 text-muted" />
                ) : (
                  <ChevronRightIcon className="size-4 text-muted" />
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
  // tags 子表伴读（M1 useCollectionData）：retry:false 同旧。
  const tags = useCollectionData(registryTagsResource, {
    scope: { projectId, repo },
    window: { size: 200 },
    enabled: repo !== "",
    retry: false,
  });
  if (tags.status === "loading") {
    return <p className="px-3 pb-2 pl-9 text-xs text-muted">Loading tags…</p>;
  }
  if (tags.status === "error") {
    return <p className="px-3 pb-2 pl-9 text-xs text-err">{tags.error ? describeApiError(tags.error) : "unknown error"}</p>;
  }
  return (
    <table className="mb-1 ml-6 w-[calc(100%-1.5rem)] text-xs">
      <thead className="text-left text-2xs font-semibold tracking-wide text-muted uppercase">
        <tr>
          <th className="px-2 py-1">Tag</th>
          <th className="px-2 py-1">Digest</th>
          <th className="px-2 py-1">Size</th>
          <th className="px-2 py-1">Pushed</th>
        </tr>
      </thead>
      <tbody>
        {tags.rows.map((tag) => (
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
