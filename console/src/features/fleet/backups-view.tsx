import { HardDriveDownloadIcon, LinkIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection } from "@lynx-go/uikit-core";
import { backupHealth } from "@/features/databases/backup-health";
import { backupsSearch, databaseBackupsResource, platformSnapshotsResource, type DatabaseBackupRow, type PlatformSnapshotRow } from "@/kit/backups";
import { CliEquivalent } from "@/components/domain/list-toolbar";
import { PageTabs } from "@/components/domain/page-tabs";
import { PageFrame } from "@/components/domain/page-frame";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { statusToneClass } from "@/components/domain/status-badge";
import { DataTable } from "@/components/kit/query/data-table";
import { FilterBar } from "@/components/kit/query/filter-bar";
import { ListPage as FleetListPage } from "@/components/kit/query/list-page";
import { Pagination } from "@/components/kit/query/pagination";

// Backups 聚合页（IA v3 T7，§5.3 → 2026-10-26 批1 Wave2-A useCollection
// 化）：数据安全一屏回答"昨晚备份成没成"——每库一行（v1Database
// .last_backup_at 是最近成功锚，ADR-0039）+ 平台快照（restic）区块。
// 跨项目聚合不变量保持客户端 fan-out（ADR-0057），取数并轨 kit/backups
// 双资源（Databases 60s 轮询沿用旧拍）；快照小表收编 kit 四件套——页内
// 手挂分页的反模式豁免随迁移摘除（分页/快筛进 URL）。页头动态计数
//（N databases · M healthy）随列表句柄化退场（句柄只暴露当前页切片），
// 页头描述收敛静态文案。
export function BackupsView({
  tab,
  onTabChange,
  from,
}: {
  tab: "databases" | "snapshots";
  onTabChange: (value: "databases" | "snapshots") => void;
  from: string;
}) {
  return (
    <PageFrame
      title="Backups"
      description="Data safety at a glance — database backups and platform snapshots"
      actions={<ButtonLink href="/settings" label="Platform backup settings" />}
    >
      <PageTabs
        tabs={[
          { value: "databases", label: "Databases" },
          { value: "snapshots", label: "Snapshots" },
        ]}
        current={tab}
        onChange={onTabChange}
      />

      {tab === "databases" ? <DatabasesTab from={from} /> : <SnapshotsTab from={from} />}
    </PageFrame>
  );
}

// DatabasesTab：跨项目备份健康台账（kit fan-out 资源；staleness 业务排序
// 在 fetch 内，URL 无 sort 时保持视线序）。
function DatabasesTab({ from }: { from: string }) {
  const list = useCollection(databaseBackupsResource, {
    from,
    search: backupsSearch,
    query: { refetchInterval: 60_000 },
  });

  const columns: LegacyColumnDef<DatabaseBackupRow, any>[] = [
    {
      accessorKey: "name",
      header: "Database",
      meta: { sortField: "name" },
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.id} label={row.original.name} />
          <div className="text-sm font-semibold">{row.original.name}</div>
        </div>
      ),
    },
    {
      accessorKey: "project_name",
      header: "Project",
      meta: { sortField: "project_name" },
      cell: ({ row }) => (
        <span className="flex items-center gap-1 text-xs text-muted">
          <LinkIcon className="size-3" />
          {row.original.project_name}
        </span>
      ),
    },
    {
      accessorKey: "engine",
      header: "Engine",
      meta: { sortField: "engine" },
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.engine}</span>,
    },
    {
      id: "health",
      header: "Backup health",
      cell: ({ row }) => {
        const health = backupHealth(row.original.database.last_backup_at);
        return (
          <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${statusToneClass(health.tone)}`}>
            <span className="inline-block size-1.5 rounded-full bg-current" />
            {health.label}
          </span>
        );
      },
    },
    {
      id: "restore",
      header: "Restore state",
      cell: ({ row }) =>
        row.original.database.restore_from_backup ? (
          <span className="text-info">
            restoring from {row.original.database.restore_from_backup}
            {row.original.database.restore_error ? <div className="text-2xs text-err">{row.original.database.restore_error}</div> : null}
          </span>
        ) : (
          <span className="text-muted">—</span>
        ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="text-right">
          <a
            className="text-xs text-info underline-offset-2 hover:underline"
            href={`/p/${encodeURIComponent(row.original.project_id)}/databases/${encodeURIComponent(row.original.id)}`}
          >
            open
          </a>
        </div>
      ),
    },
  ];

  const filtered = list.query.predicate != null;
  return (
    <FleetListPage
      list={list}
      toolbar={<FilterBar list={list} schema={backupsSearch} textPlaceholder="Filter databases..." />}
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No databases match the filter" : "No databases"}
      emptyDescription={
        filtered
          ? "Adjust or clear the filter to see rows again."
          : "Database backup health rows appear here once databases exist."
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id} />
    </FleetListPage>
  );
}

// SnapshotsTab：平台快照区块（restic）——只读三列小表；快照行数 = 备份
// 窗口频次的台账（kit 四件套收编，60s 轮询沿用旧拍）。
function SnapshotsTab({ from }: { from: string }) {
  const list = useCollection(platformSnapshotsResource, {
    from,
    search: backupsSearch,
    query: { refetchInterval: 60_000 },
  });

  const columns: LegacyColumnDef<PlatformSnapshotRow, any>[] = [
    {
      accessorKey: "id",
      header: "Snapshot",
      meta: { sortField: "id" },
      cell: ({ row }) => <span className="font-mono text-xs">{row.original.id}</span>,
    },
    {
      accessorKey: "hostname",
      header: "Host",
      meta: { sortField: "hostname" },
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.hostname}</span>,
    },
    {
      accessorKey: "time",
      header: "Taken",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          <RelativeTime value={row.original.time} />
        </span>
      ),
    },
  ];

  const filtered = list.query.predicate != null;
  return (
    <FleetListPage
      list={list}
      toolbar={<FilterBar list={list} schema={backupsSearch} textPlaceholder="Filter snapshots..." />}
      pagination={<Pagination list={list} />}
      emptyTitle={filtered ? "No snapshots match the filter" : "No snapshots listed yet"}
      emptyDescription={
        filtered
          ? "Adjust or clear the filter to see rows again."
          : "The platform backup window writes here (local store + optional S3 mirror)."
      }
      appendix={
        <>
          <CliEquivalent command="fleetly platform backups" />
          <p className="mt-3 flex items-center gap-1.5 text-2xs text-muted">
            <HardDriveDownloadIcon className="size-3.5" />
            Snapshot cadence and retention follow the platform backup config; the local store is same-node — not disaster
            recovery.
          </p>
        </>
      }
    >
      <DataTable list={list} columns={columns} getRowId={(row) => row.id ?? ""} />
    </FleetListPage>
  );
}

function ButtonLink({ href, label }: { href: string; label: string }) {
  return (
    <a
      href={href}
      className="inline-flex h-8 items-center justify-center gap-2 rounded-md border bg-panel px-3 text-xs font-medium hover:bg-panel-2"
    >
      {label}
    </a>
  );
}
