import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useSubscription } from "@lynx-go/uikit-core/react";
import { z } from "zod";
import { ChevronDownIcon, DownloadIcon, PlayIcon, SquareIcon, XIcon } from "lucide-react";
import type { LogFrame } from "@/api/streams";
import { useQuery } from "@tanstack/react-query";
import { collectionQueryOptions } from "@lynx-go/uikit-core/react";
import { scoped } from "@lynx-go/uikit-core/kernel";
import { useProjectsDirectory } from "@/kit/projects";
import { useAppsDirectory } from "@/kit/apps";
import { revisionsResource, revisionsLookup } from "@/kit/revisions";
import { useProjectId } from "@/lib/project";
import { decodeLine, type LogControls } from "@/features/logs/use-log-stream";
import { describeApiError } from "@/kit/errors";
import { logsTransport, type EventFrame } from "@/kit/log-stream";
import { LogViewer } from "@/components/kit/subscription/log-viewer";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";

// 日志工作台（Workbench 原型 → UI v2 批3 订阅族）：GET /v1/logs 的 NDJSON
// 帧流迁 uikit useSubscription（引擎手动形态——「点 Run 才启动」：Run 把
// 过滤器定格为 controls 并 bump nonce，transport 实例重建 + autoStart 即开；
// Stop 后再 Run 同样走重建，不在旧引擎上 resume）。环形缓冲 5000 交引擎
// （policy.buffer 丢最旧）；呈现走 LogViewer 块（智能跟随/jump-to-live/
// 下载/清空），页级保留 TSV 下载与清空附加面。筛选走类型化 search params
// （URL 即状态）；根高 calc(100svh-3.25rem)（顶栏 h-13）：workbench 视口
// 定死，追加帧只滚日志框不再撑高页面。

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

// 环形缓冲 5000（旧页同口径）交引擎；退避序列走引擎重连（follow 重开同
// query 重放 tail）。
const LOG_BUFFER = 5_000;
const LOG_BACKOFF_MS = [1_000, 2_000, 4_000];

// run=null 时 transport 不开（autoStart=false）——占位 controls 仅满足
// transport 形状，open 永不触达。
const IDLE_CONTROLS: LogControls = {
  processes: [],
  containers: [],
  tailLines: "",
  text: "",
  follow: false,
};

function LogsWorkbench() {
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const [projectId] = useProjectId();
  const [appId, setAppId] = useState(search.app ?? "");
  // process= 逗号形多值（空 = 全部 process）。
  const [processes, setProcesses] = useState<string[]>(() => parseProcesses(search.process));
  const [tailLines, setTailLines] = useState(search.tail ?? "200");
  const [text, setText] = useState(search.text ?? "");
  const [follow, setFollow] = useState(true);
  // run 定格「点 Run 时刻」的过滤器（服务端契约定格语义）；nonce 保证
  // 同参重跑也重建引擎（清缓冲、退避归零）。
  const [run, setRun] = useState<{ controls: LogControls; nonce: number } | null>(null);
  const transport = useMemo(() => logsTransport(run?.controls ?? IDLE_CONTROLS), [run]);
  const log = useSubscription(transport, {
    cursor: null, // 活尾即开；日志流无游标语义
    autoStart: run !== null,
    policy: { buffer: LOG_BUFFER, retry: { backoffMs: LOG_BACKOFF_MS } },
  });
  // 批4 集成收口：目录/修订伴读迁 kit（revisions 取最新一条做进程候选）。
  const projects = useProjectsDirectory();
  const apps = useAppsDirectory(projectId);
  const revisions = useQuery({
    ...collectionQueryOptions(scoped(revisionsResource, { projectId, appId }), revisionsLookup.defaults, {
      refetchInterval: 15_000,
    }),
    enabled: appId !== "",
  });
  const processOptions = (revisions.data?.rows[0]?.process_strategies ?? [])
    .map((entry) => entry?.process ?? "")
    .filter((name) => name !== "");

  // streaming = 本轮 Run 仍在连接或收流（待命引擎的 idle 态同为
  // reconnecting，须以 run 已落定区分）。
  const streaming = run !== null && (log.status === "live" || log.status === "reconnecting");
  const noApp = appId === "";

  // 项目目录到达后补默认 App（切换器语境优先，search 参数其次）
  useEffect(() => {
    if (appId === "" && search.app === undefined && (apps.data?.rows ?? []).length > 0) {
      setAppId(apps.data!.rows[0].id);
    }
  }, [apps.data, appId, search.app]);

  // 已选 App 不在当前项目目录（切了项目）→ 重置
  useEffect(() => {
    const rows = apps.data?.rows ?? [];
    if (appId !== "" && rows.length > 0 && !rows.some((entry) => entry.id === appId)) {
      setAppId(rows[0].id);
    }
  }, [apps.data, appId]);

  // LogViewer 块定高（px prop）——以容器实测高度注入，保持 workbench 满
  // 高口径（追加帧只滚日志框不撑高页面）。
  const holderRef = useRef<HTMLDivElement>(null);
  const [holderHeight, setHolderHeight] = useState(420);
  useLayoutEffect(() => {
    const el = holderRef.current;
    if (!el) return;
    const observer = new ResizeObserver(() => setHolderHeight(el.clientHeight));
    observer.observe(el);
    setHolderHeight(el.clientHeight);
    return () => observer.disconnect();
  }, []);

  function start() {
    if (appId === "") return;
    setRun({
      controls: { appId, databaseId: undefined, runId: undefined, processes, containers: [], tailLines, text, follow },
      nonce: (run?.nonce ?? 0) + 1,
    });
    void navigate({
      search: {
        app: appId,
        process: processes.length > 0 ? processes.join(",") : undefined,
        text: text || undefined,
        tail: tailLines !== "200" ? tailLines : undefined,
      },
    });
  }

  function download() {
    const body = log.frames
      .map(
        (frame) =>
          `${frame.payload.time ?? ""}\t${[frame.payload.node, frame.payload.container?.slice(0, 12)].filter(Boolean).join(" ")}\t${decodeLine(frame.payload.line)}`,
      )
      .join("\n");
    const blob = new Blob([body], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `fleetly-logs-${new Date().toISOString().slice(0, 19).replaceAll(":", "")}.txt`;
    anchor.click();
    URL.revokeObjectURL(url);
  }

  // formatLogLine 面向 LogViewer 块（行呈现 + 块内下载同文）。
  const formatLogLine = (frame: EventFrame<LogFrame>) => {
    const source = [frame.payload.node, frame.payload.container?.slice(0, 12)].filter(Boolean).join(" ");
    return `${frame.payload.time ?? ""}  ${source || frame.payload.workload_id?.slice(0, 12) || ""}  ${decodeLine(frame.payload.line)}`;
  };

  return (
    <div className="flex h-[calc(100svh-3.25rem)] flex-col px-6 pt-6">
      <div className="mb-1 flex items-baseline gap-3">
        <h1 className="font-heading text-lg font-bold tracking-tight">Logs</h1>
        <span className="text-xs text-muted">
          {projects.data?.rows?.find((project) => project.id === projectId)?.name ?? "—"} — live stream or stored search
        </span>
      </div>

      <div className="flex flex-none flex-wrap items-end gap-2.5 py-3">
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">App</Label>
          <Select value={appId} onValueChange={setAppId}>
            <SelectTrigger className="w-44">
              <SelectValue placeholder={noApp ? "select an app…" : ""} />
            </SelectTrigger>
            <SelectContent>
              {(apps.data?.rows ?? []).map((app) => (
                <SelectItem key={app.id} value={app.id}>
                  {app.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Process</Label>
          <ProcessMultiSelect
            options={processOptions}
            selected={processes}
            onChange={setProcesses}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Tail</Label>
          <Input
            value={tailLines}
            onChange={(event) => setTailLines(event.target.value.replace(/[^0-9]/g, ""))}
            inputMode="numeric"
            className="h-8 w-16 font-mono text-xs"
          />
        </div>
        <div className="flex flex-1 flex-col gap-1">
          <Label className="text-2xs text-muted">Text filter</Label>
          <Input
            value={text}
            onChange={(event) => setText(event.target.value)}
            placeholder="search text — switches to the stored path"
            className="h-8 text-xs"
            onKeyDown={(event) => {
              if (event.key === "Enter" && !streaming && !noApp) start();
            }}
          />
        </div>
        <Label className="mb-1.5 flex items-center gap-2 text-xs text-muted">
          <Switch checked={follow} onCheckedChange={setFollow} />
          Follow
        </Label>
        {/* 单一稳定按钮（F1 走查先例：恒 type=button 同一节点，Stop/Start
            同位换型的重提交面不存在——语义随 streaming 分派）。 */}
        <Button
          type="button"
          size="sm"
          variant={streaming ? "destructive" : "default"}
          disabled={!streaming && noApp}
          title={streaming ? undefined : noApp ? "select an app first" : undefined}
          onClick={() => (streaming ? log.stop() : start())}
          className="mb-0.5"
        >
          {streaming ? <SquareIcon className="size-3" /> : <PlayIcon className="size-3" />}
          {streaming ? "Stop" : "Start"}
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

      {log.status === "error" && log.error ? (
        <div className="mb-2 flex items-center gap-3 rounded-md border border-err/30 bg-err/10 px-3 py-2 text-xs text-err">
          <span>{describeApiError(log.error)}</span>
          <Button type="button" size="sm" variant="outline" className="ml-auto" onClick={start}>
            Retry
          </Button>
        </div>
      ) : null}

      <div ref={holderRef} className="min-h-0 flex-1">
        {run === null ? (
          <div className="flex size-full items-center justify-center rounded-lg border bg-panel/40">
            <p className="text-sm text-muted">Pick filters and press Start.</p>
          </div>
        ) : (
          <LogViewer sub={log} formatLine={formatLogLine} height={Math.max(holderHeight, 120)} />
        )}
      </div>

      <div className="flex flex-none items-center gap-4 py-2 font-mono text-2xs text-muted">
        <span>
          {log.frames.length} frame{log.frames.length === 1 ? "" : "s"}
          {streaming ? " · streaming" : log.status === "ended" ? " · ended" : ""}
          {log.status === "stalled" ? " · stalled" : ""}
          {log.frames.length >= LOG_BUFFER ? " · buffer capped (oldest dropped)" : ""}
        </span>
        <span className="ml-auto">GET /v1/logs · {text !== "" ? "stored path" : "runtime live path"}</span>
      </div>
    </div>
  );
}

// ProcessMultiSelect 是 process 多选下拉（CheckboxItem 形）：空选 = 全部
// process（服务端语义：无 workload 过滤 = 本 App 全部 Workload）。
function ProcessMultiSelect({
  options,
  selected,
  onChange,
}: {
  options: string[];
  selected: string[];
  onChange: (next: string[]) => void;
}) {
  const summary = selected.length === 0 ? "All processes" : selected.length <= 2 ? selected.join(", ") : `${selected[0]} +${selected.length - 1}`;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" size="sm" className="mb-0 h-8 w-44 justify-between font-normal">
          <span className="truncate font-mono text-xs">{summary}</span>
          <ChevronDownIcon className="size-3.5 flex-none text-muted" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-44">
        <DropdownMenuLabel className="text-2xs text-muted">Filter by process</DropdownMenuLabel>
        {options.length === 0 ? (
          <div className="px-2 py-1.5 text-2xs text-muted">No frozen processes yet — deploy first.</div>
        ) : (
          options.map((name) => (
            <DropdownMenuCheckboxItem
              key={name}
              checked={selected.includes(name)}
              onCheckedChange={(checked) => {
                onChange(checked ? [...selected, name] : selected.filter((entry) => entry !== name));
              }}
              onSelect={(event) => event.preventDefault()}
              className="font-mono text-xs"
            >
              {name}
            </DropdownMenuCheckboxItem>
          ))
        )}
        {selected.length > 0 ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onChange([])} className="text-xs">
              Clear selection
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// parseProcesses 解析 process= 逗号形（去空、去重——深链诚实收敛）。
function parseProcesses(raw: string | undefined): string[] {
  if (raw === undefined) return [];
  const seen = new Set<string>();
  for (const part of raw.split(",")) {
    const name = part.trim();
    if (name !== "") seen.add(name);
  }
  return [...seen];
}
