import type { StatusTone } from "@/components/domain/status-badge";

// 备份健康度派生（IA v3 T3）：从 v1Database.last_backup_at（'' = 从未成功，
// ADR-0039 调度锚）派生列表页"Backup health"列。阈值 48h 是无调度信息侧的
// 诚实启发——平台备份节拍可配（config.platform_backup），行上没有节拍字段，
// 不假装配平。
export interface BackupHealth {
  label: string;
  tone: StatusTone;
}

const STALE_AFTER_MS = 48 * 60 * 60_000;

export function backupHealth(lastBackupAt: string | undefined, now: Date = new Date()): BackupHealth {
  if (lastBackupAt == null || lastBackupAt === "") {
    return { label: "never", tone: "neutral" };
  }
  const at = new Date(lastBackupAt).getTime();
  if (Number.isNaN(at)) {
    return { label: "never", tone: "neutral" };
  }
  const ageMs = Math.max(0, now.getTime() - at);
  const hours = Math.floor(ageMs / 60 / 60_000);
  const age = hours < 1 ? "<1h ago" : hours < 48 ? `${hours}h ago` : `${Math.floor(hours / 24)}d ago`;
  return ageMs >= STALE_AFTER_MS ? { label: `${age} · stale`, tone: "warning" } : { label: `${age} · ok`, tone: "success" };
}
