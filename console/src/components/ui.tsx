import { useEffect, useRef, useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiSend } from "../api/client";

// 共享 UI 件（F2.6 只读三页骨架 + F3.1 写面扩展）：页面骨架、状态徽章、
// 错误/加载态之外，新增表单字段族（Modal 内的受控输入）、写操作确认钮
// 与结果横幅。用户可见文本英文；配色只走 Tailwind 原子类。

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

// ---- 写面扩展（F3.1） ----

// Modal 是受控表单对话框：原生 <dialog>（焦点管理/Escape 免费，Radix 留给
// 真正需要无障碍组合件的场合——最小依赖纪律）。壳层不得包 form：
// ①嵌套 form 的 submit 事件在真实浏览器不冒泡出外层 form（非规范 HTML，
// Chromium 实测截断），内层业务表单的 React onSubmit 会整体失效——2026-10-07
// 浏览器走查 W1，jsdom 冒泡行为不同所以组件测试此前全绿；②dialog 的
// close/cancel 事件不冒泡，React 19 委托面收不到，Escape 关窗会让受控 open
// 态与原生 dialog 失同步（同批走查 W3，"再开同一弹窗无响应"形态）——由
// 原生监听直挂 dialog 节点收口，Escape/✕/背板三路关闭都归一到 onClose。
export function Modal({ title, open, onClose, children }: { title: string; open: boolean; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    const dlg = ref.current;
    if (dlg === null) return;
    const handle = () => onCloseRef.current();
    dlg.addEventListener("close", handle);
    return () => dlg.removeEventListener("close", handle);
  }, []);
  useEffect(() => {
    if (open) ref.current?.showModal();
    else ref.current?.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      onClick={(event) => {
        if (event.target === ref.current) onCloseRef.current();
      }}
      className="m-auto w-full max-w-lg rounded-lg border border-slate-700 bg-slate-900 p-0 text-slate-200 backdrop:bg-slate-950/70"
    >
      <div className="flex flex-col gap-4 p-5">
        <div className="flex items-baseline justify-between gap-4">
          <h2 className="text-base font-semibold text-slate-100">{title}</h2>
          <button type="button" onClick={onClose} className="rounded px-2 py-0.5 text-sm text-slate-500 hover:bg-slate-800 hover:text-slate-300">
            ✕
          </button>
        </div>
        {children}
      </div>
    </dialog>
  );
}

const INPUT_CLASS =
  "w-full rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-200 placeholder:text-slate-600 focus:border-sky-600 focus:outline-none";

// Field 是标签 + 控件的行形态（写面表单的公因子）。
export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-slate-400">
      <span className="font-medium text-slate-300">{label}</span>
      {children}
      {hint ? <span className="text-[11px] leading-snug text-slate-500">{hint}</span> : null}
    </label>
  );
}

export function TextInput(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`${INPUT_CLASS} ${props.className ?? ""}`} />;
}

export function TextArea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={`${INPUT_CLASS} font-mono text-xs leading-relaxed ${props.className ?? ""}`} />;
}

export function Select(props: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={INPUT_CLASS} />;
}

// PrimaryButton 是提交钮（表单 submit 触发）。
export function PrimaryButton(props: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type="submit"
      {...props}
      className={`rounded-md bg-sky-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-sky-600 disabled:opacity-40 ${props.className ?? ""}`}
    />
  );
}

// RowButton 是表格行内动作钮（type=button——F1 修复先例：行内钮恒
// button 型，避免同位换型重提交）。
export function RowButton(props: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      type="button"
      {...props}
      className={`rounded border border-slate-700 px-2 py-0.5 text-xs text-slate-300 hover:border-slate-500 hover:text-slate-100 disabled:opacity-40 ${props.className ?? ""}`}
    />
  );
}

// DangerRowButton 是破坏性动作（原生 confirm 二次确认——删除面最低门槛）。
export function DangerRowButton({ confirm, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { confirm: string }) {
  return (
    <RowButton
      {...props}
      onClick={(event) => {
        if (window.confirm(confirm)) props.onClick?.(event);
      }}
      className={`border-red-900 text-red-300 hover:border-red-700 hover:text-red-200 ${props.className ?? ""}`}
    />
  );
}

// MutationBanner 是写操作的行内结果面：pending 禁用态 + 错误信封渲染 +
// 成功文案（受影响提示如 affected_apps 由调用方拼进 success 文案）。
export function MutationBanner({ pending, error, success }: { pending: boolean; error: unknown; success: string | null }) {
  if (!pending && error == null && success == null) return null;
  return (
    <div className="text-xs">
      {pending ? <span className="text-slate-500">Working…</span> : null}
      {!pending && error != null ? <ErrorNote error={error} /> : null}
      {!pending && error == null && success != null ? (
        <div className="rounded-md border border-emerald-900/60 bg-emerald-950/40 px-3 py-2 font-medium text-emerald-300">{success}</div>
      ) : null}
    </div>
  );
}

// useApiMutation 是写动词的统一封装：apiSend + 受影响查询键失效。
// 失效粒度按资源键（目录页 60s 轮询兜底，写后即时反映优先）。
export function useApiMutation<TResponse>(options: {
  path: string | (() => string);
  method: string;
  body?: () => unknown;
  invalidate?: readonly unknown[][];
}) {
  const queryClient = useQueryClient();
  return useMutation<TResponse, Error, void>({
    mutationFn: () => apiSend<TResponse>(typeof options.path === "function" ? options.path() : options.path, options.method, options.body?.()),
    onSuccess: () => {
      for (const key of options.invalidate ?? []) void queryClient.invalidateQueries({ queryKey: key });
    },
  });
}

// useActionState 是行内动作钮的状态面（写后受影响提示/错误信封渲染的
// 轻量载体——不引 react-hook-form 之类表单库，表单复杂度未到阈值）。
export function useActionState() {
  const [success, setSuccess] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  return { success, error, setSuccess, setError, clear: () => { setSuccess(null); setError(null); } };
}

// TableWrap 是共享表格容器（横向滚动 + 边框）。
export function TableWrap({ children }: { children: ReactNode }) {
  return <div className="overflow-x-auto rounded-lg border border-slate-800">{children}</div>;
}

export function TableHead({ columns }: { columns: readonly string[] }) {
  return (
    <thead>
      <tr className="border-b border-slate-800 bg-slate-900/60 text-left text-xs uppercase tracking-wide text-slate-500">
        {columns.map((column) => (
          <th key={column} className="px-3 py-2 font-medium">
            {column}
          </th>
        ))}
      </tr>
    </thead>
  );
}
