import { useCallback, useEffect, useRef, useState } from "react";
import type { LogFrame } from "@/api/streams";
import { streamLogs } from "@/api/streams";

// 日志流生命周期（UI v2 批 3，Workbench 原型数据面）：过滤器在流启动时
// 定格（app/process/tail/text/follow——服务端契约定格语义）；follow 形态
// Stop 中断、改参重启；环形缓冲 5000 帧（旧页同口径）。decodeLine 导出
// 供下载/复制面复用（protojson bytes = base64）。

const MAX_FRAMES = 5_000;

export interface LogControls {
  appId: string;
  process: string;
  tailLines: string;
  text: string;
  follow: boolean;
}

export function decodeLine(b64: string | undefined): string {
  if (b64 === undefined) return "";
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

export function useLogStream() {
  const [frames, setFrames] = useState<LogFrame[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [streaming, setStreaming] = useState(false);
  // ended = 最近一次运行已自然收流（服务端关流/查询完）——空结果提示与
  // 状态栏后缀靠它与手动 Stop 区分（F3 走查口径）。
  const [ended, setEnded] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  const start = useCallback((controls: LogControls) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setFrames([]);
    setError(null);
    setStreaming(true);
    setEnded(false);
    const query = new URLSearchParams();
    if (controls.appId !== "") query.set("app_id", controls.appId);
    if (controls.process !== "") query.set("process", controls.process);
    if (controls.tailLines !== "") query.set("tail_lines", controls.tailLines);
    if (controls.text !== "") query.set("text", controls.text);
    query.set("follow", controls.follow ? "1" : "0");
    streamLogs(query, controller.signal, (frame) => {
      setFrames((prev) => {
        const next = [...prev, frame];
        return next.length > MAX_FRAMES ? next.slice(next.length - MAX_FRAMES) : next;
      });
    })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(cause);
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setStreaming(false);
          setEnded(true);
        }
      });
  }, []);

  const stop = useCallback(() => {
    abortRef.current?.abort();
    setStreaming(false);
  }, []);

  const clear = useCallback(() => setFrames([]), []);

  return { frames, error, streaming, ended, start, stop, clear, maxFrames: MAX_FRAMES };
}
