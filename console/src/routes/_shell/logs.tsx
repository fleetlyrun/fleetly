import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";
import { DownloadIcon, PlayIcon, SquareIcon, XIcon } from "lucide-react";
import { useApps, useProjects } from "@/lib/catalog";
import { useProjectId } from "@/lib/project";
import { decodeLine, useLogStream } from "@/features/logs/use-log-stream";
import { LogViewer } from "@/features/logs/log-viewer";
import { ErrorState } from "@/components/domain/error-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

// 日志工作台（Workbench 原型，UI v2 批 3）：GET /v1/logs 的 NDJSON 帧流
// （协议只换壳不换契约）。筛选走类型化 search params（URL 即状态——可分享
// 可回退）；虚拟滚动 + 智能跟随在 features/logs。

const logsSearch = z.object({
  app: z.string().optional(),
  process: z.string().optional(),
  text: z.string().optional(),
  tail: z.string().optional(),
});

export const Route = createFileRoute("/_shell/logs")({
  validateSearch: logsSearch,
  component: LogsWorkbench,
});

function LogsWorkbench() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const [projectId] = useProjectId();
  const projects = useProjects();
  const apps = useApps(projectId);
  const [appId, setAppId] = useState(search.app ?? "");
  const [process, setProcess] = useState(search.process ?? "");
  const [tailLines, setTailLines] = useState(search.tail ?? "200");
  const [text, setText] = useState(search.text ?? "");
  const [follow, setFollow] = useState(true);
  const log = useLogStream();

  // 项目目录到达后补默认 App（切换器语境优先，search 参数其次）
  useEffect(() => {
    if (appId === "" && search.app === undefined && (apps.data ?? []).length > 0) {
      setAppId(apps.data![0].id);
    }
  }, [apps.data, appId, search.app]);

  // 已选 App 不在当前项目目录（切了项目）→ 重置
  useEffect(() => {
    if (appId !== "" && (apps.data ?? []).length > 0 && !apps.data!.some((entry) => entry.id === appId)) {
      setAppId(apps.data![0].id);
    }
  }, [apps.data, appId]);

  function start() {
    if (appId === "") return;
    log.start({ appId, process, tailLines, text, follow });
    void navigate({
      search: { app: appId, process: process || undefined, text: text || undefined, tail: tailLines !== "200" ? tailLines : undefined },
    });
  }

  function download() {
    const body = log.frames
      .map((frame) => `${frame.time ?? ""}\t${[frame.node, frame.container?.slice(0, 12)].filter(Boolean).join(" ")}\t${decodeLine(frame.line)}`)
      .join("\n");
    const blob = new Blob([body], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `fleetly-logs-${new Date().toISOString().slice(0, 19).replaceAll(":", "")}.txt`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  const noApp = appId === "";

  return (
    <div className="flex h-full flex-col px-5 pt-4">
      <div className="mb-1 flex items-baseline gap-3">
        <h1 className="font-heading text-[17px] font-bold tracking-tight">Logs</h1>
        <span className="text-xs text-muted-foreground">
          {projects.data?.find((project) => project.id === projectId)?.name ?? "—"} — live stream or stored search
        </span>
      </div>

      <div className="flex flex-none flex-wrap items-end gap-2.5 py-3">
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">App</Label>
          <Select value={appId} onValueChange={setAppId}>
            <SelectTrigger className="w-44">
              <SelectValue placeholder={noApp ? "select an app…" : ""} />
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
          <Input value={process} onChange={(event) => setProcess(event.target.value)} placeholder="web" className="h-8 w-24 font-mono text-xs" />
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Tail</Label>
          <Input
            value={tailLines}
            onChange={(event) => setTailLines(event.target.value.replace(/[^0-9]/g, ""))}
            inputMode="numeric"
            className="h-8 w-16 font-mono text-xs"
          />
        </div>
        <div className="flex flex-1 flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Text filter</Label>
          <Input
            value={text}
            onChange={(event) => setText(event.target.value)}
            placeholder="search text — switches to the stored path"
            className="h-8 text-xs"
            onKeyDown={(event) => {
              if (event.key === "Enter" && !log.streaming && !noApp) start();
            }}
          />
        </div>
        <Label className="mb-1.5 flex items-center gap-2 text-xs text-muted-foreground">
          <Switch checked={follow} onCheckedChange={setFollow} />
          Follow
        </Label>
        {/* 单一稳定按钮（F1 走查先例：恒 type=button 同一节点，Stop/Start
            同位换型的重提交面不存在——语义随 streaming 分派）。 */}
        <Button
          type="button"
          size="sm"
          variant={log.streaming ? "destructive" : "default"}
          disabled={!log.streaming && noApp}
          title={log.streaming ? undefined : noApp ? "select an app first" : undefined}
          onClick={() => (log.streaming ? log.stop() : start())}
          className="mb-0.5"
        >
          {log.streaming ? <SquareIcon className="size-3" /> : <PlayIcon className="size-3" />}
          {log.streaming ? "Stop" : "Start"}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={log.frames.length === 0} onClick={download} className="mb-0.5">
          <DownloadIcon className="size-3" />
          Download
        </Button>
        <Button type="button" size="sm" variant="ghost" disabled={log.frames.length === 0} onClick={log.clear} className="mb-0.5">
          <XIcon className="size-3" />
          Clear
        </Button>
      </div>

      {log.error ? (
        <div className="pb-2">
          <ErrorState error={log.error} onRetry={start} />
        </div>
      ) : null}

      <LogViewer frames={log.frames} streaming={log.streaming} />

      <div className="flex flex-none items-center gap-4 py-2 font-mono text-[11px] text-muted-foreground">
        <span>
          {log.frames.length} frame{log.frames.length === 1 ? "" : "s"}
          {log.streaming ? " · streaming" : log.ended ? " · ended" : ""}
          {log.frames.length >= log.maxFrames ? " · buffer capped (oldest dropped)" : ""}
        </span>
        <span className="ml-auto">GET /v1/logs · {text !== "" ? "stored path" : "runtime live path"}</span>
      </div>
    </div>
  );
}
