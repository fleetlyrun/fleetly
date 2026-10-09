import { useEffect, useMemo, useRef, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { MinusIcon, PlusIcon, PlayIcon } from "lucide-react";
import { useApps, useProjects, useRevisions } from "@/lib/catalog";
import { useProjectId } from "@/lib/project";
import { createExecSession, openExecStream, sendResize, sendStdin } from "@/api/execstream";
import { ApiError } from "@/api/client";
import type { ServerFrame } from "@/lib/execFrames";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// 终端工作台（F3.2/ADR-0049 → UI v2 批 5 reskin）：受理换票据 → WS 流 →
// xterm 双向接线（协议/会话语义零改动：断线/退出呈现重开面——会话不迁移，
// 重开 = 新会话新票据，ADR-0049 决策 5）。批 5 增量：flex 自适应高度
//（ResizeObserver 驱动 fit）+ 项目语境来自切换器 + search param 预选 +
// 状态徽章 + 字号调节。
const terminalSearch = z.object({ app: z.string().optional(), process: z.string().optional() });

export const Route = createFileRoute("/_shell/terminal")({
  validateSearch: terminalSearch,
  component: TerminalWorkbench,
});

type Status = "idle" | "connecting" | "open" | "closed";

function TerminalWorkbench() {
  const search = Route.useSearch();
  const [projectId] = useProjectId();
  const projects = useProjects();
  const apps = useApps(projectId);
  const [appId, setAppId] = useState(search.app ?? "");
  const [process, setProcess] = useState(search.process ?? "");
  const [command, setCommand] = useState("");
  const [status, setStatus] = useState<Status>("idle");
  const [banner, setBanner] = useState("");
  const [fontSize, setFontSize] = useState(13);
  const revisions = useRevisions(appId !== "" ? appId : (apps.data?.[0]?.id ?? ""));

  const effectiveApp = appId !== "" ? appId : (apps.data?.[0]?.id ?? "");
  const processes = useMemo(
    () => (revisions.data?.[0]?.process_strategies ?? []).map((entry) => entry?.process ?? "").filter((name) => name !== ""),
    [revisions.data],
  );
  const effectiveProcess = process !== "" ? process : (processes[0] ?? "");

  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const termDiv = useRef<HTMLDivElement | null>(null);
  const shellRef = useRef<HTMLDivElement | null>(null);
  const statusRef = useRef(status);
  statusRef.current = status;

  // xterm 挂载（一次）：输出写入 + 输入上行 + resize 双向。批 5：
  // ResizeObserver 观察容器（flex 自适应高度，不再写死 28rem）。
  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      scrollback: 5000,
      theme: { background: "oklch(0.135 0.015 285)" },
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
    const observer = new ResizeObserver(() => fitRef.current?.fit());
    if (shellRef.current !== null) observer.observe(shellRef.current);
    return () => {
      observer.disconnect();
      wsRef.current?.close();
      wsRef.current = null;
      term.dispose();
      termRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (termRef.current != null) termRef.current.options.fontSize = fontSize;
    fitRef.current?.fit();
  }, [fontSize]);

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
    <div className="flex h-full flex-col px-5 pt-4">
      <div className="mb-1 flex items-baseline gap-3">
        <h1 className="font-heading text-[17px] font-bold tracking-tight">Terminal</h1>
        <span className="text-xs text-muted-foreground">Exec session — one-shot ticket over WebSocket</span>
      </div>

      <div className="flex flex-none flex-wrap items-end gap-2.5 py-3">
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Project</Label>
          <Select value={projectId} onValueChange={() => undefined} disabled>
            <SelectTrigger className="w-40">
              <SelectValue placeholder={projects.data?.find((project) => project.id === projectId)?.name ?? "—"} />
            </SelectTrigger>
            <SelectContent>
              {(projects.data ?? []).map((project) => (
                <SelectItem key={project.id} value={project.id}>
                  {project.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">App</Label>
          <Select
            value={effectiveApp}
            onValueChange={(value) => {
              setAppId(value);
              setProcess("");
            }}
          >
            <SelectTrigger className="w-44">
              <SelectValue placeholder="select an app…" />
            </SelectTrigger>
            <SelectContent>
              {(apps.data ?? []).map((app) => (
                <SelectItem key={app.id} value={app.id}>
                  {app.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Process</Label>
          <Select value={effectiveProcess} onValueChange={setProcess}>
            <SelectTrigger className="w-32">
              <SelectValue placeholder={effectiveProcess === "" ? "—" : ""} />
            </SelectTrigger>
            <SelectContent>
              {processes.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-1 flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Command (empty = /bin/sh)</Label>
          <Input
            value={command}
            placeholder="/bin/sh"
            onChange={(event) => setCommand(event.target.value)}
            className="h-8 font-mono text-xs"
          />
        </div>
        <Button
          type="button"
          size="sm"
          className="mb-0.5"
          disabled={status === "connecting" || status === "open" || effectiveApp === "" || effectiveProcess === ""}
          onClick={connect}
        >
          <PlayIcon className="size-3" />
          {status === "open" ? "Connected" : status === "connecting" ? "Connecting…" : "Connect"}
        </Button>
        <div className="mb-0.5 flex items-center gap-1">
          <Button variant="outline" size="sm" onClick={() => setFontSize((size) => Math.max(10, size - 1))} aria-label="Decrease font size">
            <MinusIcon className="size-3" />
          </Button>
          <Button variant="outline" size="sm" onClick={() => setFontSize((size) => Math.min(18, size + 1))} aria-label="Increase font size">
            <PlusIcon className="size-3" />
          </Button>
        </div>
      </div>

      <div ref={shellRef} className="relative min-h-0 flex-1 overflow-hidden rounded-lg border bg-[oklch(0.135_0.015_285)]">
        <div ref={termDiv} className="absolute inset-0 px-2 py-1" />
      </div>

      <div className="flex flex-none items-center gap-4 py-2 font-mono text-[11px] text-muted-foreground">
        <span
          className={
            status === "open"
              ? "text-[var(--status-success)]"
              : status === "closed"
                ? "text-[var(--status-warning)]"
                : status === "connecting"
                  ? "text-[var(--status-info)]"
                  : ""
          }
        >
          ws: {status}
        </span>
        {banner !== "" ? <span className="truncate">{banner} — the session has ended; press Connect to open a new one.</span> : null}
        <span className="ml-auto">POST /v1/exec/sessions → /v1/exec/stream</span>
      </div>
      <p className="flex-none pb-3 text-[11px] leading-relaxed text-muted-foreground">
        Sessions are interactive TTYs into a running replica (platform picks the first running instance and reports it in the session
        header). Change freeze does not cover exec — it is a diagnostics face (ADR-0049).
      </p>
    </div>
  );
}
