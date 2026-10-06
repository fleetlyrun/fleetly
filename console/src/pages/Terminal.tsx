import { useEffect, useMemo, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useApps, useProjects, useRevisions } from "../lib/catalog";
import { createExecSession, openExecStream, sendResize, sendStdin } from "../api/execstream";
import { ApiError } from "../api/client";
import type { ServerFrame } from "../lib/execFrames";

// 终端页（F3.2，ADR-0049）：xterm.js 首引（ADR-0044 预留点）。受理换票据
// → WS 流 → xterm 双向接线；断线/退出呈现重开面（会话不迁移——重开 =
// 新会话新票据，ADR-0049 决策 5）。进程选择 = App 目录 + 最新 Revision 的
// process_strategies 全进程目录。

type Status = "idle" | "connecting" | "open" | "closed";

export function TerminalPage() {
  const [projectId, setProjectId] = useState("");
  const [appId, setAppId] = useState("");
  const [process, setProcess] = useState("");
  const [command, setCommand] = useState("");
  const [status, setStatus] = useState<Status>("idle");
  const [banner, setBanner] = useState("");
  const projects = useProjects();
  const apps = useApps(projectId);
  const revisions = useRevisions(appId);

  const effectiveProject = projectId || (projects.data?.[0]?.id ?? "");
  const effectiveApp = appId || (apps.data?.[0]?.id ?? "");
  const processes = useMemo(
    () => (revisions.data?.[0]?.process_strategies ?? []).map((entry) => entry?.process ?? "").filter((name) => name !== ""),
    [revisions.data],
  );
  const effectiveProcess = process || (processes[0] ?? "");

  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const termDiv = useRef<HTMLDivElement | null>(null);

  // xterm 挂载（一次）：输出写入 + 输入上行 + resize 双向（fit 组件驱动
  // 尺寸事件 → resize 帧；resize 帧是尽力语义——服务端转发到载体 PTY）。
  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      scrollback: 5000,
      theme: { background: "#020617" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    if (termDiv.current !== null) {
      term.open(termDiv.current);
      fit.fit();
    }
    term.onData((data) => sendStdin(wsRef.current, data));
    term.onResize(({ cols, rows }) => sendResize(wsRef.current, cols, rows));
    termRef.current = term;
    fitRef.current = fit;
    const onWinResize = () => fitRef.current?.fit();
    window.addEventListener("resize", onWinResize);
    return () => {
      window.removeEventListener("resize", onWinResize);
      wsRef.current?.close();
      wsRef.current = null;
      term.dispose();
      termRef.current = null;
    };
  }, []);

  function connect() {
    if (effectiveApp === "" || effectiveProcess === "" || termRef.current === null) return;
    const term = termRef.current;
    term.reset();
    setBanner("");
    setStatus("connecting");
    const argv = command.trim() === "" ? [] : command.trim().split(/\s+/);
    createExecSession(effectiveApp, effectiveProcess, argv, true)
      .then((created) => {
        const session = created.session;
        if (session?.id === "" || session?.id == null || created.ticket == null || created.ticket === "") {
          throw new ApiError(0, "no_session", "session acceptance returned no ticket");
        }
        term.writeln(`\x1b[90m# session ${session.id} on ${effectiveApp}/${effectiveProcess} (instance ${session.instance ?? "?"}, node ${session.node_id ?? "?"})\x1b[0m`);
        const ws = openExecStream(session.id, created.ticket, {
          onFrame: (frame: ServerFrame) => deliver(term, frame),
          onClose: () => {
            setStatus((cur) => (cur === "open" ? "closed" : cur));
          },
        });
        ws.onopen = () => {
          wsRef.current = ws;
          setStatus("open");
          fitRef.current?.fit();
        };
        wsRef.current = ws;
      })
      .catch((err: unknown) => {
        const message = err instanceof ApiError ? `${err.code}: ${err.message}` : String(err);
        term.writeln(`\x1b[31m${message}\x1b[0m`);
        setBanner(message);
        setStatus("idle");
      });
  }

  function deliver(term: Terminal, frame: ServerFrame) {
    switch (frame.kind) {
      case "stdout":
        term.write(frame.data);
        return;
      case "stderr":
        term.write(frame.data);
        return;
      case "exit":
        term.writeln(`\x1b[90m# exited with code ${frame.exit.code}\x1b[0m`);
        setBanner(`exited with code ${frame.exit.code}`);
        setStatus("closed");
        return;
      case "error":
        term.writeln(`\x1b[31m# ${frame.error.code}: ${frame.error.message}\x1b[0m`);
        setBanner(`${frame.error.code}: ${frame.error.message}`);
        setStatus("closed");
        return;
      case "meta":
        return;
    }
  }

  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs text-slate-400">
          Project
          <select
            className="rounded border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-100"
            value={effectiveProject}
            onChange={(e) => {
              setProjectId(e.target.value);
              setAppId("");
              setProcess("");
            }}
          >
            {(projects.data ?? []).map((p) => (
              <option key={p.id} value={p.id ?? ""}>
                {p.name ?? p.id}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-400">
          App
          <select
            className="rounded border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-100"
            value={effectiveApp}
            onChange={(e) => {
              setAppId(e.target.value);
              setProcess("");
            }}
          >
            {(apps.data ?? []).map((a) => (
              <option key={a.id} value={a.id ?? ""}>
                {a.name ?? a.id}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-xs text-slate-400">
          Process
          <select
            className="rounded border border-slate-700 bg-slate-900 px-2 py-1.5 text-sm text-slate-100"
            value={effectiveProcess}
            onChange={(e) => setProcess(e.target.value)}
          >
            {processes.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-1 flex-col gap-1 text-xs text-slate-400">
          Command (empty = /bin/sh)
          <input
            className="rounded border border-slate-700 bg-slate-900 px-2 py-1.5 font-mono text-sm text-slate-100"
            value={command}
            placeholder="/bin/sh"
            onChange={(e) => setCommand(e.target.value)}
          />
        </label>
        <button
          type="button"
          className="rounded bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
          disabled={status === "connecting" || status === "open" || effectiveApp === "" || effectiveProcess === ""}
          onClick={connect}
        >
          {status === "open" ? "Connected" : status === "connecting" ? "Connecting…" : "Connect"}
        </button>
      </div>
      {status === "closed" && banner !== "" ? (
        <p className="rounded border border-slate-700 bg-slate-900 px-3 py-2 text-xs text-slate-300">
          {banner} — <span className="text-slate-400">the session has ended; press Connect to open a new one.</span>
        </p>
      ) : null}
      <div className="overflow-hidden rounded border border-slate-800 bg-slate-950">
        <div ref={termDiv} className="h-[28rem] px-2 py-1" />
      </div>
      <p className="text-xs text-slate-500">
        Sessions are interactive TTYs into a running replica (platform picks the first running instance and reports it in
        the session header). Change freeze does not cover exec — it is a diagnostics face (ADR-0049).
      </p>
    </section>
  );
}
