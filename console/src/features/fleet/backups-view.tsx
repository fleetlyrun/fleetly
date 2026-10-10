import { useQueries } from "@tanstack/react-query";
import { DatabaseIcon, HardDriveDownloadIcon, LinkIcon } from "lucide-react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { apiFetch } from "@/api/client";
import type { components } from "@/api/structure";
import { backupHealth } from "@/features/databases/backup-health";
import { CliEquivalent, ListPagination, useClientPage } from "@/components/domain/list-toolbar";
import { PageTabs } from "@/components/domain/page-tabs";
import { EmptyState } from "@/components/domain/empty-state";
import { ListCard } from "@/components/domain/list-page";
import { PageFrame } from "@/components/domain/page-frame";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { statusToneClass } from "@/components/domain/status-badge";
import { usePlatformBackups, useProjects } from "@/lib/catalog";

type DatabaseEntry = components["schemas"]["v1Database"];

type DatabaseRow = { project: { id?: string; name?: string }; database: DatabaseEntry };

// Backups 聚合页（IA v3 T7，§5.3；模板化收编 PageFrame+PageTabs+ListCard）：
// 数据安全一屏回答"昨晚备份成没成"——每库一行（v1Database.last_backup_at
// 是最近成功锚，ADR-0039，无需深度 fan-out）+ 平台快照（restic）区块。
// 跨项目聚合走客户端（ADR-0057 不变量）；聚合 API 二期优化（§8）。
export function BackupsView({ tab, onTabChange }: { tab: "databases" | "snapshots"; onTabChange: (value: "databases" | "snapshots") => void }) {
  const projects = useProjects();
  const projectList = projects.data ?? [];
  const databaseLists = useQueries({
    queries: projectList.slice(0, 50).map((project) => ({
      queryKey: ["resources", "databases", project.id],
      queryFn: async (): Promise<DatabaseEntry[]> => {
        const res = await apiFetch<{ databases?: Array<DatabaseEntry | undefined> }>(`/v1/databases?project_id=${encodeURIComponent(project.id)}`);
        return (res.databases ?? []).flatMap((entry) => (entry != null ? [entry] : []));
      },
      enabled: projectList.length > 0,
      refetchInterval: 60_000,
    })),
  });
  const snapshots = usePlatformBackups();

  // 行序（"昨晚成没成"的视线序）：过期库先于健康库，组内按最近成功备份
  // 升序（最久未备份最前）；ListCard 的过滤保持入参相对序，过滤后仍成立。
  const aggregated = projectList
    .slice(0, 50)
    .flatMap((project, index) =>
      (databaseLists[index]?.data ?? []).map((database) => ({ project, database })),
    );
  const byStaleness = (row: DatabaseRow) => (backupHealth(row.database.last_backup_at).tone !== "success" ? 0 : 1);
  const rows = [...aggregated].sort((a, b) => {
    const stale = byStaleness(a) - byStaleness(b);
    if (stale !== 0) return stale;
    return (a.database.last_backup_at ?? "").localeCompare(b.database.last_backup_at ?? "");
  });
  const healthyCount = rows.filter((row) => backupHealth(row.database.last_backup_at).tone === "success").length;
  const loading = projects.isPending || databaseLists.some((list) => list.isPending);

  return (
    <PageFrame
      title="Backups"
      description={
        rows.length > 0
          ? `Data safety at a glance — ${rows.length} database${rows.length === 1 ? "" : "s"} · ${healthyCount} healthy`
          : "Data safety at a glance — database backups and platform snapshots"
      }
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

      {tab === "databases" ? <DatabasesTab rows={rows} loading={loading} /> : <SnapshotsTab snapshots={snapshots.data ?? []} />}
    </PageFrame>
  );
}

function DatabasesTab({ rows, loading }: { rows: DatabaseRow[]; loading: boolean }) {
  const columns: LegacyColumnDef<DatabaseRow, any>[] = [
    {
      id: "database",
      header: "Database",
      cell: ({ row }) => (
        <div className="flex items-center gap-2.5">
          <ProjectAvatar seed={row.original.database.id ?? ""} label={row.original.database.name ?? ""} />
          <div className="text-[13px] font-semibold">{row.original.database.name}</div>
        </div>
      ),
    },
    {
      id: "project",
      header: "Project",
      cell: ({ row }) => (
        <span className="flex items-center gap-1 text-xs text-muted-foreground">
          <LinkIcon className="size-3" />
          {row.original.project.name}
        </span>
      ),
    },
    {
      accessorKey: "engine",
      header: "Engine",
      cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{row.original.database.engine}</span>,
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
            {row.original.database.restore_error ? <div className="text-[11px] text-destructive">{row.original.database.restore_error}</div> : null}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => (
        <div className="text-right">
          <a
            className="text-xs text-info underline-offset-2 hover:underline"
            href={`/p/${encodeURIComponent(row.original.project.id ?? "")}/databases/${encodeURIComponent(row.original.database.id ?? "")}`}
          >
            open
          </a>
        </div>
      ),
    },
  ];

  return (
    <section className="mb-8">
      <ListCard
        label="databases"
        placeholder="Filter databases..."
        rows={rows}
        fieldsOf={(row) => [row.database.name ?? "", row.database.engine ?? "", row.project.name ?? ""]}
        loading={loading}
        columns={columns}
        empty={
          <EmptyState
            icon={DatabaseIcon}
            title="No databases"
            description="Database backup health rows appear here once databases exist."
          />
        }
      />
    </section>
  );
}

// SnapshotsTab 是平台快照区块（restic）——只读三列小表；快照行数 = 备份
// 窗口频次的台账，分页保留（客户端切片）。
function SnapshotsTab({ snapshots }: { snapshots: Array<{ id: string; hostname: string; time: string }> }) {
  const { page, pageCount, pageRows, setPage } = useClientPage(snapshots);
  return (
    <section>
      <div className="rounded-xl border bg-card">
        {snapshots.length === 0 ? (
          <p className="p-6 text-center text-xs text-muted-foreground">
            No snapshots listed yet — the platform backup window writes here (local store + optional S3 mirror).
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-3 py-2">Snapshot</th>
                <th className="px-3 py-2">Host</th>
                <th className="px-3 py-2">Taken</th>
              </tr>
            </thead>
            <tbody>
              {pageRows.map((snapshot) => (
                <tr key={snapshot.id} className="border-b last:border-b-0">
                  <td className="px-3 py-2 font-mono text-xs">{snapshot.id}</td>
                  <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{snapshot.hostname}</td>
                  <td className="px-3 py-2 text-xs text-muted-foreground">
                    <RelativeTime value={snapshot.time} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {snapshots.length > 0 ? <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={snapshots.length} /> : null}
      </div>
      <CliEquivalent command="fleetly platform backups" />
      <p className="mt-3 flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
        <HardDriveDownloadIcon className="size-3.5" />
        Snapshot cadence and retention follow the platform backup config; the local store is same-node — not disaster recovery.
      </p>
    </section>
  );
}

function ButtonLink({ href, label }: { href: string; label: string }) {
  return (
    <a
      href={href}
      className="inline-flex h-8 items-center justify-center gap-2 rounded-md border bg-card px-3 text-xs font-medium hover:bg-muted"
    >
      {label}
    </a>
  );
}
