import { useState } from "react";
import { useLogStream } from "@/features/logs/use-log-stream";
import { LogViewer } from "@/features/logs/log-viewer";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { CircleStopIcon, PlayIcon } from "lucide-react";

// Database Logs tab（IA v3 二期①点亮）：库载体日志流——三轴寻址的
// database 轴（服务端 StreamLogsRequest.database_id，T8 预案兑现）。
// 库载体是单 workload：无 process 选择器；text/tail/follow 与工作台同源。
export function DatabaseLogsTab({ databaseId }: { databaseId: string }) {
  const log = useLogStream();
  const [text, setText] = useState("");
  const [tailLines, setTailLines] = useState("300");

  const start = () => log.start({ databaseId, process: "", tailLines, text, follow: true });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-2.5">
        <div className="flex flex-col gap-1">
          <Label className="text-[11px] text-muted-foreground">Filter text (VL retrieval)</Label>
          <Input
            value={text}
            placeholder="checkpoint, slow query…"
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
          <Button size="sm" onClick={start}>
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
        <span>scope: database carrier {databaseId}</span>
        <span>{log.frames.length} frames</span>
        {log.ended && !log.streaming ? <span>stream ended — adjust filters and restart</span> : null}
        <span className="ml-auto">GET /v1/logs?database_id=…&follow (NDJSON)</span>
      </div>
    </div>
  );
}
