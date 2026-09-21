// 状态备份台账卡（T2.22 台账 + E3-3 上传轨）：本地回读校验（verify）与
// 远端上传（upload）两列结论如实并列——上传失败不回写 verify，红色行是
// 告警面的一部分（设计 §5.5 锚点 backup-upload-status）。

import { useQuery } from "@tanstack/react-query";
import { DatabaseBackup } from "lucide-react";

import { listBackups } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EmptyState } from "@/components/empty-state";
import { EnvelopeAlertFrom } from "@/components/envelope-alert";
import { StatusDot } from "@/components/status-dot";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatTime, timeAgo } from "@/lib/utils";
import type { BackupView } from "@/api/types";

function formatSize(bytes: string | undefined): string {
  const n = Number(bytes ?? 0);
  if (!Number.isFinite(n) || n <= 0) return "—";
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

/** 上传结论单元格（none/ok/failed + uploaded_at；failed 红色 + 原因摘要）。 */
function UploadStatusCell({ backup }: { backup: BackupView }) {
  const status = backup.upload_status ?? "none";
  if (status === "ok") {
    return (
      <span
        data-testid="backup-upload-status"
        data-upload-status="ok"
        title={backup.uploaded_at ? `uploaded ${formatTime(backup.uploaded_at)}` : undefined}
        className="inline-flex items-center gap-1.5 text-xs"
      >
        <StatusDot state="running" />
        <span className="text-muted-foreground">
          uploaded {timeAgo(backup.uploaded_at)}
        </span>
      </span>
    );
  }
  if (status === "failed") {
    return (
      <span
        data-testid="backup-upload-status"
        data-upload-status="failed"
        title={backup.upload_error || "upload failed"}
        className="inline-flex items-center gap-1.5 text-xs text-red-600 dark:text-red-400"
      >
        <StatusDot state="failed" />
        <span className="max-w-[220px] truncate">
          {backup.upload_error || "upload failed"}
        </span>
      </span>
    );
  }
  return (
    <span
      data-testid="backup-upload-status"
      data-upload-status="none"
      className="text-xs text-muted-foreground"
    >
      not uploaded
    </span>
  );
}

export function BackupsCard() {
  const backupsQuery = useQuery({
    queryKey: ["system", "backups"],
    queryFn: listBackups,
    refetchInterval: 15000,
  });

  const backups = backupsQuery.data?.backups ?? [];

  return (
    <Card>
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <CardTitle className="text-sm font-semibold">Backups</CardTitle>
      </CardHeader>
      <CardContent className="pt-4">
        {backupsQuery.isError ? (
          <EnvelopeAlertFrom envelope={errorEnvelopeFrom(backupsQuery.error)} />
        ) : backups.length === 0 ? (
          <EmptyState
            icon={DatabaseBackup}
            title="No backups yet"
            hint="The daemon takes a daily snapshot and one after every successful deploy."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Created</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Size</TableHead>
                <TableHead>Verify</TableHead>
                <TableHead>Remote upload</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {backups.map((b) => {
                const verifyFailed = b.verify_status === "failed";
                return (
                  <TableRow key={b.id}>
                    <TableCell
                      className="whitespace-nowrap text-xs text-muted-foreground"
                      title={b.created_at}
                    >
                      {formatTime(b.created_at)}
                    </TableCell>
                    <TableCell className="text-xs">{b.kind}</TableCell>
                    <TableCell className="text-xs">{formatSize(b.size_bytes)}</TableCell>
                    <TableCell>
                      <span
                        className={`inline-flex items-center gap-1.5 text-xs ${
                          verifyFailed ? "text-red-600 dark:text-red-400" : ""
                        }`}
                        title={b.error || undefined}
                      >
                        <StatusDot state={verifyFailed ? "failed" : "running"} />
                        {b.verify_status}
                      </span>
                    </TableCell>
                    <TableCell>
                      <UploadStatusCell backup={b} />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
