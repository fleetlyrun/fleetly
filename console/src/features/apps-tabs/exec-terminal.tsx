import { useEffect, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { CircleStopIcon, MinusIcon, PlusIcon, PlayIcon } from "lucide-react";
import { createExecSession, openExecStream, sendResize, sendStdin } from "@/api/execstream";
import { ApiError } from "@/api/client";
import { useProcessInstances } from "@/lib/catalog";
import type { ServerFrame } from "@/lib/execFrames";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type Status = "idle" | "connecting" | "open" | "closed";

// shortInstance 截断编排器实例标识（swarm task ID 64 hex / k3s pod 名——
// 下拉展示面；Connect 后的会话头行回显全值）。
function shortInstance(id: string): string {
  return id.length > 14 ? `${id.slice(0, 12)}…` : id;
}

// Exec 终端核心（IA v3 T2 自全局 /terminal 页抽出，ADR-0049 协议/会话
// 语义零改动；实例锚定 ADR-0067）：受理换票据 → WS 流 → xterm 双向接线；
// 断线/退出呈现重开面（会话不迁移，重开 = 新会话新票据）。app 语境由
// tab 固化，process 由 revisions 的 process_strategies 给定，instance 由
// 在跑实例集观测给定（auto = 平台代选首个在跑）。会话锚定受理时刻的
// 实例与节点（ADR-0049）：连接期切换 process/instance 即断旧开新，
// Disconnect/Cancel 主动收口——旧流的收口不迁移状态（wsRef 身份比对）。
export function ExecTerminal({ appId, processes }: { appId: string; processes: string[] }) {
  const [process, setProcess] = useState("");
  const [instance, setInstance] = useState("auto");
  const [command, setCommand] = useState("");
  const [status, setStatus] = useState<Status>("idle");
  const [banner, setBanner] = useState("");
  const [fontSize, setFontSize] = useState(13);
  const effectiveProcess = process !== "" ? process : (processes[0] ?? "");

  const instances = useProcessInstances(appId, effectiveProcess);
  const instanceRows = instances.data?.instances ?? [];

  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  // connectSeq 作废在飞受理：切换/断开时递增序号，旧受理的 .then 见序号
  // 落后即弃置（弃置会话的票据秒级 TTL 自然过期——客户端无从撤销受理）。
  const connectSeqRef = useRef(0);
  const termDiv = useRef<HTMLDivElement | null>(null);
  const shellRef = useRef<HTMLDivElement | null>(null);

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

  // disconnect 主动收口当前会话（旧流 onClose 静默——状态迁移只认现役流）。
  function disconnect() {
    connectSeqRef.current += 1;
    const ws = wsRef.current;
    wsRef.current = null;
    if (ws != null && ws.readyState !== WebSocket.CLOSED) ws.close();
    setStatus("closed");
    setBanner("disconnected");
    termRef.current?.writeln("\x1b[90m# disconnected\x1b[0m");
  }

  // connect 以显式目标受理（state 在异步闭包里会滞后——切换重连路径必须
  // 传新值）。
  function connect(targetProcess: string, targetInstance: string) {
    if (appId === "" || targetProcess === "" || termRef.current === null) return;
    const seq = ++connectSeqRef.current;
    const term = termRef.current;
    term.reset();
    setBanner("");
    setStatus("connecting");
    const argv = command.trim() === "" ? [] : command.trim().split(/\s+/);
    // "auto" 哨兵 → 空 instance（平台代选首个在跑——Radix Select 不收空值）。
    createExecSession(appId, targetProcess, argv, true, targetInstance === "auto" ? "" : targetInstance)
      .then((created) => {
        if (seq !== connectSeqRef.current) return; // 已被后续切换/断开取代
        const session = created.session;
        if (session?.id === "" || session?.id == null || created.ticket == null || created.ticket === "") {
          throw new ApiError(0, "no_session", "session acceptance returned no ticket");
        }
        term.writeln(`\x1b[90m# session ${session.id} on ${appId}/${targetProcess} (instance ${session.instance ?? "?"}, node ${session.node_id ?? "?"})\x1b[0m`);
        const ws = openExecStream(session.id, created.ticket, {
          onFrame: (frame: ServerFrame) => deliver(term, frame),
          onClose: () => {
            if (wsRef.current === ws) setStatus((cur) => (cur === "open" ? "closed" : cur));
          },
        });
        ws.onopen = () => {
          if (wsRef.current !== ws) return;
          setStatus("open");
          fitRef.current?.fit();
        };
        wsRef.current = ws;
      })
      .catch((err: unknown) => {
        if (seq !== connectSeqRef.current) return;
        const message = err instanceof ApiError ? `${err.code}: ${err.message}` : String(err);
        term.writeln(`\x1b[31m${message}\x1b[0m`);
        setBanner(message);
        setStatus("idle");
      });
  }

  // 进程切换即重置实例锚（实例集是 per-process 观测——跨进程的锚必然不中）。
  // 会话不可迁移：开着会话切换 = 断旧开新（对准新 process、auto 锚）。
  function pickProcess(name: string) {
    setProcess(name);
    setInstance("auto");
    if (status === "open" || status === "connecting") {
      disconnect();
      connect(name, "auto");
    }
  }

  // 实例切换同理：对准新实例重开（受理即锚定，见 ADR-0067）。
  function pickInstance(id: string) {
    setInstance(id);
    if (status === "open" || status === "connecting") {
      disconnect();
      connect(effectiveProcess, id);
    }
  }

  return (
    <div className="flex h-[32rem] flex-col">
      <div className="flex flex-none flex-wrap items-end gap-2.5 pb-2.5">
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Process</Label>
          <Select value={effectiveProcess} onValueChange={pickProcess}>
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
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Instance</Label>
          <Select value={instance} onValueChange={pickInstance}>
            <SelectTrigger className="w-48 font-mono">
              <SelectValue placeholder="—" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto" className="font-mono text-xs">
                auto <span className="text-muted-foreground">(first running)</span>
              </SelectItem>
              {instanceRows.map((row) => {
                const id = row.instance ?? "";
                const node = row.node_id ?? "";
                return (
                  <SelectItem key={id} value={id} className="font-mono text-xs">
                    {shortInstance(id)}
                    {node !== "" ? <span className="ml-1.5 text-muted-foreground">@ {shortInstance(node)}</span> : null}
                  </SelectItem>
                );
              })}
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
        {status === "open" || status === "connecting" ? (
          <Button type="button" size="sm" variant="destructive" className="mb-0.5" onClick={disconnect}>
            <CircleStopIcon className="size-3" />
            {status === "open" ? "Disconnect" : "Cancel"}
          </Button>
        ) : (
          <Button
            type="button"
            size="sm"
            className="mb-0.5"
            disabled={effectiveProcess === ""}
            onClick={() => connect(effectiveProcess, instance)}
          >
            <PlayIcon className="size-3" />
            Connect
          </Button>
        )}
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
        <span className="ml-auto">app {appId}</span>
      </div>
    </div>
  );
}
