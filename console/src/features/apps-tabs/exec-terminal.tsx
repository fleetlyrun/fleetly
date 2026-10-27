import { useState } from "react";
import { CircleStopIcon, MinusIcon, PlusIcon, PlayIcon } from "lucide-react";
import { useProcessInstances } from "@/kit/exec";
import { useExecSession } from "@/features/terminal/use-exec-session";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// shortInstance 截断编排器实例标识（swarm task ID 64 hex / k3s pod 名——
// 下拉展示面；Connect 后的会话头行回显全值）。
function shortInstance(id: string): string {
  return id.length > 14 ? `${id.slice(0, 12)}…` : id;
}

// Exec 终端核心（IA v3 T2 自全局 /terminal 页抽出，ADR-0049 协议/会话
// 语义零改动；实例锚定 ADR-0067）。批3 订阅族收尾：会话生命周期走
// features/terminal use-exec-session 单源（受理换票据 → WS 流 → xterm
// 接线 → 帧投递 → 断线/退出呈现重开面，语义与平台页并集对齐）；页面层
// 只留差异面——app 语境由 tab 固化，process 由 revisions 的
// process_strategies 给定，instance 由在跑实例集观测给定（auto = 平台代
// 选首个在跑）。会话锚定受理时刻的实例与节点（ADR-0049）：连接期切换
// process/instance 即断旧开新，Disconnect/Cancel 主动收口。
export function ExecTerminal({ appId, processes }: { appId: string; processes: string[] }) {
  const [process, setProcess] = useState("");
  const [instance, setInstance] = useState("auto");
  const [command, setCommand] = useState("");
  const effectiveProcess = process !== "" ? process : (processes[0] ?? "");

  const instances = useProcessInstances(appId, effectiveProcess);
  const instanceRows = instances.data?.rows ?? [];

  const session = useExecSession();

  const argv = command.trim() === "" ? [] : command.trim().split(/\s+/);

  // 进程切换即重置实例锚（实例集是 per-process 观测——跨进程的锚必然不
  // 中）。会话不可迁移：开着会话切换 = 断旧开新（对准新 process、auto 锚）。
  function pickProcess(name: string) {
    setProcess(name);
    setInstance("auto");
    if (session.status === "open" || session.status === "connecting") {
      session.disconnect();
      session.connect({ appId, process: name, argv });
    }
  }

  // 实例切换同理：对准新实例重开（受理即锚定，见 ADR-0067）。
  function pickInstance(id: string) {
    setInstance(id);
    if (session.status === "open" || session.status === "connecting") {
      session.disconnect();
      session.connect({ appId, process: effectiveProcess, argv, instance: id === "auto" ? "" : id });
    }
  }

  return (
    <div className="flex h-[32rem] flex-col">
      <div className="flex flex-none flex-wrap items-end gap-2.5 pb-2.5">
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Process</Label>
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
          <Label className="text-2xs text-muted">Instance</Label>
          <Select value={instance} onValueChange={pickInstance}>
            <SelectTrigger className="w-48 font-mono">
              <SelectValue placeholder="—" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto" className="font-mono text-xs">
                auto <span className="text-muted">(first running)</span>
              </SelectItem>
              {instanceRows.map((row) => {
                const id = row.instance ?? "";
                const node = row.node_id ?? "";
                return (
                  <SelectItem key={id} value={id} className="font-mono text-xs">
                    {shortInstance(id)}
                    {node !== "" ? <span className="ml-1.5 text-muted">@ {shortInstance(node)}</span> : null}
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-1 flex-col gap-1">
          <Label className="text-2xs text-muted">Command (empty = /bin/sh)</Label>
          <Input
            value={command}
            placeholder="/bin/sh"
            onChange={(event) => setCommand(event.target.value)}
            className="h-8 font-mono text-xs"
          />
        </div>
        {session.status === "open" || session.status === "connecting" ? (
          <Button type="button" size="sm" variant="destructive" className="mb-0.5" onClick={session.disconnect}>
            <CircleStopIcon className="size-3" />
            {session.status === "open" ? "Disconnect" : "Cancel"}
          </Button>
        ) : (
          <Button
            type="button"
            size="sm"
            className="mb-0.5"
            disabled={effectiveProcess === ""}
            onClick={() => session.connect({ appId, process: effectiveProcess, argv, instance: instance === "auto" ? "" : instance })}
          >
            <PlayIcon className="size-3" />
            Connect
          </Button>
        )}
        <div className="mb-0.5 flex items-center gap-1">
          <Button variant="outline" size="sm" onClick={() => session.setFontSize((size) => Math.max(10, size - 1))} aria-label="Decrease font size">
            <MinusIcon className="size-3" />
          </Button>
          <Button variant="outline" size="sm" onClick={() => session.setFontSize((size) => Math.min(18, size + 1))} aria-label="Increase font size">
            <PlusIcon className="size-3" />
          </Button>
        </div>
      </div>

      <div ref={session.shellRef} className="relative min-h-0 flex-1 overflow-hidden rounded-lg border bg-bg">
        <div ref={session.termDivRef} className="absolute inset-0 px-2 py-1" />
      </div>

      <div className="flex flex-none items-center gap-4 py-2 font-mono text-2xs text-muted">
        <span
          className={
            session.status === "open"
              ? "text-[var(--status-success)]"
              : session.status === "closed"
                ? "text-[var(--status-warning)]"
                : session.status === "connecting"
                  ? "text-[var(--status-info)]"
                  : ""
          }
        >
          ws: {session.status}
        </span>
        {session.banner !== "" ? (
          <span className="truncate">{session.banner} — the session has ended; press Connect to open a new one.</span>
        ) : null}
        <span className="ml-auto">app {appId}</span>
      </div>
    </div>
  );
}
