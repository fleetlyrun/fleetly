// 事件 subject / 审计 target 的可读投影（backlog #12 的 Console 半步，
// 2026-09-26 走查 W2-7）：`kind:ref` 形态按缓存反解业务名，反解不到按 kind
// 降级——26 字符平台 ID 对人不可读，Home 活动流/Events/审计 target/库
// placement 各自裸显是同族问题，可读化统一出口在此。完整解 = 服务端投影
// 补名称/属主字段（机制票 #12 在册），本层只做展示收敛、不改过滤语义
// （审计 target 过滤仍按原始行值）。

/** 反解器可用的名称映射（各缓存行是生成类型的窄投影——本文件只读这些字段）。 */
export interface SubjectLookup {
  apps?: { id?: string; name?: string }[];
  databases?: { id?: string; name?: string }[];
  teams?: { id?: string; name?: string }[];
  projects?: { id?: string; slug?: string; team_slug?: string }[];
  nodes?: { platform_id?: string; hostname?: string }[];
  users?: { id?: string; email?: string }[];
}

// 26 字符平台 ID（ULID 字典：0-9 + Crockford base32 去 I/L/O/U）。
const PLATFORM_ID = /^[0-9A-HJKMNP-TV-Z]{26}$/i;

/**
 * subjectLabel 把 `kind:ref` 投影为人读形态：
 * - app/team/database/user 命中缓存 → `kind:业务名`；
 * - project → `project:team/prj`；
 * - node → 节点主机名；
 * - 反解不到且 ref 是平台 ID → 只出 kind（ID 对人无信息量；事件名+时刻已
 *   足以定位）；反解不到且 ref 本就可读（database:pgshared、platform:*）
 *   → 原样保留。
 */
export function subjectLabel(subject: string | undefined, lookup: SubjectLookup): string {
  if (!subject) return "";
  // 裸节点平台 ID（db placement 形态 `n_<id>`，无 kind 前缀）。
  if (/^n_[0-9A-HJKMNP-TV-Z]{26}$/i.test(subject)) {
    return nodeLabel(subject, lookup.nodes);
  }
  const i = subject.indexOf(":");
  if (i < 0) return subject;
  const kind = subject.slice(0, i);
  const ref = subject.slice(i + 1);
  switch (kind) {
    case "app": {
      const app = lookup.apps?.find((a) => a.id === ref);
      return app?.name ? `app:${app.name}` : degrade(subject, kind, ref);
    }
    case "database": {
      const db = lookup.databases?.find((d) => d.id === ref);
      return db?.name ? `database:${db.name}` : degrade(subject, kind, ref);
    }
    case "team": {
      const team = lookup.teams?.find((t) => t.id === ref);
      return team?.name ? `team:${team.name}` : degrade(subject, kind, ref);
    }
    case "project": {
      const proj = lookup.projects?.find((p) => p.id === ref);
      return proj?.slug ? `project:${proj.team_slug ?? "?"}/${proj.slug}` : degrade(subject, kind, ref);
    }
    case "node":
      return nodeLabel(subject, lookup.nodes);
    case "user": {
      const u = lookup.users?.find((x) => x.id === ref);
      return u?.email ? `user:${u.email}` : degrade(subject, kind, ref);
    }
    default:
      // 未知 kind（deployment/backup/invite/…）：ref 是平台 ID → 降级为
      // kind（事件名+时刻足以定位）；可读 ref 原样保留。
      return degrade(subject, kind, ref);
  }
}

/** nodeLabel 把节点平台 ID（`n_<id>` 或 `node:<id>`）投影为主机名；未命中原样返回。 */
export function nodeLabel(ref: string | undefined, nodes: SubjectLookup["nodes"]): string {
  if (!ref || !nodes?.length) return ref ?? "";
  const bare = ref.replace(/^node:/, "").replace(/^n_/, "");
  const node = nodes.find((n) => (n.platform_id ?? "").replace(/^n_/, "") === bare);
  return node?.hostname ?? ref;
}

function degrade(subject: string, kind: string, ref: string): string {
  return PLATFORM_ID.test(ref) ? kind : subject;
}
