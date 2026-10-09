import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ArrowUpRightIcon, LayersIcon } from "lucide-react";
import { apiFetch } from "@/api/client";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { RelativeTime } from "@/components/domain/relative-time";
import { Skeleton } from "@/components/ui/skeleton";
import { useApps } from "@/lib/catalog";

// 项目总览（Dashboard 原型，UI v2 批 2）：App 磁贴墙 + 每磁贴最近部署态。
// 项目不存在（目录查无）给 Not found 诚实态。
export const Route = createFileRoute("/_shell/p/$projectId/")({
  component: ProjectOverviewPage,
});

interface ProjectEntry {
  id: string;
  name: string;
}

function ProjectOverviewPage() {
  const { projectId } = Route.useParams();
  const project = useQuery({
    queryKey: ["catalog", "project", projectId],
    queryFn: async (): Promise<ProjectEntry | null> => {
      const res = await apiFetch<{ projects?: Array<{ id?: string; name?: string } | undefined> }>(
        `/v1/projects?limit=100`,
      );
      const hit = (res.projects ?? []).find((entry) => entry?.id === projectId);
      return hit?.id && hit?.name ? { id: hit.id, name: hit.name } : null;
    },
  });
  const apps = useApps(projectId);

  if (project.isError) {
    return (
      <div className="p-8">
        <ErrorState error={project.error} onRetry={() => void project.refetch()} />
      </div>
    );
  }
  if (project.data === null) {
    return (
      <div className="p-8">
        <ErrorState error={new Error("project not found in catalog")} />
      </div>
    );
  }

  const name = project.data?.name ?? "…";

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <PageHeader
        title={
          <span className="flex items-center gap-3">
            <ProjectAvatar seed={projectId} label={name} className="size-8 rounded-xl text-sm" />
            {name}
          </span>
        }
        description="Project overview — apps and their latest deployments"
      />
      {apps.isPending ? (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-3.5">
          {Array.from({ length: 3 }).map((_, index) => (
            <Skeleton key={index} className="h-28 rounded-xl" />
          ))}
        </div>
      ) : apps.isError ? (
        <ErrorState error={apps.error} onRetry={() => void apps.refetch()} />
      ) : (apps.data ?? []).length === 0 ? (
        <div className="rounded-xl border border-dashed px-6 py-10 text-center">
          <p className="text-sm font-semibold">No apps yet</p>
          <p className="mt-1 text-xs text-muted-foreground">
            Create one via Quickstart, or deploy an image straight from the Deployments page.
          </p>
        </div>
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-3.5">
          {(apps.data ?? []).map((app) => (
            <AppTile key={app.id} projectId={projectId} appId={app.id} appName={app.name} />
          ))}
        </div>
      )}
    </div>
  );
}

function AppTile({ projectId, appId, appName }: { projectId: string; appId: string; appName: string }) {
  const base = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;
  // 每磁贴自持最近部署态（5s 轮询与列表页同键，共享缓存不加重）
  const deployments = useQuery({
    queryKey: ["deployments", appId],
    queryFn: async () => {
      const res = await apiFetch<{ deployments?: Array<{ id?: string; state?: string; updated_at?: string }> }>(
        `/v1/deployments?app_id=${encodeURIComponent(appId)}&limit=1`,
      );
      return res.deployments?.[0] ?? null;
    },
    refetchInterval: 5_000,
  });
  const latest = deployments.data;

  return (
    <a
      href={base}
      className="group rounded-xl border bg-card p-4 shadow-[0_1px_2px_oklch(0_0_0/0.06)] transition-all hover:-translate-y-px hover:border-border-strong hover:shadow-lg"
    >
      <div className="flex items-center gap-3">
        <ProjectAvatar seed={appId} label={appName} className="size-9 rounded-xl text-sm" />
        <div className="min-w-0">
          <div className="truncate text-sm font-bold">{appName}</div>
          <div className="font-mono text-[11px] text-muted-foreground">{appId.slice(0, 10)}…</div>
        </div>
        <ArrowUpRightIcon className="ml-auto size-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100" />
      </div>
      <div className="mt-3 flex items-center gap-2">
        {deployments.isPending ? (
          <Skeleton className="h-5 w-20" />
        ) : latest?.state ? (
          <>
            <DeploymentStatusBadge state={latest.state} />
            {latest.updated_at ? (
              <RelativeTime value={latest.updated_at} className="text-[11px] text-muted-foreground" />
            ) : null}
          </>
        ) : (
          <span className="inline-flex items-center gap-1 text-[11.5px] text-muted-foreground">
            <LayersIcon className="size-3" />
            no deployments yet
          </span>
        )}
      </div>
    </a>
  );
}
