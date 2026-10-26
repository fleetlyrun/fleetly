import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as structureSchemas } from "@/api/structure";
import { backupHealth } from "@/features/databases/backup-health";
import { toApiError } from "./errors";
import { fetchRows, qs } from "./http";

// Backups 域资源适配（2026-10-26 批1 Wave2-A useCollection 化，平台域
// 聚合——无 scope）：数据安全台账双 tab 双资源。
// - Databases tab：跨项目 fan-out 复刻旧页（projects 整取 → 逐项目
//   /v1/databases 合并；≤50 项目上界、单项目失败吞为空段——useQueries
//   失败行 data??[] 同语义）。行序 = 旧页业务排序（过期库先于健康库、组
//   内最近成功备份升序）保留在 fetch 内（派生自 backupHealth 的"现在"，
//   不是行字段——defaultOrder 表达不了）。
// - Snapshots tab：平台快照台账（GET /v1/platform/backups；system 服务
//   不在生成上下文，行形态内联手写——与 catalog PlatformSnapshot 同形）。

type DatabaseEntry = structureSchemas["schemas"]["v1Database"];

// 行形态：快筛字段平铺到行首（name/engine/project_name 直达行字段——
// 客户端求值谓词不走嵌套路径，registry 先例），database 整行随行携带
//（restore/健康列直读）。
export interface DatabaseBackupRow {
  id: string;
  name: string;
  engine: string;
  project_id: string;
  project_name: string;
  database: DatabaseEntry;
}

export interface PlatformSnapshotRow {
  id?: string;
  time?: string;
  hostname?: string;
}

const PROJECTS_FETCH_LIMIT = 100;
const PROJECT_FAN_OUT_LIMIT = 50;

// 项目目录段：/v1/projects?limit=100 → {id,name} 投影（缺 id/name 行过滤
// ——catalog useProjects 同口径）。
async function fetchProjectDirectory(signal?: AbortSignal): Promise<Array<{ id: string; name: string }>> {
  const projects = await fetchRows<{ id?: string; name?: string }>(
    `/v1/projects${qs({ limit: PROJECTS_FETCH_LIMIT })}`,
    (res) => (res as { projects?: Array<{ id?: string; name?: string } | undefined> }).projects,
    signal,
  );
  return projects.flatMap((project) => (project.id && project.name ? [{ id: project.id, name: project.name }] : []));
}

// 行序（"昨晚成没成"的视线序，旧页同款）：过期库先于健康库，组内按最近
// 成功备份升序（最久未备份最前）。
function byStaleness(row: DatabaseBackupRow): number {
  return backupHealth(row.database.last_backup_at).tone !== "success" ? 0 : 1;
}

export async function fetchDatabaseBackups(_ctx: ResourceScope, signal?: AbortSignal): Promise<DatabaseBackupRow[]> {
  try {
    const projects = await fetchProjectDirectory(signal);
    const targets = projects.slice(0, PROJECT_FAN_OUT_LIMIT);
    const perProject = await Promise.all(
      targets.map((project) =>
        fetchRows<DatabaseEntry>(
          `/v1/databases${qs({ project_id: project.id })}`,
          (res) => (res as { databases?: Array<DatabaseEntry | undefined> }).databases,
          signal,
        ).catch(() => [] as DatabaseEntry[]),
      ),
    );
    const rows = targets.flatMap((project, index) =>
      perProject[index].map((database) => ({
        id: database.id ?? "",
        name: database.name ?? "",
        engine: database.engine ?? "",
        project_id: project.id,
        project_name: project.name,
        database,
      })),
    );
    // projects 轴失败即整列表失败（诚实错误态——旧页静默空表掩错，kit 契约
    // 下收敛为 kernel ApiError 走 ListPage 错误分支）
    return rows.sort((a, b) => {
      const stale = byStaleness(a) - byStaleness(b);
      if (stale !== 0) return stale;
      return (a.database.last_backup_at ?? "").localeCompare(b.database.last_backup_at ?? "");
    });
  } catch (err) {
    throw toApiError(err);
  }
}

export const databaseBackupsResource = defineResource({
  name: "database-backups",
  getId: (row: DatabaseBackupRow) => row.id,
  collection: clientCollection(fetchDatabaseBackups),
});

export async function fetchPlatformSnapshots(_ctx: ResourceScope, signal?: AbortSignal): Promise<PlatformSnapshotRow[]> {
  try {
    return await fetchRows<PlatformSnapshotRow>(
      "/v1/platform/backups",
      (res) => (res as { snapshots?: Array<PlatformSnapshotRow | undefined> }).snapshots,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

export const platformSnapshotsResource = defineResource({
  name: "platform-snapshots",
  getId: (row: PlatformSnapshotRow) => row.id ?? "",
  collection: clientCollection(fetchPlatformSnapshots),
});

// 双 tab 共用实例：q/分页跨 tab 持久是接受语义（tab 切换函数式 search 保
// 留集合键）。字段集 = Databases（name/engine/project_name）∪ Snapshots
//（id/hostname）。
export const backupsSearch = collectionQuerySchema({
  text: ["name", "engine", "project_name", "id", "hostname"],
  defaultSize: 20,
});
