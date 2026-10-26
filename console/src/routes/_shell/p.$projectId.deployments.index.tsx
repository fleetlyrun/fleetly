import { useState } from "react";
import { useNavigate, createFileRoute } from "@tanstack/react-router";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection } from "@lynx-go/uikit-core";
import { RocketIcon } from "lucide-react";
import { DeploySheet } from "@/features/deployments/deploy-sheet";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { DeploymentStatusBadge } from "@/components/domain/status-badge";
import { PageFrame } from "@/components/domain/page-frame";
import { Button } from "@/components/ui/button";
import { useApps } from "@/lib/catalog";
import { deploymentsResource, deploymentsSearch, type DeploymentRow } from "@/kit/deployments";

// 项目级部署流（List 原型，UI v2 批 2；2026-10-26 批1 useCollection 化）：
// 跨 App 聚合走客户端 fan-out（有界 50 App、15s 档——服务端 per-App 轴
// 契约不动，聚合是 Console 侧的诚实替代，ADR-0057 不变量）。快筛/分页/
// 排序首次进 URL（deploymentsSearch）；App 名解析与 DeploySheet 继续吃
// useApps 的 catalog 缓存（60s 目录档）。
export const Route = createFileRoute("/_shell/p/$projectId/deployments/")({
  validateSearch: deploymentsSearch.toSearchSchema(),
  component: ProjectDeploymentsPage,
});

function ProjectDeploymentsPage() {
  const { projectId } = Route.useParams();
  const navigate = useNavigate();
  const list = useCollection(deploymentsResource, {
    from: Route.id,
    search: deploymentsSearch,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const apps = useApps(projectId);
  const [deployOpen, setDeployOpen] = useState(false);

  const columns: LegacyColumnDef<DeploymentRow, any>[] = [
    {
      accessorKey: "state",
      header: "State",
      meta: { sortField: "state" },
      cell: ({ row }) => <DeploymentStatusBadge state={row.original.state} />,
    },
    {
      id: "app",
      header: "App",
      cell: ({ row }) => {
        const app = (apps.data ?? []).find((entry) => entry.id === row.original.app_id);
        return (
          <div className="flex items-center gap-2">
            <ProjectAvatar seed={row.original.app_id} label={app?.name ?? "?"} className="size-5 rounded-md text-2xs" />
            <span className="text-sm font-medium">{app?.name ?? row.original.app_id?.slice(0, 10) + "…"}</span>
          </div>
        );
      },
    },
    {
      id: "revision",
      header: "Revision",
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.from_revision ? `R…${row.original.from_revision.slice(-6)} → ` : ""}
          R…{row.original.to_revision?.slice(-6) ?? "—"}
        </span>
      ),
    },
    {
      id: "generation",
      header: "Generation",
      cell: ({ row }) => {
        const hasBaseline = row.original.from_generation != null && row.original.from_generation !== "0";
        return (
          <span className="font-mono text-xs text-muted-foreground">
            {hasBaseline ? `g${row.original.from_generation} → ` : ""}
            g{row.original.generation ?? "?"}
          </span>
        );
      },
    },
    {
      id: "updated",
      header: "Updated",
      meta: { sortField: "updated_at" },
      cell: ({ row }) => <RelativeTime value={row.original.updated_at} className="text-xs text-muted-foreground" />,
    },
    {
      id: "error",
      header: "Error",
      cell: ({ row }) =>
        row.original.error ? (
          <div className="max-w-56 truncate font-mono text-2xs text-[var(--status-danger)]" title={row.original.error}>
            {row.original.error}
          </div>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
  ];

  // 源空与过滤空两套文案：谓词在场 = 过滤空。
  const filtered = list.query.predicate != null;

  return (
    <PageFrame title="Deployments" description="All apps in this project — client-side fan-out, refreshed every 15s">
      <FleetListPage
        list={list}
        toolbar={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <FilterBar list={list} schema={deploymentsSearch} textPlaceholder="Filter deployments..." />
            {/* 列表主动作钉工具栏行最右（对齐批 5 裁决） */}
            <div className="py-2 pr-3">
              <Button size="sm" onClick={() => setDeployOpen(true)}>
                <RocketIcon data-icon-start-inline />
                Deploy…
              </Button>
            </div>
          </div>
        }
        pagination={<Pagination list={list} />}
        emptyTitle={filtered ? "No deployments match the filter" : "No deployments yet"}
        emptyDescription={
          filtered
            ? "Adjust or clear the filter to see rows again."
            : "Deploy from an app's page, or pick an app above and hit Deploy."
        }
        appendix={
          <DeploySheet
            open={deployOpen}
            onOpenChange={setDeployOpen}
            apps={apps.data ?? []}
            defaultAppId={(apps.data ?? [])[0]?.id ?? ""}
            projectId={projectId}
            onDeployed={(deploymentId) => {
              const appId = (apps.data ?? [])[0]?.id ?? "";
              if (appId === "") return;
              void navigate({
                to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
                params: { projectId, appId, deploymentId },
              });
            }}
          />
        }
      >
        <DataTable
          list={list}
          columns={columns}
          getRowId={(row) => row.id ?? ""}
          onRowClick={(deployment) => {
            const appId = deployment.app_id ?? "";
            if (appId === "") return;
            void navigate({
              to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
              params: { projectId, appId, deploymentId: deployment.id ?? "" },
            });
          }}
        />
      </FleetListPage>
    </PageFrame>
  );
}
