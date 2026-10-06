import { useEffect, useRef, useState } from "react";
import type { LogFrame } from "../api/streams";
import { streamLogs } from "../api/streams";
import { useApps, useProjects } from "../lib/catalog";
import { ErrorNote, PageShell } from "../components/ui";

// 日志页（F2.6/ADR-0044 决策 1）：GET /v1/logs 的 NDJSON 帧流（fetch 带
// Bearer 头）。过滤器在流启动时定格（app/process/tail/text/follow）；
// follow 形态下 Stop 中断、改参重启。line 是 protojson bytes（base64）。

const MAX_FRAMES = 5_000;

interface LogControls {
  appId: string;
  process: string;
  tailLines: string;
  text: string;
  follow: boolean;
}

function decodeLine(b64: string | undefined): string {
  if (b64 === undefined) return "";
  // protojson bytes = base64；经 Uint8Array + TextDecoder 保 UTF-8 行体。
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

export function LogsPage() {
  const projects = useProjects();
  const [projectId, setProjectId] = useState("");
  const apps = useApps(projectId);
  const [controls, setControls] = useState<LogControls>({
    appId: "",
    process: "",
    tailLines: "200",
    text: "",
    follow: true,
  });
  const [frames, setFrames] = useState<LogFrame[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [streaming, setStreaming] = useState(false);
  // ended = 最近一次运行已自然收流（服务端关流/查询完），区别于手动 Stop
  // 与未启动——空结果时的提示与计数后缀都靠它区分（F3）。
  const [ended, setEnded] = useState(false);
  const [autoScroll, setAutoScroll] = useState(true);
  const abortRef = useRef<AbortController | null>(null);
  const tailRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (autoScroll) tailRef.current?.scrollIntoView({ block: "end" });
  }, [frames, autoScroll]);

  useEffect(() => () => abortRef.current?.abort(), []);

  function start() {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setFrames([]);
    setError(null);
    setStreaming(true);
    setEnded(false);
    const query = new URLSearchParams();
    if (controls.appId !== "") query.set("app_id", controls.appId);
    if (controls.process !== "") query.set("process", controls.process);
    if (controls.tailLines !== "") query.set("tail_lines", controls.tailLines);
    if (controls.text !== "") query.set("text", controls.text);
    query.set("follow", controls.follow ? "1" : "0");
    streamLogs(query, controller.signal, (frame) => {
      setFrames((prev) => {
        const next = [...prev, frame];
        return next.length > MAX_FRAMES ? next.slice(next.length - MAX_FRAMES) : next;
      });
    })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err);
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setStreaming(false);
          setEnded(true);
        }
      });
  }

  function stop() {
    abortRef.current?.abort();
    setStreaming(false);
  }

  const noApp = controls.appId === "";
  return (
    <PageShell title="Logs" hint="GET /v1/logs — runtime live path, or full-text search when a filter is set">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(event) => {
          event.preventDefault();
          if (!streaming && !noApp) start();
        }}
      >
        {/* 隐形 submit 锚点：保住文本框 Enter 的隐式提交（无 submit 按钮的
            多输入表单按规范不触发）；不可点、不参与视觉。 */}
        <button type="submit" hidden tabIndex={-1} aria-hidden="true" />
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          Project
          <select
            value={projectId}
            onChange={(event) => {
              setProjectId(event.target.value);
              setControls((prev) => ({ ...prev, appId: "" }));
            }}
            className="rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-200"
          >
            {/* ListApps 契约 project_id 必填（F4）：App 目录按项目取，首项
              是引导语义而非"全部"。 */}
            <option value="">select a project…</option>
            {(projects.data ?? []).map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          App
          <select
            value={controls.appId}
            onChange={(event) => setControls((prev) => ({ ...prev, appId: event.target.value }))}
            disabled={projectId === ""}
            className="w-44 rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-200 disabled:opacity-50"
          >
            <option value="">select an app…</option>
            {(apps.data ?? []).map((app) => (
              <option key={app.id} value={app.id}>
                {app.name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          Process
          <input
            value={controls.process}
            onChange={(event) => setControls((prev) => ({ ...prev, process: event.target.value }))}
            placeholder="web"
            spellCheck={false}
            className="w-24 rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 font-mono text-sm text-slate-200 placeholder:text-slate-600"
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          Tail lines
          <input
            value={controls.tailLines}
            onChange={(event) => setControls((prev) => ({ ...prev, tailLines: event.target.value.replace(/[^0-9]/g, "") }))}
            inputMode="numeric"
            className="w-20 rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 font-mono text-sm text-slate-200"
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-500">
          Text filter
          <input
            value={controls.text}
            onChange={(event) => setControls((prev) => ({ ...prev, text: event.target.value }))}
            placeholder="search text (switches to stored path)"
            spellCheck={false}
            className="w-64 rounded-md border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-200 placeholder:text-slate-600"
          />
        </label>
        <label className="flex items-center gap-1.5 pb-2 text-xs text-slate-400">
          <input
            type="checkbox"
            checked={controls.follow}
            onChange={(event) => setControls((prev) => ({ ...prev, follow: event.target.checked }))}
            className="size-3.5 accent-sky-600"
          />
          Follow
        </label>
        {/* 单一稳定按钮（F1 修复，2026-10-05 走查）：Stop/Start 若为同位
            换型的两个按钮，mousedown 的 Stop 会把 DOM 原地改成 submit 型
            Start，同一按压的 click 即触发 form 重提交——Stop 变成"停了
            又立刻重启"。恒为 type=button 的同一节点，点击语义随 streaming
            分派，换型面不存在。 */}
        <button
          type="button"
          onClick={() => (streaming ? stop() : start())}
          disabled={!streaming && noApp}
          title={streaming ? undefined : noApp ? "select an app first" : undefined}
          className={
            streaming
              ? "rounded-md border border-red-800 bg-red-950/50 px-3 py-1.5 text-sm font-medium text-red-300"
              : "rounded-md border border-sky-700 bg-sky-900/40 px-3 py-1.5 text-sm font-medium text-sky-300 disabled:opacity-40"
          }
        >
          {streaming ? "Stop" : "Start"}
        </button>
      </form>

      {error ? <ErrorNote error={error} hint="GET /v1/logs failed — check the API token and the app selection." /> : null}

      <div className="max-h-[70vh] min-h-48 overflow-auto rounded-lg border border-slate-800 bg-slate-950 p-2">
        {frames.length === 0 ? (
          <div className="p-6 text-center text-sm text-slate-600">
            {streaming
              ? "Waiting for frames…"
              : ended
                ? "Stream ended — no frames matched (check the process name and filter)."
                : "Pick an app and press Start."}
          </div>
        ) : (
          <table className="w-full border-separate border-spacing-0 font-mono text-xs">
            <tbody>
              {frames.map((frame, index) => (
                <tr key={index} className="align-top">
                  <td className="whitespace-nowrap pr-3 text-slate-600">{frame.time ?? ""}</td>
                  <td className="whitespace-nowrap pr-3 text-slate-500">
                    {[frame.node, frame.container?.slice(0, 12)].filter(Boolean).join(" ") || frame.workload_id?.slice(0, 12) || ""}
                  </td>
                  <td className="whitespace-pre-wrap break-all text-slate-300">{decodeLine(frame.line)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <div ref={tailRef} />
      </div>
      <div className="flex items-center gap-3 text-xs text-slate-500">
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={autoScroll}
            onChange={(event) => setAutoScroll(event.target.checked)}
            className="size-3.5 accent-sky-600"
          />
          Auto-scroll
        </label>
        <span>
          {frames.length} frame{frames.length === 1 ? "" : "s"}
          {streaming ? " · streaming" : ended ? " · ended" : ""}
          {frames.length >= MAX_FRAMES ? " · buffer capped (oldest dropped)" : ""}
        </span>
      </div>
    </PageShell>
  );
}
