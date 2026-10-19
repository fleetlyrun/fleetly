import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { ArrowUpRightIcon, LayersIcon } from "lucide-react";
import { apiFetch } from "@/api/client";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { RelativeTime } from "@/components/domain/relative-time";
import { RouteFallback } from "@/components/shell/route-fallback";
import { Skeleton } from "@/components/ui/skeleton";
import { useApps } from "@/lib/catalog";

// 项目总览页体（Dashboard 原型，UI v2 批 2）：App 磁贴墙 + 每磁贴最近部署
// 态。页面体住 features/（路由文件只剩薄壳——router 插件改写路由文件，
// 页体在 features 才可单测）。陈旧项目 id 回退（W-1，URL 侧收口）：侧栏
// 链接取自 store 记忆，记忆中的项目已删时整条 /p/ 链接都落在死页——目录
// 在册校验（切换器 5a44138 只校准 store，URL 不回头）确认查无即 redirect
// 首个项目（replace：死地址不留历史）；目录全空才是诚实 Not found。
// redirect 只在目录加载完成且确认查无后触发（pending 窗不弹跳——记录·
// 四十九"整页弹回"教训）。

interface ProjectEntry {
  id: string;
  name: string;
}

export function ProjectOverviewPage({ projectId }: { projectId: string }) {
  const navigate = useNavigate();
  const project = useQuery({
    queryKey: ["catalog", "project", projectId],
    queryFn: async (): Promise<{ projects: ProjectEntry[]; hit: ProjectEntry | null }> => {
      const res = await apiFetch<{ projects?: Array<{ id?: string; name?: string } | undefined> }>(
        `/v1/projects?limit=100`,
      );
      const projects = (res.projects ?? []).flatMap((entry) =>
        entry?.id && entry?.name ? [{ id: entry.id, name: entry.name }] : [],
      );
      return { projects, hit: projects.find((entry) => entry.id === projectId) ?? null };
    },
  });
  const apps = useApps(projectId);

  const miss = project.data?.hit == null && !project.isPending && !project.isError;
  useEffect(() => {
    const projects = project.data?.projects ?? [];
    if (miss && projects.length > 0) {
      // replace：陈旧地址不留历史（回退是导航语境修正，不是新页面）。
      void navigate({ to: "/p/$projectId", params: { projectId: projects[0].id }, replace: true });
    }
  }, [miss, project.data, navigate]);

  if (project.isError) {
    return (
      <div className="p-8">
        <ErrorState error={project.error} onRetry={() => void project.refetch()} />
      </div>
    );
  }
  if (miss) {
    const projects = project.data?.projects ?? [];
    if (projects.length === 0) {
      return (
        <div className="p-8">
          <ErrorState error={new Error("no projects yet — create one via Quickstart")} />
        </div>
      );
    }
    // redirect 在途帧：spinner 兜住，不渲染死页内容。
    return <RouteFallback />;
  }

  const name = project.data?.hit?.name ?? "…";

  return (
    <PageFrame
      title={
        <span className="flex items-center gap-3">
          <ProjectAvatar seed={projectId} label={name} className="size-8 rounded-xl text-sm" />
          {name}
        </span>
      }
      description="Project overview — apps and their latest deployments"
    >
      {apps.isPending ? (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
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
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
          {(apps.data ?? []).map((app) => (
            <AppTile key={app.id} projectId={projectId} appId={app.id} appName={app.name} />
          ))}
        </div>
      )}
    </PageFrame>
  );
}

function AppTile({ projectId, appId, appName }: { projectId: string; appId: string; appName: string }) {
  const base = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;
  // 每磁贴自持最近部署态（5s 轮询）。查询键必须独立于列表页的
  // ["deployments", appId]（Deployment[] 形状，H13）——同键双形状会让
  // App 详情页把这里缓存的单条 Deployment 当数组 slice 而首帧崩溃。
  const deployments = useQuery({
    queryKey: ["deployments", "latest", appId],
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
      className="group rounded-xl border bg-card p-5 transition-all hover:-translate-y-px hover:border-border-strong"
    >
      <div className="flex items-center gap-3">
        <ProjectAvatar seed={appId} label={appName} className="size-9 rounded-xl text-sm" />
        <div className="min-w-0">
          <div className="truncate text-sm font-bold">{appName}</div>
          <div className="font-mono text-2xs text-muted-foreground">{appId.slice(0, 10)}…</div>
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
              <RelativeTime value={latest.updated_at} className="text-2xs text-muted-foreground" />
            ) : null}
          </>
        ) : (
          <span className="inline-flex items-center gap-1 text-2xs text-muted-foreground">
            <LayersIcon className="size-3" />
            no deployments yet
          </span>
        )}
      </div>
    </a>
  );
}
