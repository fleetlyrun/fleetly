// Web 终端 WS 客户端（E7 W5-S6）：帧编解码与 Go 侧 internal/execrelay/
// protocol.go 镜像（线路形态唯一真源在 Go——TS 侧按注释逐字对齐，测试钉
// 往返）。浏览器只消费帧词表的子集：发 stdin/resize，收 stdout/stderr/
// close（register/open 由控制面 ↔ relay 内部流转，浏览器永不发出）。
//
// 线路形态（每条 WS binary 消息 = 一帧）：
//   [len: u32 BE][type: u8][payload]
// 结构化载荷 = JSON；流载荷 = [idLen: u16 BE][id][data]。
//
// 会话 ID 归属：一条浏览器 WS = 恰一个会话（ticket 绑定），控制面按连接
// 路由、忽略浏览器面帧自报的 ID——客户端统一发 SESSION_ID_PLACEHOLDER
//（协议流载荷要求非空 ID；真实 ID 只在服务端 close 帧回显，Console 不依赖）。

import { apiUrl } from "./client";

export const enum FrameType {
  Register = 0x01,
  SessionOpen = 0x02,
  Stdin = 0x03,
  Stdout = 0x04,
  Stderr = 0x05,
  Resize = 0x06,
  SessionClose = 0x07,
  Ping = 0x08,
  Pong = 0x09,
}

/** 会话关闭码（Go SessionCloseFrame.Code 镜像；只增）。 */
export const CloseCode = {
  OK: 0,
  BAD_SHELL: 400,
  DENIED: 403,
  NO_TARGET: 404,
  TIMEOUT: 408,
  DISCONNECTED: 410,
  LIMIT: 429,
  INTERNAL: 500,
} as const;

/** 浏览器面流帧的会话 ID 占位（hub 按连接路由——ID 值无语义，仅协议非空）。 */
export const SESSION_ID_PLACEHOLDER = "-";

/** 终帧投影（close.code/reason 直接进 Console 状态行）。 */
export interface SessionCloseFrame {
  id: string;
  code: number;
  reason: string;
}

export interface TerminalHandlers {
  /** stdout/stderr 数据。 */
  onData(data: Uint8Array, stderr: boolean): void;
  /** 终帧（会话收尾——任何原因；恰好一次）。 */
  onClose(frame: SessionCloseFrame): void;
  /** 传输层断开（未带终帧的异常路径；恰好一次）。 */
  onDisconnect(reason: string): void;
}

export interface TerminalConnection {
  write(data: string): void;
  resize(cols: number, rows: number): void;
  close(): void;
}

const MAX_PAYLOAD = 512 << 10;
const MAX_SESSION_ID = 64;

/** 编码流帧（stdin 出站）：u16be idLen + id + data。 */
export function encodeStreamFrame(type: FrameType, sessionID: string, data: Uint8Array): Uint8Array {
  const idBytes = new TextEncoder().encode(sessionID);
  if (idBytes.length === 0 || idBytes.length > MAX_SESSION_ID) {
    throw new Error(`stream frame session id out of range (${idBytes.length})`);
  }
  if (data.length > MAX_PAYLOAD - idBytes.length - 2) {
    throw new Error("stream frame data exceeds limit");
  }
  const out = new Uint8Array(5 + 2 + idBytes.length + data.length);
  const view = new DataView(out.buffer);
  view.setUint32(0, 2 + idBytes.length + data.length, false);
  out[4] = type;
  view.setUint16(5, idBytes.length, false);
  out.set(idBytes, 7);
  out.set(data, 7 + idBytes.length);
  return out;
}

/** 编码 resize 帧（JSON 载荷）。 */
export function encodeResizeFrame(sessionID: string, cols: number, rows: number): Uint8Array {
  const payload = new TextEncoder().encode(JSON.stringify({ id: sessionID, cols, rows }));
  const out = new Uint8Array(5 + payload.length);
  const view = new DataView(out.buffer);
  view.setUint32(0, payload.length, false);
  out[4] = FrameType.Resize;
  out.set(payload, 5);
  return out;
}

/** 编码会话关闭帧（操作员主动退出）。 */
export function encodeCloseFrame(sessionID: string, code: number, reason: string): Uint8Array {
  const payload = new TextEncoder().encode(JSON.stringify({ id: sessionID, code, reason }));
  const out = new Uint8Array(5 + payload.length);
  const view = new DataView(out.buffer);
  view.setUint32(0, payload.length, false);
  out[4] = FrameType.SessionClose;
  out.set(payload, 5);
  return out;
}

/** 解帧：返回 type + 载荷（长度/类型违例抛错——与 Go 侧 fail-closed 同锚）。 */
export function decodeFrame(msg: ArrayBuffer | Uint8Array): { type: FrameType; payload: Uint8Array } {
  const bytes = msg instanceof Uint8Array ? msg : new Uint8Array(msg);
  if (bytes.length < 5) throw new Error(`frame too short (${bytes.length})`);
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const n = view.getUint32(0, false);
  if (n !== bytes.length - 5) throw new Error("frame length prefix mismatch");
  if (n > MAX_PAYLOAD) throw new Error("frame payload exceeds limit");
  const type = bytes[4] as FrameType;
  if (type < FrameType.Register || type > FrameType.Pong) throw new Error(`unknown frame type ${type}`);
  return { type, payload: bytes.subarray(5) };
}

/** 解流载荷：会话 ID + 数据。 */
export function decodeStreamPayload(payload: Uint8Array): { id: string; data: Uint8Array } {
  if (payload.length < 2) throw new Error("stream frame too short");
  const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
  const n = view.getUint16(0, false);
  if (n === 0 || n > MAX_SESSION_ID || 2 + n > payload.length) throw new Error("stream frame id length invalid");
  return { id: new TextDecoder().decode(payload.subarray(2, 2 + n)), data: payload.subarray(2 + n) };
}

/**
 * WS URL 拼装：websocket_path 形如 /v1/terminal?ticket=...（服务端下发）。
 * 同源形态（缺省 /v1 base）→ ws(s)://location.host；独立域名 base（
 * VITE_API_BASE 形态 https://ctrl.example.com/v1）→ 剥 /v1 后缀取源并把
 * http(s) 映射 ws(s)。协议随页面。
 */
export function terminalWebSocketUrl(websocketPath: string): string {
  const base = apiUrl("").replace(/\/+$/, "");
  let origin = "";
  if (base !== "" && base !== "/v1") {
    origin = base.replace(/\/v1\/?$/i, "");
    if (/^https?:\/\//i.test(origin)) {
      origin = origin.replace(/^http/i, "ws");
    }
  }
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}${origin}${websocketPath}`;
}

/**
 * 接入终端会话（持 ticket 的 websocket_path——CreateTerminalTicket 响应）。
 * settled 栅保证 onClose/onDisconnect 恰好一次。
 */
export function connectTerminal(websocketPath: string, handlers: TerminalHandlers): TerminalConnection {
  const ws = new WebSocket(terminalWebSocketUrl(websocketPath));
  ws.binaryType = "arraybuffer";
  let settled = false;
  const settle = (fn: () => void) => {
    if (!settled) {
      settled = true;
      fn();
    }
  };

  ws.onmessage = (ev) => {
    try {
      const { type, payload } = decodeFrame(ev.data as ArrayBuffer);
      if (type === FrameType.Stdout || type === FrameType.Stderr) {
        const { data } = decodeStreamPayload(payload);
        handlers.onData(data, type === FrameType.Stderr);
      } else if (type === FrameType.SessionClose) {
        const frame = JSON.parse(new TextDecoder().decode(payload)) as SessionCloseFrame;
        settle(() => handlers.onClose(frame));
      }
      // register/open/resize/ping/pong 与浏览器面无关——静默忽略。
    } catch (err) {
      settle(() => handlers.onDisconnect(String(err)));
    }
  };
  ws.onclose = () => settle(() => handlers.onDisconnect("websocket closed"));
  ws.onerror = () => settle(() => handlers.onDisconnect("websocket error"));

  return {
    write(data) {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(encodeStreamFrame(FrameType.Stdin, SESSION_ID_PLACEHOLDER, new TextEncoder().encode(data)));
    },
    resize(cols, rows) {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(encodeResizeFrame(SESSION_ID_PLACEHOLDER, cols, rows));
    },
    close() {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(encodeCloseFrame(SESSION_ID_PLACEHOLDER, 0, "closed by operator"));
      }
      ws.close();
    },
  };
}
