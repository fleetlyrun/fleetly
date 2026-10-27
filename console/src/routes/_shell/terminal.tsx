import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useCollection } from "@lynx-go/uikit-core";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import { z } from "zod";
import { MinusIcon, PlusIcon, CircleStopIcon, PlayIcon } from "lucide-react";
import { appsDirectorySearch, appsResource } from "@/kit/apps";
import { projectsResource } from "@/kit/projects";
import { revisionsLookup, revisionsResource } from "@/kit/revisions";
import { useProjectId } from "@/lib/project";
import { useExecSession } from "@/features/terminal/use-exec-session";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// 终端工作台（F3.2/ADR-0049 → UI v2 批 5 reskin → 批3 订阅族收尾）：受理换
// 票据 → WS 流 → xterm 双向接线——会话生命周期走 features/terminal
// use-exec-session 单源（协议/会话语义零改动：断线/退出呈现重开面——会话
// 不迁移，重开 = 新会话新票据，ADR-0049 决策 5；语义取两处消费面并集，
// 含 disconnect 主动收口与切换作废在飞受理）。页面层只留差异面：项目/
// 进程选择 UI + search param 预选 + 字号调节。批 5 增量保留：flex 自适应
// 高度（ResizeObserver 驱动 fit）+ 项目语境来自切换器 + 状态徽章。
// 2026-10-27 批4 读面收尾：项目/App 目录走 kit 资源（选择器非列表语义，
// 目录级大页直接吃 rows，60s 目录轮询照 catalog 原值）。
const terminalSearch = z.object({ app: z.string().optional(), process: z.string().optional() });

// 目录级大页（纯目录拉取：窗口 ≥ 取数封顶，目录不截断）。
const projectsDirectorySearch = collectionQuerySchema({ defaultSize: 200 });

export const Route = createFileRoute("/_shell/terminal")({
  validateSearch: terminalSearch,
  component: TerminalWorkbench,
});

function TerminalWorkbench() {
  const search = Route.useSearch();
  const [projectId] = useProjectId();
  const projects = useCollection(projectsResource, { from: Route.id, search: projectsDirectorySearch, query: { refetchInterval: 60_000 } });
  const apps = useCollection(appsResource, {
    from: Route.id,
    search: appsDirectorySearch,
    scope: { projectId: projectId || undefined },
    query: { refetchInterval: 60_000 },
  });
  const [appId, setAppId] = useState(search.app ?? "");
  const [process, setProcess] = useState(search.process ?? "");
  const [command, setCommand] = useState("");

  const effectiveApp = appId !== "" ? appId : (apps.rows[0]?.id ?? "");
  // 冻结历史 join 读（进程下拉数据源）：appId 显式 scope（资源声明
  // projectId/appId 双键，未就绪自动停查），15s 轮询照 catalog 原值。
  const revisions = useCollection(revisionsResource, {
    from: Route.id,
    search: revisionsLookup,
    scope: { projectId: projectId || undefined, appId: effectiveApp || undefined },
    query: { refetchInterval: 15_000 },
  });
  const processes = useMemo(
    () => (revisions.rows[0]?.process_strategies ?? []).map((entry) => entry?.process ?? "").filter((name) => name !== ""),
    [revisions.rows],
  );
  const effectiveProcess = process !== "" ? process : (processes[0] ?? "");

  const session = useExecSession();

  const argv = command.trim() === "" ? [] : command.trim().split(/\s+/);

  // 进程切换即对准新目标重开（会话锚定受理时刻的实例与节点，ADR-0049/0067
  // ——开着会话换目标 = 断旧开新，auto 锚）；app 切换语境整体换血，进程集
  // 随之未知，只断旧呈现重开面（用户对新 app 显式 Connect）。
  function pickApp(value: string) {
    setAppId(value);
    setProcess("");
    if (session.status === "open" || session.status === "connecting") session.disconnect();
  }

  function pickProcess(name: string) {
    setProcess(name);
    if (name !== "" && (session.status === "open" || session.status === "connecting")) {
      session.disconnect();
      session.connect({ appId: effectiveApp, process: name, argv });
    }
  }

  return (
    <div className="flex h-full flex-col px-6 pt-6">
      <div className="mb-1 flex items-baseline gap-3">
        <h1 className="font-heading text-lg font-bold tracking-tight">Terminal</h1>
        <span className="text-xs text-muted">Exec session — one-shot ticket over WebSocket</span>
      </div>

      <div className="flex flex-none flex-wrap items-end gap-2.5 py-3">
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Project</Label>
          <Select value={projectId} onValueChange={() => undefined} disabled>
            <SelectTrigger className="w-40">
              <SelectValue placeholder={projects.rows.find((project) => project.id === projectId)?.name ?? "—"} />
            </SelectTrigger>
            <SelectContent>
              {projects.rows.map((project) => (
                <SelectItem key={project.id} value={project.id}>
                  {project.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">App</Label>
          <Select value={effectiveApp} onValueChange={pickApp}>
            <SelectTrigger className="w-44">
              <SelectValue placeholder="select an app…" />
            </SelectTrigger>
            <SelectContent>
              {apps.rows.map((app) => (
                <SelectItem key={app.id} value={app.id}>
                  {app.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
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
            disabled={effectiveApp === "" || effectiveProcess === ""}
            onClick={() => session.connect({ appId: effectiveApp, process: effectiveProcess, argv })}
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
        <span className="ml-auto">POST /v1/exec/sessions → /v1/exec/stream</span>
      </div>
      <p className="flex-none pb-3 text-2xs leading-relaxed text-muted">
        Sessions are interactive TTYs into a running replica (platform picks the first running instance and reports it in the session
        header). Change freeze does not cover exec — it is a diagnostics face (ADR-0049).
      </p>
    </div>
  );
}
