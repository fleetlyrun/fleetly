import { useState } from "react";
import { useLogStream } from "@/features/logs/use-log-stream";
import { LogViewer } from "@/features/logs/log-viewer";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { CircleStopIcon, PlayIcon } from "lucide-react";

// Bucket Store Logs tab（B5 姐妹页，pubsub-logs.tsx 同构）：实例载体日志流
// ——多轴寻址的 bucketstore 轴（服务端 StreamLogsRequest.bucket_store_id，
// B5 起在册）。实例载体是单 workload：无 process 选择器；text/tail/follow
// 与工作台同源。
export function BucketStoreLogsTab({ bucketStoreId }: { bucketStoreId: string }) {
  const log = useLogStream();
  const [text, setText] = useState("");
  const [tailLines, setTailLines] = useState("300");

  const start = () => log.start({ bucketStoreId, processes: [], containers: [], tailLines, text, follow: true });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end gap-2.5">
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Filter text (VL retrieval)</Label>
          <Input
            value={text}
            placeholder="erasure set, multipart…"
            onChange={(event) => setText(event.target.value)}
            className="h-8 w-56 font-mono text-xs"
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label className="text-2xs text-muted">Tail lines</Label>
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
        <p className="text-xs text-err">{log.error instanceof Error ? log.error.message : String(log.error)}</p>
      ) : null}

      <div className="h-[26rem] overflow-hidden rounded-lg border">
        <LogViewer frames={log.frames} streaming={log.streaming} />
      </div>

      <div className="flex items-center gap-3 font-mono text-2xs text-muted">
        <span>scope: bucket store carrier {bucketStoreId}</span>
        <span>{log.frames.length} frames</span>
        {log.ended && !log.streaming ? <span>stream ended — adjust filters and restart</span> : null}
        <span className="ml-auto">GET /v1/logs?bucket_store_id=…&amp;follow (NDJSON)</span>
      </div>
    </div>
  );
}
