// 库实例列表（E4 managed-databases §4 验收步 9：Console 库实例为一等页面
// ——独立资源面 /ui/databases，与 apps 分立）。统计卡（生命周期态计数）+
// 表格（模板/状态/放置/卷/最近备份/可升级）+ 创建对话框（抽出的可复用组件
// components/create-database-dialog.tsx——项目详情页 Databases 卡同享；
// 目标项目 = 顶栏选中项目限定形，对话框内明示）。备份列逐行轻查询（limit=1，
// 无轮询——单操作员平台量级可控）。

import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Database, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import {
  listDatabaseBackups,
  listDatabases,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { DatabaseView } from "@/api/types";
import { CreateDatabaseDialog } from "@/components/create-database-dialog";
import { EmptyState } from "@/components/empty-state";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PageHeader } from "@/components/page-header";
import { StateBadge } from "@/components/state-badge";
import { StatCard } from "@/components/stat-card";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatBytes, timeAgo } from "@/lib/utils";
import { useIsPlatformAdmin, useProjectContext, useTeamCapabilities } from "@/lib/context";

/** 行内最近备份（limit=1 只取最新一行；无轮询——创建/触发后随缓存失效刷新）。 */
function LastBackupCell({ name }: { name: string }) {
  const query = useQuery({
    queryKey: ["database-backups", name],
    queryFn: () => listDatabaseBackups(name, 1),
    staleTime: 30_000,
  });
  const row = query.data?.backups?.[0];
  if (!row) {
    return <span className="text-muted-foreground">—</span>;
  }
  const failed = row.verify_status === "failed";
  return (
    <span className={failed ? "text-red-600 dark:text-red-400" : undefined}>
      {row.kind} · {timeAgo(row.created_at)}
      {failed ? " (verify failed)" : ""}
    </span>
  );
}

function DatabaseRow({ db }: { db: DatabaseView }) {
  const navigate = useNavigate();
  const name = db.name ?? "";
  return (
    <TableRow
      data-testid="database-row"
      data-state={db.status}
      className="cursor-pointer"
      onClick={() => navigate(`/databases/${encodeURIComponent(name)}`)}
    >
      <TableCell>
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-muted/40">
            <Database aria-hidden className="h-4 w-4 text-muted-foreground" />
          </span>
          <span className="min-w-0">
            <Link
              to={`/databases/${encodeURIComponent(name)}`}
              className="block truncate font-medium hover:underline"
              onClick={(e) => e.stopPropagation()}
            >
              {name}
            </Link>
            <span className="block truncate font-mono text-xs text-muted-foreground">
              {db.template}
            </span>
          </span>
        </div>
      </TableCell>
      <TableCell>
        <StateBadge state={db.status ?? ""} />
      </TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {db.placement || "—"}
      </TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground">
        {db.volume ? `${db.volume.name} (${db.volume.status})` : "—"}
      </TableCell>
      <TableCell className="text-xs">
        <LastBackupCell name={name} />
      </TableCell>
      <TableCell className="text-xs">
        {db.upgrade_available ? (
          <span data-testid="database-upgrade-available" className="font-medium text-amber-600 dark:text-amber-400">
            upgrade available
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">{formatBytes(db.limits?.memory_bytes)}</TableCell>
      <TableCell className="w-10 text-right">
        <ChevronRight aria-hidden className="ml-auto h-4 w-4 text-muted-foreground/60" />
      </TableCell>
    </TableRow>
  );
}

export function DatabasesPage() {
  // 项目上下文收窄（W2-S5）+ 创建按钮角色门（库生命周期 = admin+，§3.2
  // 矩阵；前端体验门，服务端硬门不变）。平台管理员资源面恒只读（P0-3
  // 双门）——创建钮消失时以说明卡明示原因，不做静默消失。
  const { projectRef } = useProjectContext();
  const { canAdminResources } = useTeamCapabilities();
  const isPlatformAdmin = useIsPlatformAdmin();
  const query = useQuery({
    queryKey: ["databases", projectRef],
    queryFn: () => listDatabases(projectRef ? { project: projectRef } : {}),
    refetchInterval: 5000,
  });
  const [createOpen, setCreateOpen] = useState(false);

  const databases = useMemo(() => query.data?.databases ?? [], [query.data]);
  const counts = useMemo(() => {
    const by: Record<string, number> = {};
    for (const d of databases) {
      by[d.status ?? ""] = (by[d.status ?? ""] ?? 0) + 1;
    }
    return by;
  }, [databases]);

  // P0-3 只读说明：Create database 按钮因平台管理员身份隐藏时，落一张
  // 说明卡（三个返回形态共享——pending/error/ready 的页头都在）。
  const platformReadonlyNote = !canAdminResources && isPlatformAdmin ? (
    <Card className="border-dashed">
      <CardContent
        className="p-4 text-sm text-muted-foreground"
        data-testid="platform-readonly-note"
      >
        Platform administrators have read-only access to resources (separation
        of duties). Create and manage database instances from the CLI with a
        machine token, or ask a team owner for a member role.
      </CardContent>
    </Card>
  ) : null;

  const header = (
    <PageHeader
      title="Databases"
      description="Managed database instances: platform-run engines with credentials, backups and lifecycle — a separate resource type from apps."
      actions={
        <>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void query.refetch()}
            aria-label="Refresh databases"
          >
            Refresh
          </Button>
          {canAdminResources ? (
            <Button size="sm" data-testid="database-create-button" onClick={() => setCreateOpen(true)}>
              <Plus aria-hidden className="h-3.5 w-3.5" />
              Create database
            </Button>
          ) : null}
        </>
      }
    />
  );

  if (query.isPending) {
    return (
      <div className="space-y-4" data-testid="databases-page">
        {header}
        {platformReadonlyNote}
        <p className="text-sm text-muted-foreground">Loading databases…</p>
      </div>
    );
  }
  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <div className="space-y-4" data-testid="databases-page">
        {header}
        {platformReadonlyNote}
        <EnvelopeAlert
          code={envelope.code}
          message={envelope.message}
          suggestion={envelope.suggestion}
          docs={envelope.docs}
        />
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="databases-page">
      {header}
      {platformReadonlyNote}
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard label="Instances" value={databases.length} />
        <StatCard label="Ready" value={counts.ready ?? 0} />
        <StatCard label="Converging" value={(counts.provisioning ?? 0) + (counts.paused ?? 0)} sub="provisioning + paused" />
        <StatCard label="Attention" value={(counts.failed ?? 0) + (counts.degraded ?? 0)} sub="failed + degraded" />
      </div>
      <Card>
        <CardContent className="p-0">
          {databases.length === 0 ? (
            <EmptyState
              icon={Database}
              title="No database instances yet."
              hint="Create one with the button above — or 'fleetly databases create <name> --template postgres-16'. Referencing apps declare the instance with the fleetly.databases compose label."
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Instance</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Placement</TableHead>
                  <TableHead>Volume</TableHead>
                  <TableHead>Last backup</TableHead>
                  <TableHead>Upgrade</TableHead>
                  <TableHead>Memory limit</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {databases.map((db) => (
                  <DatabaseRow key={db.id} db={db} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      {/* 抽出的可复用对话框：目标项目 = 顶栏选中项目（对话框内明示）——
          与抽取前行为一致（projectRef 缺省回落服务端缺省）。 */}
      <CreateDatabaseDialog open={createOpen} onOpenChange={setCreateOpen} projectRef={projectRef} />
    </div>
  );
}
