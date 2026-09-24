// 库实例列表（E4 managed-databases §4 验收步 9：Console 库实例为一等页面
// ——独立资源面 /ui/databases，与 apps 分立）。统计卡（生命周期态计数）+
// 表格（模板/状态/放置/卷/最近备份/可升级）+ 创建对话框（模板 + 名 + 可选
// 限额）。备份列逐行轻查询（limit=1，无轮询——单操作员平台量级可控）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Database, Plus } from "lucide-react";
import { useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";

import {
  createDatabase,
  listDatabaseBackups,
  listDatabases,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { DatabaseView } from "@/api/types";
import { EmptyState } from "@/components/empty-state";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PageHeader } from "@/components/page-header";
import { StateBadge } from "@/components/state-badge";
import { StatCard } from "@/components/stat-card";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatBytes, timeAgo } from "@/lib/utils";
import { useProjectContext, useTeamCapabilities } from "@/lib/context";

const TEMPLATES = [
  { value: "postgres-16", label: "postgres-16" },
  { value: "redis-7", label: "redis-7" },
];

const NAME_PATTERN = /^[a-z0-9][a-z0-9_-]*$/;

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

function CreateDatabaseDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const queryClient = useQueryClient();
  const { projectRef } = useProjectContext();
  const [name, setName] = useState("");
  const [template, setTemplate] = useState("postgres-16");
  const [cpu, setCpu] = useState("");
  const [memoryGiB, setMemoryGiB] = useState("");
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  const nameOk = NAME_PATTERN.test(name);
  const createMutation = useMutation({
    mutationFn: () => {
      const cpuN = Number(cpu);
      const memN = Number(memoryGiB);
      return createDatabase({
        name,
        template,
        // 项目上下文收窄（W2-S5）：顶栏选中项目即创建目标（限定形
        // team/prj）；无选择 = 服务端缺省（调用者个人队 default 项目）。
        project: projectRef || undefined,
        limits:
          cpu.trim() !== "" || memoryGiB.trim() !== ""
            ? {
                cpu_seconds: cpu.trim() !== "" && !Number.isNaN(cpuN) ? cpuN : 0,
                memory_bytes:
                  memoryGiB.trim() !== "" && !Number.isNaN(memN)
                    ? String(Math.round(memN * 1024 * 1024 * 1024))
                    : undefined,
              }
            : undefined,
      });
    },
    onSuccess: () => {
      setError(null);
      setName("");
      setCpu("");
      setMemoryGiB("");
      void queryClient.invalidateQueries({ queryKey: ["databases"] });
      onOpenChange(false);
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (nameOk) createMutation.mutate();
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="database-create-dialog">
        <DialogHeader>
          <DialogTitle>Create database</DialogTitle>
          <DialogDescription>
            A managed instance is created immediately (status provisioning) and
            converges to ready once the engine health gate passes. Credentials
            are generated once and stored encrypted; they are injected into
            referencing apps as <code>FLEETLY_DB_*</code> variables.
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <Label htmlFor="database-name">Name</Label>
            <Input
              id="database-name"
              data-testid="database-name-input"
              className="font-mono text-xs"
              placeholder="pg-prod"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              lowercase letters, digits, - and _ (must start alphanumeric).
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="database-template">Template</Label>
            <Select value={template} onValueChange={setTemplate}>
              <SelectTrigger id="database-template" data-testid="database-template-select" className="w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TEMPLATES.map((t) => (
                  <SelectItem key={t.value} value={t.value}>
                    {t.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="database-cpu">CPU limit (cores)</Label>
              <Input
                id="database-cpu"
                data-testid="database-cpu-input"
                className="font-mono text-xs"
                placeholder="template default"
                inputMode="decimal"
                value={cpu}
                onChange={(e) => setCpu(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="database-memory">Memory limit (GiB)</Label>
              <Input
                id="database-memory"
                data-testid="database-memory-input"
                className="font-mono text-xs"
                placeholder="template default"
                inputMode="decimal"
                value={memoryGiB}
                onChange={(e) => setMemoryGiB(e.target.value)}
              />
            </div>
          </div>
          {error ? (
            <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
          ) : null}
          <DialogFooter>
            <Button
              type="submit"
              data-testid="database-create-submit"
              disabled={!nameOk || createMutation.isPending}
            >
              {createMutation.isPending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
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
  // 矩阵；前端体验门，服务端硬门不变）。
  const { projectRef } = useProjectContext();
  const { canAdminResources } = useTeamCapabilities();
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
        <p className="text-sm text-muted-foreground">Loading databases…</p>
      </div>
    );
  }
  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <div className="space-y-4" data-testid="databases-page">
        {header}
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
      <CreateDatabaseDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}
