import { format, isValid, parseISO } from "date-fns";

// 展示格式化单源（UI v2）：相对时间 + 悬浮绝对时间是全站契约（交互契约
// §反馈），ID 截断与字节/时长缩写在此集中，页面禁止自拼时间文案（反模式
// 守卫 scanning，见 console.ui.antipattern.test.ts）。

// formatRelative 把 RFC3339 时间戳渲染为紧凑相对形态（"3m ago"；>7d 落
// 到短日期）。失败原样返回——诚实边界：坏数据不伪装成正常时间。
export function formatRelative(value: string | undefined): string {
  if (!value) return "";
  const date = value.endsWith("Z") || value.includes("+") ? parseISO(value) : parseISO(`${value}Z`);
  if (!isValid(date)) return value;
  const diffSeconds = Math.round((Date.now() - date.getTime()) / 1000);
  if (diffSeconds < 60) return `${Math.max(diffSeconds, 0)}s ago`;
  const minutes = Math.floor(diffSeconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  return format(date, "MMM d");
}

// formatAbsolute 是悬浮提示用的绝对形态（本地时区）。
export function formatAbsolute(value: string | undefined): string {
  if (!value) return "";
  const date = value.endsWith("Z") || value.includes("+") ? parseISO(value) : parseISO(`${value}Z`);
  if (!isValid(date)) return value;
  return format(date, "yyyy-MM-dd HH:mm:ss");
}

// formatBytes 是字节的人类可读缩写（protojson 数值/字符串皆可）。
export function formatBytes(value: number | string | undefined | null): string {
  const n = typeof value === "string" ? Number(value) : value;
  if (n == null || !Number.isFinite(n)) return "—";
  const abs = Math.abs(n);
  if (abs >= 1024 ** 4) return `${(n / 1024 ** 4).toFixed(2)} TiB`;
  if (abs >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(2)} GiB`;
  if (abs >= 1024 ** 2) return `${(n / 1024 ** 2).toFixed(1)} MiB`;
  if (abs >= 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${n} B`;
}

// formatDuration 把秒数渲染为 "2m 14s" 形态（<1s 显示毫秒级）。
export function formatDuration(seconds: number | string | undefined | null): string {
  const n = typeof seconds === "string" ? Number(seconds) : seconds;
  if (n == null || !Number.isFinite(n)) return "—";
  if (n < 1) return `${Math.round(n * 1000)}ms`;
  const m = Math.floor(n / 60);
  const s = Math.round(n % 60);
  if (m === 0) return `${s}s`;
  if (m < 60) return `${m}m ${String(s).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}

// shortId 取超长 id/digest 的显示截断（可读 id 多数短于阈值原样展示），
// 完整值由 CopyButton/title 承载。
export function shortId(value: string | undefined): string {
  if (!value) return "—";
  return value.length > 10 ? `${value.slice(0, 10)}…` : value;
}

// revisionLabel 呈现 revision 序号（R12；seq 对表未命中退化为 id 尾 6 位
// 短形式——诚实降级，不伪装成序号）。单源消费方：Overview 现行/最近部署
// 列、App Deployments 列表 Revision 列（W-2 修复：列表不再恒 R…hash）。
export function revisionLabel(seq: string | undefined, revisionId: string | undefined): string {
  if (seq) return `R${seq}`;
  if (revisionId) return `R…${revisionId.slice(-6)}`;
  return "—";
}

// sourceKindLabel 把 SourceKind 枚举（protojson 原串）归一为展示词：
// git / image / uploaded source——材料来源投影（ADR-0071 决策 6，值域只有
// 三值）。compose 受理落 image：source_text 非空时改报 compose（受理原文
// 在 Revision 行上，Spec tab Compose Source 卡同判据），不新增枚举。
// 消费方：app hero 徽标 / Overview Current status / Settings Deployment 卡。
export function sourceKindLabel(kind: string | undefined, sourceText?: string): string {
  const value = (kind ?? "").replace("SOURCE_KIND_", "").toLowerCase();
  if (value === "git") return "git";
  if (value === "upload") return "uploaded source";
  if (value === "image") return sourceText ? "compose" : "image";
  return "—";
}
