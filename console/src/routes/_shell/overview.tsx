import { Link, createFileRoute } from "@tanstack/react-router";
import { BellIcon, BoxesIcon, PlusIcon } from "lucide-react";
import { ErrorState } from "@/components/domain/error-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { useAlertStates, useApps, useNodes, useProjects } from "@/lib/catalog";
import { useProjectId } from "@/lib/project";

// 舰队总览（Dashboard 原型，UI v2 批 2）：平台状态条 + 项目磁贴墙 +
// 入场动作。最近事件流批 3 随 Events 域落地（此处不造假数据）。
export const Route = createFileRoute("/_shell/overview")({
  component: OverviewPage,
});

function OverviewPage() {
  const projects = useProjects();
  const nodes = useNodes();
  const alerts = useAlertStates();
  const firing = (alerts.data ?? []).filter((state) => state.state === "firing");
  const availableNodes = (nodes.data ?? []).filter((node) => node.available === true);
  const [, setProjectId] = useProjectId();

  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Overview"
        description="Fleet health at a glance"
        actions={
          <>
            <Button variant="outline" size="sm" asChild>
              <a href="/quickstart">
                <PlusIcon data-icon-start-inline />
                New project
              </a>
            </Button>

          </>
        }
      />

      <div className="mb-6 grid grid-cols-[repeat(auto-fit,minmax(180px,1fr))] gap-3">
        <StatCard label="Nodes" value={`${availableNodes.length}/${(nodes.data ?? []).length}`} sub="available" loading={nodes.isPending} />
        <StatCard
          label="Alerts"
          value={String(firing.length)}
          sub={firing.length > 0 ? "firing now" : "all quiet"}
          tone={firing.length > 0 ? "danger" : "neutral"}
          loading={alerts.isPending}
        />
        <StatCard label="Projects" value={String((projects.data ?? []).length)} sub="in this fleet" loading={projects.isPending} />
      </div>

      <h2 className="mb-2.5 px-0.5 text-[13.5px] font-semibold">Projects</h2>
      {projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : projects.isPending ? (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-3.5">
          {Array.from({ length: 3 }).map((_, index) => (
            <Skeleton key={index} className="h-28 rounded-xl" />
          ))}
        </div>
      ) : (projects.data ?? []).length === 0 ? (
        <div className="rounded-xl border border-dashed px-6 py-12 text-center">
          <p className="text-sm font-semibold">No projects yet</p>
          <p className="mx-auto mt-1 max-w-sm text-xs text-muted-foreground">
            Run Quickstart to create your first project, app and route in one guided pass.
          </p>
          <Button size="sm" className="mt-4" asChild>
            <a href="/quickstart">Open Quickstart</a>
          </Button>
        </div>
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-3.5">
          {(projects.data ?? []).map((project) => (
            <ProjectTile key={project.id} projectId={project.id} projectName={project.name} onVisit={() => setProjectId(project.id)} />
          ))}
        </div>
      )}
    </div>
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
    <div className="rounded-xl border bg-card px-4 py-3.5 shadow-[0_1px_2px_oklch(0_0_0/0.06)]">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        {tone === "danger" ? <BellIcon className="size-3" /> : <BoxesIcon className="size-3" />}
        {label}
      </div>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-16" />
      ) : (
        <div className={`mt-0.5 text-[21px] font-bold tracking-tight ${tone === "danger" && value !== "0" ? "text-[var(--status-danger)]" : ""}`}>
          {value}
        </div>
      )}
      <div className="text-[11.5px] text-muted-foreground">{sub}</div>
    </div>
  );
}

function ProjectTile({
  projectId,
  projectName,
  onVisit,
}: {
  projectId: string;
  projectName: string;
  onVisit: () => void;
}) {
  const apps = useApps(projectId);
  const rows = apps.data ?? [];

  return (
    <Link
      to="/p/$projectId"
      params={{ projectId }}
      onClick={onVisit}
      className="group rounded-xl border bg-card p-4 shadow-[0_1px_2px_oklch(0_0_0/0.06)] transition-all hover:-translate-y-px hover:border-border-strong hover:shadow-lg"
    >
      <div className="flex items-center gap-3">
        <ProjectAvatar seed={projectId} label={projectName} className="size-9 rounded-xl text-sm" />
        <div className="min-w-0">
          <div className="truncate text-sm font-bold">{projectName}</div>
          <div className="text-[11.5px] text-muted-foreground">
            {apps.isPending ? "…" : `${rows.length} app${rows.length === 1 ? "" : "s"}`}
          </div>
        </div>
      </div>
      {apps.isError ? (
        <div className="mt-3 text-[11px] text-[var(--status-danger)]">apps failed to load</div>
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
          <span className="ml-1 text-[11px] text-muted-foreground group-hover:text-foreground">open →</span>
        </div>
      )}
    </Link>
  );
}
