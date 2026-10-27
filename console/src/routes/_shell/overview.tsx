import { Link, createFileRoute } from "@tanstack/react-router";
import { useCollection, useRecord } from "@lynx-go/uikit-core";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { BellIcon, BoxesIcon, PlusIcon } from "lucide-react";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { appsDirectorySearch, appsResource } from "@/kit/apps";
import { nodesDirectorySearch, nodesResource } from "@/kit/nodes";
import { projectsResource } from "@/kit/projects";
import { sessionResource } from "@/kit/settings";
// 残留（批4 收尾注记）：alert states 是 join 数据源非列表，kit/alerts 头注
// 钉它保持 useQuery——归 alerts 域（R1）收编，本页随迁。
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { alertStatesResource, alertStatesLookup } from "@/kit/alerts";
import { useProjectId } from "@/lib/project";

// 舰队总览（Dashboard 原型，UI v2 批 2）：平台状态条 + 项目磁贴墙 +
// 入场动作。最近事件流批 3 随 Events 域落地（此处不造假数据）。
// 2026-10-27 批4 读面收尾：目录/计数全走 kit 资源（useCollection from=Route.id
// 绑定本路由；非列表语义统一目录级大页 schema 直接吃 rows），轮询照
// lib/catalog 原值（目录 60s / 资源 15s）。

// 目录级大页（纯目录拉取：窗口 ≥ 取数封顶，目录不截断）。
const projectsDirectorySearch = collectionQuerySchema({ defaultSize: 200 });

const CATALOG_MS = 60_000;
const RESOURCE_MS = 15_000;

export const Route = createFileRoute("/_shell/overview")({
  component: OverviewPage,
});

function OverviewPage() {
  const projects = useCollection(projectsResource, { from: Route.id, search: projectsDirectorySearch, query: { refetchInterval: CATALOG_MS } });
  // 节点读面是平台管理员门（R08-M1/S16-c7）：非平台身份不请求 /v1/nodes，
  // 节点卡随 whoami.platformAdmin 显隐（fail-closed）。会话读经
  // sessionResource record（id 恒 "self"——壳层各持有点共享同一缓存条目）。
  const session = useRecord(sessionResource, { id: "self" });
  const platformAdmin = session.data?.platformAdmin === true;
  const nodes = useCollection(nodesResource, { from: Route.id, search: nodesDirectorySearch, query: { refetchInterval: RESOURCE_MS, enabled: platformAdmin } });
  const alerts = useQuery({
    ...collectionQueryOptions(alertStatesResource, alertStatesLookup.defaults, { refetchInterval: 15_000 }),
  });
  const firing = (alerts.data?.rows ?? []).filter((state) => state.state === "firing");
  const availableNodes = nodes.rows.filter((node) => node.available === true);
  const [, setProjectId] = useProjectId();

  return (
    <PageFrame
      title="Overview"
      description="Fleet health at a glance"
      actions={
        <Button variant="outline" size="sm" asChild>
          <a href="/quickstart">
            <PlusIcon data-icon-start-inline />
            New project
          </a>
        </Button>
      }
    >

      <div className="mb-6 grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-3.5">
        {platformAdmin ? (
          <StatCard label="Nodes" value={`${availableNodes.length}/${nodes.rows.length}`} sub="available" loading={nodes.status === "loading"} />
        ) : null}
        <StatCard
          label="Alerts"
          value={String(firing.length)}
          sub={firing.length > 0 ? "firing now" : "all quiet"}
          tone={firing.length > 0 ? "danger" : "neutral"}
          loading={alerts.isPending}
        />
        <StatCard label="Projects" value={String(projects.rows.length)} sub="in this fleet" loading={projects.status === "loading"} />
      </div>

      <h2 className="mb-2.5 px-0.5 text-sm font-semibold">Projects</h2>
      {projects.status === "error" ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.status === "loading" ? (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
          {Array.from({ length: 3 }).map((_, index) => (
            <Skeleton key={index} className="h-28 rounded-xl" />
          ))}
        </div>
      ) : projects.rows.length === 0 ? (
        <div className="rounded-xl border border-dashed px-6 py-12 text-center">
          <p className="text-sm font-semibold">No projects yet</p>
          <p className="mx-auto mt-1 max-w-sm text-xs text-muted">
            Run Quickstart to create your first project, app and route in one guided pass.
          </p>
          <Button size="sm" className="mt-4" asChild>
            <a href="/quickstart">Open Quickstart</a>
          </Button>
        </div>
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
          {projects.rows.map((project) => (
            <ProjectTile key={project.id} from={Route.id} projectId={project.id} projectName={project.name} onVisit={() => setProjectId(project.id)} />
          ))}
        </div>
      )}
    </PageFrame>
  );
}

function StatCard({
  label,
  value,
  sub,
  tone = "neutral",
  loading,
}: {
  label: string;
  value: string;
  sub: string;
  tone?: "neutral" | "danger";
  loading?: boolean;
}) {
  return (
    <div className="rounded-xl border bg-panel px-5 py-4">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted">
        {tone === "danger" ? <BellIcon className="size-3" /> : <BoxesIcon className="size-3" />}
        {label}
      </div>
      {loading ? (
        <Skeleton className="mt-1.5 h-7 w-16" />
      ) : (
        <div className={`mt-1.5 text-2xl font-semibold tracking-tight ${tone === "danger" && value !== "0" ? "text-[var(--status-danger)]" : ""}`}>
          {value}
        </div>
      )}
      <div className="mt-0.5 text-2xs text-muted">{sub}</div>
    </div>
  );
}

function ProjectTile({
  from,
  projectId,
  projectName,
  onVisit,
}: {
  from: string;
  projectId: string;
  projectName: string;
  onVisit: () => void;
}) {
  const apps = useCollection(appsResource, { from, search: appsDirectorySearch, scope: { projectId }, query: { refetchInterval: CATALOG_MS } });
  const rows = apps.rows;

  return (
    <Link
      to="/p/$projectId"
      params={{ projectId }}
      onClick={onVisit}
      className="group rounded-xl border bg-panel p-5 transition-all hover:-translate-y-px hover:border-line-strong"
    >
      <div className="flex items-center gap-3">
        <ProjectAvatar seed={projectId} label={projectName} className="size-9 rounded-xl text-sm" />
        <div className="min-w-0">
          <div className="truncate text-sm font-bold">{projectName}</div>
          <div className="text-2xs text-muted">
            {apps.status === "loading" ? "…" : `${rows.length} app${rows.length === 1 ? "" : "s"}`}
          </div>
        </div>
      </div>
      {apps.status === "error" ? (
        <div className="mt-3 text-2xs text-[var(--status-danger)]">apps failed to load</div>
      ) : (
        <div className="mt-3 flex items-center gap-1.5">
          {rows.slice(0, 6).map((app) => (
            <span
              key={app.id}
              className="inline-block size-2 rounded-full bg-border"
              title={app.name}
              data-app={app.id}
            />
          ))}
          <span className="ml-1 text-2xs text-muted group-hover:text-fg">open →</span>
        </div>
      )}
    </Link>
  );
}
