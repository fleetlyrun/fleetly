import { useState } from "react";
import { useQueries } from "@tanstack/react-query";
import { DatabaseIcon, HardDriveDownloadIcon, LinkIcon } from "lucide-react";
import { apiFetch } from "@/api/client";
import type { components } from "@/api/structure";
import { backupHealth } from "@/features/databases/backup-health";
import { CliEquivalent, ListPagination, ListToolbar, useClientPage, useListFilter } from "@/components/domain/list-toolbar";
import { PageTabs } from "@/components/domain/page-tabs";
import { EmptyState } from "@/components/domain/empty-state";
import { PageHeader } from "@/components/domain/page-header";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { RelativeTime } from "@/components/domain/relative-time";
import { statusToneClass } from "@/components/domain/status-badge";
import { TableWrap } from "@/components/ui";
import { usePlatformBackups, useProjects } from "@/lib/catalog";

type DatabaseEntry = components["schemas"]["v1Database"];

// Backups 聚合页（IA v3 T7，§5.3）：数据安全一屏回答"昨晚备份成没成"——
// 每库一行（v1Database.last_backup_at 是最近成功锚，ADR-0039，无需深度
// fan-out）+ 平台快照（restic）区块。跨项目聚合走客户端（ADR-0057 不变
// 量）；聚合 API 二期优化（§8）。
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

  const rows = projectList
    .slice(0, 50)
    .flatMap((project, index) =>
      (databaseLists[index]?.data ?? []).map((database) => ({ project, database })),
    )
    .sort((a, b) => (a.database.last_backup_at ?? "").localeCompare(b.database.last_backup_at ?? ""));
  const healthyCount = rows.filter((row) => backupHealth(row.database.last_backup_at).tone === "success").length;
  const [query, setQuery] = useState("");
  const filtered = useListFilter(rows, query, (row: { database: DatabaseEntry; project: { name?: string } }) => [
    row.database.name ?? "",
    row.database.engine ?? "",
    row.project.name ?? "",
  ]);
  const staleRows = filtered.filter((row) => backupHealth(row.database.last_backup_at).tone !== "success");
  const healthyRows = filtered.filter((row) => backupHealth(row.database.last_backup_at).tone === "success");
  const ordered = [...staleRows, ...healthyRows];
  const { page, pageCount, pageRows, setPage } = useClientPage(ordered);
  const snapList = snapshots.data ?? [];
  const { page: snapPage, pageCount: snapPageCount, pageRows: snapRows, setPage: setSnapPage } = useClientPage(snapList);

  return (
    <div className="mx-auto max-w-7xl px-6 pt-6 pb-8">
      <PageHeader
        title="Backups"
        description={
          rows.length > 0
            ? `Data safety at a glance — ${rows.length} database${rows.length === 1 ? "" : "s"} · ${healthyCount} healthy`
            : "Data safety at a glance — database backups and platform snapshots"
        }
        actions={
          <ButtonLink href="/settings" label="Platform backup settings" />
        }
      />
      <PageTabs
        tabs={[
          { value: "databases", label: "Databases" },
          { value: "snapshots", label: "Snapshots" },
        ]}
        current={tab}
        onChange={onTabChange}
      />

      {tab === "databases" ? (
      <section className="mb-8">
        <div className="rounded-xl border bg-card">
          <div className="px-3 pt-3">
            <ListToolbar label="databases" value={query} onChange={setQuery} placeholder="Filter databases..." total={rows.length} shown={filtered.length} />
          </div>
          {projects.isPending || databaseLists.some((list) => list.isPending) ? (
            <p className="py-8 text-center text-xs text-muted-foreground">Loading databases…</p>
          ) : rows.length === 0 ? (
            <EmptyState
              icon={DatabaseIcon}
              title="No databases"
              description="Database backup health rows appear here once databases exist."
            />
          ) : (
            <>
              <TableWrap className="rounded-none border-0">
              <table className="w-full text-sm">
                <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
                  <tr>
                    <th className="px-3 py-2">Database</th>
                    <th className="px-3 py-2">Project</th>
                    <th className="px-3 py-2">Engine</th>
                    <th className="px-3 py-2">Backup health</th>
                    <th className="px-3 py-2">Restore state</th>
                    <th className="px-3 py-2 text-right">Actions</th>
                  </tr>
                </thead>
                  <tbody>
                  {pageRows.map(
                  ({ project, database }) => {
                    const health = backupHealth(database.last_backup_at);
                    return (
                      <tr key={database.id} className="border-b last:border-b-0">
                        <td className="px-3 py-2">
                          <div className="flex items-center gap-2.5">
                            <ProjectAvatar seed={database.id ?? ""} label={database.name ?? ""} />
                            <div className="text-[13px] font-semibold">{database.name}</div>
                          </div>
                        </td>
                        <td className="px-3 py-2 text-xs text-muted-foreground">
                          <span className="flex items-center gap-1">
                            <LinkIcon className="size-3" />
                            {project.name}
                          </span>
                        </td>
                        <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{database.engine}</td>
                        <td className="px-3 py-2">
                          <span className={`inline-flex items-center gap-1.5 text-xs font-medium ${statusToneClass(health.tone)}`}>
                            <span className="inline-block size-1.5 rounded-full bg-current" />
                            {health.label}
                          </span>
                        </td>
                        <td className="px-3 py-2 text-xs">
                          {database.restore_from_backup ? (
                            <span className="text-info">
                              restoring from {database.restore_from_backup}
                              {database.restore_error ? <div className="text-[11px] text-destructive">{database.restore_error}</div> : null}
                            </span>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </td>
                        <td className="px-3 py-2 text-right">
                          <a
                            className="text-xs text-info underline-offset-2 hover:underline"
                            href={`/p/${encodeURIComponent(project.id)}/databases/${encodeURIComponent(database.id ?? "")}`}
                          >
                            open
                          </a>
                        </td>
                      </tr>
                    );
                  },
                )}
                </tbody>
              </table>
            </TableWrap>
              <ListPagination page={page} pageCount={pageCount} setPage={setPage} total={ordered.length} />
            </>
          )}
        </div>
      </section>
      ) : (
      <section>
        <div className="rounded-xl border bg-card">
        {(snapshots.data ?? []).length === 0 ? (
          <p className="p-6 text-center text-xs text-muted-foreground">
            No snapshots listed yet — the platform backup window writes here (local store + optional S3 mirror).
          </p>
        ) : (
          <>
            <table className="w-full text-sm">
              <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
                <tr>
                  <th className="px-3 py-2">Snapshot</th>
                  <th className="px-3 py-2">Host</th>
                  <th className="px-3 py-2">Taken</th>
                </tr>
              </thead>
              <tbody>
                {snapRows.map((snapshot) => (
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
            <ListPagination page={snapPage} pageCount={snapPageCount} setPage={setSnapPage} total={snapList.length} />
          </>
        )}
        </div>
        <CliEquivalent command="fleetly platform backups" />
        <p className="mt-3 flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
          <HardDriveDownloadIcon className="size-3.5" />
          Snapshot cadence and retention follow the platform backup config; the local store is same-node — not disaster recovery.
        </p>
      </section>
      )}
    </div>
  );
}

function ButtonLink({ href, label }: { href: string; label: string }) {
  return (
    <a
      href={href}
      className="inline-flex h-8 items-center justify gap-2 rounded-md border bg-card px-3 text-xs font-medium hover:bg-muted"
    >
      {label}
    </a>
  );
}
