import type { ReactNode } from "react";
import { ApiError } from "../api/client";

// 共享 UI 件：页面骨架（标题/摘要/工具行）、状态徽章、错误/加载态。
// 用户可见文本英文；配色只走 Tailwind 原子类（无自定义 CSS 依赖面）。

export function PageShell({
  title,
  hint,
  toolbar,
  children,
}: {
  title: string;
  hint?: string;
  toolbar?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h1 className="text-lg font-semibold text-slate-100">{title}</h1>
        {hint ? <p className="text-xs text-slate-500">{hint}</p> : null}
        {toolbar ? <div className="ml-auto flex flex-wrap items-center gap-2">{toolbar}</div> : null}
      </div>
      {children}
    </section>
  );
}

export function ErrorNote({ error, hint }: { error: unknown; hint?: string }) {
  const detail =
    error instanceof ApiError
      ? `${error.code}: ${error.message}`
      : error instanceof Error
        ? error.message
        : String(error);
  return (
    <div className="rounded-md border border-red-900/60 bg-red-950/40 px-3 py-2 text-sm text-red-300">
      <div className="font-medium">Request failed</div>
      <div className="mt-0.5 font-mono text-xs break-words">{detail}</div>
      {hint ? <div className="mt-1 text-xs text-red-400/80">{hint}</div> : null}
    </div>
  );
}

export function LoadingNote({ label }: { label: string }) {
  return <div className="px-1 py-8 text-center text-sm text-slate-500">{label}</div>;
}

export function EmptyNote({ label }: { label: string }) {
  return <div className="px-1 py-8 text-center text-sm text-slate-600">{label}</div>;
}

// formatTime 把 RFC3339 时间戳渲染为本地紧凑形态（失败原样返回）。
export function formatTime(value: string | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, { hour12: false });
}

// shortId 取 ULID/digest 显示形态（前 10 字符），完整值进 title 提示。
export function shortId(value: string | undefined): string {
  if (!value) return "—";
  return value.length > 10 ? `${value.slice(0, 10)}…` : value;
}
