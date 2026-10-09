// 旧 hash 深链一次性迁移 shim（ADR-0057）：v2 换 browser history 干净
// URL 后，历史 `#/...` 链接（书签/聊天记录/CLI 输出）在 main.tsx 启动时
// 重写为新路径。未知 hash 不动（root 会兜到 /overview）。
const LEGACY_ROUTES = new Set([
  "deployments",
  "apps",
  "resources",
  "tasks",
  "observability",
  "identity",
  "nodes",
  "logs",
  "events",
  "audit",
  "settings",
  "quickstart",
  "templates",
  "terminal",
]);

export function hashToPath(hash: string): string | null {
  if (!hash.startsWith("#/")) return null;
  const segments = hash.slice(2).split("/").filter(Boolean);
  const head = segments[0] ?? "";
  if (head === "deployments" && segments[1] !== undefined) {
    return `/deployments/${encodeURIComponent(segments[1])}`;
  }
  if (LEGACY_ROUTES.has(head)) return `/${head}`;
  return null;
}
