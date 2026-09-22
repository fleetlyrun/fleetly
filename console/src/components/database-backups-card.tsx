// 库备份卡（E4 managed-databases §2.6，W4-S6）：台账列表（kind/snapshot/
// size/verify_status/created_at——verify_status=failed 是红色告警面的一部
// 分，行如实保留）+ 手动备份触发 + 逐行原地恢复（破坏性两段式确认）+
// s3.mode=rustfs 的「便捷层非灾备」常驻诚实标注（§6 诚实口径行，D-S3-8
// 延伸——复用 System 存储页同一 API 投影判断模式）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Clock, HardDriveDownload, ShieldAlert } from "lucide-react";
import { useState } from "react";

import {
  getS3Settings,
  listDatabaseBackups,
  restoreDatabaseBackup,
  triggerDatabaseBackup,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { DatabaseBackupView } from "@/api/types";
import { EmptyState } from "@/components/empty-state";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatBytes, formatTime, timeAgo } from "@/lib/utils";

// RustFS 同节点诚实口径（managed-databases §2.6 诚实口径行——文案与
// s3-settings-card 的 D-S3-8 注记同族，限定到库备份）。
const RUSTFS_DB_BACKUP_NOTE =
  "Backups stored on the same-host RustFS are a convenience layer (protection against accidental deletion), not disaster recovery: if the host is lost, these backups are lost with it.";

function verifyTone(status: string | undefined): string {
  switch (status) {
    case "verified":
      return "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400";
    case "failed":
      return "bg-red-500/10 text-red-700 dark:text-red-400";
    default:
      return "bg-muted text-muted-foreground";
  }
}

function RestoreDialog({
  db,
  row,
  onOpenChange,
}: {
  db: string;
  row: DatabaseBackupView;
  onOpenChange: (v: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const name = db;
  const confirmOk = confirm === name;

  const restoreMutation = useMutation({
    mutationFn: () => restoreDatabaseBackup(name, row.snapshot ?? "", name),
    onSuccess: () => {
      setError(null);
      onOpenChange(false);
      // 原地恢复 = 停库重放（分钟级）：实例与台账随后都会动，交给轮询。
      void queryClient.invalidateQueries({ queryKey: ["database", name] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent data-testid="database-restore-dialog">
        <DialogHeader>
          <DialogTitle>Restore from snapshot</DialogTitle>
          <DialogDescription>
            The instance is stopped while snapshot{" "}
            <code className="font-mono text-xs">{row.snapshot}</code> is replayed
            over the current data on the volume. Everything written after that
            backup is overwritten — this cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-1.5">
          <Label htmlFor="database-restore-confirm">
            Type the instance name <span className="font-mono">{name}</span> to confirm
          </Label>
          <Input
            id="database-restore-confirm"
            data-testid="database-restore-confirm-input"
            className="font-mono text-xs"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </div>
        {error ? (
          <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
        ) : null}
        <DialogFooter>
          <Button
            variant="destructive"
            data-testid="database-restore-submit"
            disabled={!confirmOk || restoreMutation.isPending}
            onClick={() => restoreMutation.mutate()}
          >
            {restoreMutation.isPending ? "Restoring…" : "Restore (stops the database)"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function DatabaseBackupsCard({ name, actionable }: { name: string; actionable: boolean }) {
  const queryClient = useQueryClient();
  const backupsQuery = useQuery({
    queryKey: ["database-backups", name],
    queryFn: () => listDatabaseBackups(name),
    refetchInterval: 10_000,
  });
  const s3Query = useQuery({
    queryKey: ["system", "s3-settings"],
    queryFn: getS3Settings,
    staleTime: 60_000,
  });
  const [restoreRow, setRestoreRow] = useState<DatabaseBackupView | null>(null);
  const [triggerError, setTriggerError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  const triggerMutation = useMutation({
    mutationFn: () => triggerDatabaseBackup(name),
    onSuccess: () => {
      setTriggerError(null);
      // 异步受理：在途备份无台账行——稍后随轮询出现。
      void queryClient.invalidateQueries({ queryKey: ["database-backups", name] });
    },
    onError: (err) => setTriggerError(errorEnvelopeFrom(err)),
  });

  const backups = backupsQuery.data?.backups ?? [];
  const isRustfs = s3Query.data?.settings?.mode === "rustfs";

  return (
    <Card data-testid="database-backups-card">
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <HardDriveDownload aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Backups</CardTitle>
        <CardDescription className="ml-auto text-xs">
          pg_dump / RDB exports in the platform restic repository, verified by read-back
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        {isRustfs ? (
          <div
            data-testid="database-honesty-note"
            className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-xs text-amber-800 dark:text-amber-300"
          >
            <ShieldAlert aria-hidden className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            {RUSTFS_DB_BACKUP_NOTE}
          </div>
        ) : null}

        <div className="flex items-center justify-between gap-2">
          <p className="text-xs text-muted-foreground">
            Restores stop the database and replay the snapshot in place; a
            restore replays the backup-time password — rotate afterwards if
            credentials changed since the backup.
          </p>
          <Button
            size="sm"
            variant="outline"
            data-testid="database-backup-trigger-button"
            disabled={!actionable || triggerMutation.isPending}
            title={
              actionable
                ? "Trigger a manual backup (async)"
                : "Backups need the instance in ready or degraded state"
            }
            onClick={() => triggerMutation.mutate()}
          >
            <Clock aria-hidden className="h-3.5 w-3.5" />
            Back up now
          </Button>
        </div>
        {triggerError ? (
          <EnvelopeAlert code={triggerError.code} message={triggerError.message} suggestion={triggerError.suggestion} />
        ) : null}

        {backupsQuery.isError ? (
          <EnvelopeAlertFromQuery error={backupsQuery.error} />
        ) : backups.length === 0 ? (
          <EmptyState
            icon={HardDriveDownload}
            title="No backups recorded."
            hint="Trigger a manual backup, or wait for the daily plan (default 03:00 UTC, keeps 7). Backups need an object store target configured in System → Storage."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Kind</TableHead>
                <TableHead>Snapshot</TableHead>
                <TableHead>Size</TableHead>
                <TableHead>Verify</TableHead>
                <TableHead>Created</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {backups.map((b) => (
                <TableRow key={b.id} data-testid="database-backup-row" data-verify={b.verify_status}>
                  <TableCell className="text-xs">{b.kind}</TableCell>
                  <TableCell className="max-w-[180px] truncate font-mono text-xs" title={b.snapshot}>
                    {b.snapshot}
                  </TableCell>
                  <TableCell className="text-xs">{formatBytes(b.size_bytes)}</TableCell>
                  <TableCell>
                    <Badge className={`text-xs ${verifyTone(b.verify_status)}`} variant="secondary">
                      {b.verify_status}
                    </Badge>
                    {b.error ? (
                      <span className="ml-2 text-xs text-red-600 dark:text-red-400" title={b.error}>
                        {b.error}
                      </span>
                    ) : null}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground" title={formatTime(b.created_at)}>
                    {timeAgo(b.created_at)}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 text-red-600 dark:text-red-400"
                      data-testid="database-restore-button"
                      disabled={!actionable}
                      title={
                        actionable
                          ? `Restore snapshot ${b.snapshot} in place (destructive)`
                          : "Restores need the instance in ready or degraded state"
                      }
                      onClick={() => setRestoreRow(b)}
                    >
                      Restore
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
      {restoreRow ? (
        <RestoreDialog db={name} row={restoreRow} onOpenChange={(v) => (v ? undefined : setRestoreRow(null))} />
      ) : null}
    </Card>
  );
}

function EnvelopeAlertFromQuery({ error }: { error: unknown }) {
  const envelope = errorEnvelopeFrom(error);
  return <EnvelopeAlert code={envelope.code} message={envelope.message} suggestion={envelope.suggestion} />;
}
