import { apiSend } from "./client";
import { decodeServerFrame, encodeResize, encodeStdin, type ServerFrame } from "../lib/execFrames";

// exec 会话消费面（F3.2，ADR-0049）：受理走注解面 REST（POST
// /v1/exec/sessions，Bearer）换会话 + 秒级单用途票据（绑会话——浏览器 WS
// 无自定义头，ADR-0026 exec 版）；流走原生 WS 入口 GET /v1/exec/stream
//（不进 swagger，本文件即手写契约消费面——webhook/uploads 先例）。
// 帧信封编解码单源 lib/execFrames.ts。

export interface ExecSession {
  id?: string;
  app_id?: string;
  process?: string;
  workload_id?: string;
  instance?: string;
  node_id?: string;
  command?: string[];
  tty?: boolean;
  created_at?: string;
}

export interface CreateResult {
  session?: ExecSession;
  ticket?: string;
  expires_in?: number;
}

// createExecSession 受理会话（argv 空 + tty = /bin/sh 服务端缺省）。
export function createExecSession(appId: string, process: string, command: string[], tty: boolean): Promise<CreateResult> {
  return apiSend<CreateResult>("/v1/exec/sessions", "POST", {
    app_id: appId,
    process,
    command,
    tty,
  });
}

// ExecStreamCallbacks 是会话流的消费回调面（onClose 收口即会话终态：
// exit/error 帧已投递或传输层断开——调用方呈现重开面）。
export interface ExecStreamCallbacks {
  onFrame: (frame: ServerFrame) => void;
  onClose: () => void;
}

// openExecStream 打开 WS 会话流（同源 ws/wss；票据单用途）。
export function openExecStream(sessionId: string, ticket: string, cb: ExecStreamCallbacks): WebSocket {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  const url = `${proto}://${window.location.host}/v1/exec/stream?session_id=${encodeURIComponent(sessionId)}&ticket=${encodeURIComponent(ticket)}`;
  const ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";
  ws.onmessage = (ev) => {
    if (!(ev.data instanceof ArrayBuffer)) return;
    const frame = decodeServerFrame(ev.data);
    if (frame !== null) cb.onFrame(frame);
  };
  ws.onclose = cb.onClose;
  ws.onerror = cb.onClose;
  return ws;
}

// sendStdin/sendResize 是上行帧助手（连接未开时静默丢弃——xterm 输入在
// connect 窗口内即到达的边界形态）。
export function sendStdin(ws: WebSocket | null, data: string): void {
  if (ws !== null && ws.readyState === WebSocket.OPEN) {
    ws.send(encodeStdin(new TextEncoder().encode(data)));
  }
}

export function sendResize(ws: WebSocket | null, cols: number, rows: number): void {
  if (ws !== null && ws.readyState === WebSocket.OPEN) {
    ws.send(encodeResize(cols, rows));
  }
}
