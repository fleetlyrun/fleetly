import { useState } from "react";
import { useRevisions } from "@/lib/catalog";
import { useLogStream } from "@/features/logs/use-log-stream";
import { LogViewer } from "@/features/logs/log-viewer";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { CircleStopIcon, PlayIcon } from "lucide-react";

// App Logs scoped tab（IA v3 T2）：全局工作台的 app 域变体——同一
// useLogStream/LogViewer 数据面（协议零改动），过滤器在此固化 app 语境，
// process/text/tail 由 tab 内控件给定。URL 契约不变：全局工作台深链仍走
// /logs?app=。
export function AppLogsTab({ appId }: { appId: string }) {
  const log = useLogStream();
  const revisions = useRevisions(appId);
  const processes = (revisions.data?.[0]?.process_strategies ?? [])
    .map((entry) => entry?.process ?? "")
    .filter((name) => name !== "");
  const [process, setProcess] = useState("");
  const [text, setText] = useState("");
  const [tailLines, setTailLines] = useState("300");
  const effectiveProcess = process !== "" ? process : (processes[0] ?? "");

  const start = () =>
    log.start({ appId, process: effectiveProcess, tailLines, text, follow: true });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-2.5">
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
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Filter text (VL retrieval)</Label>
          <Input
            value={text}
            placeholder="error, timeout…"
            onChange={(event) => setText(event.target.value)}
            className="h-8 w-56 font-mono text-xs"
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Tail lines</Label>
          <Input
            value={tailLines}
            inputMode="numeric"
            onChange={(event) => setTailLines(event.target.value.replace(/\D/g, ""))}
            className="h-8 w-20 font-mono text-xs"
          />
        </div>
        {log.streaming ? (
          <Button size="sm" variant="destructive" onClick={log.stop}>
            <CircleStopIcon data-icon-start-inline />
            Stop
          </Button>
        ) : (
          <Button size="sm" disabled={effectiveProcess === ""} onClick={start}>
            <PlayIcon data-icon-start-inline />
            Start follow
          </Button>
        )}
        <Button size="sm" variant="ghost" onClick={log.clear} disabled={log.frames.length === 0}>
          Clear
        </Button>
      </div>

      {log.error != null ? (
        <p className="text-xs text-destructive">{log.error instanceof Error ? log.error.message : String(log.error)}</p>
      ) : null}

      <div className="h-[26rem] overflow-hidden rounded-lg border">
        <LogViewer frames={log.frames} streaming={log.streaming} />
      </div>

      <div className="flex items-center gap-3 font-mono text-[11px] text-muted-foreground">
        <span>scope: app {appId}</span>
        <span>{log.frames.length} frames</span>
        {log.ended && !log.streaming ? <span>stream ended — adjust filters and restart</span> : null}
        <span className="ml-auto">GET /v1/logs?app_id=…&follow (NDJSON)</span>
      </div>
      <p className="text-[11.5px] text-muted-foreground">
        Full workbench (history window, export, cross-app filters):{" "}
        <a className="text-info underline-offset-2 hover:underline" href={`/logs?app=${encodeURIComponent(appId)}`}>
          open in Logs workbench
        </a>
        .
      </p>
    </div>
  );
}
