// exec 会话流的 WS 帧编解码（F3.2，ADR-0049）：[1B kind][payload] 二进制
// 信封——与 gateway 原生入口（internal/assembly/gateway_exec.go）同一常量
// 集（原生入口不进 swagger，Console 手写消费面——webhook/uploads 先例；
// 协议形状由 apitest exec 契约测试双端钉死）。

export const FRAME_STDIN = 0x01;
export const FRAME_STDOUT = 0x02;
export const FRAME_STDERR = 0x03;
export const FRAME_RESIZE = 0x04;
export const FRAME_EXIT = 0x05;
export const FRAME_ERROR = 0x06;
export const FRAME_META = 0x07;

export interface SessionMeta {
  instance: string;
  node_id: string;
}

export interface SessionExit {
  code: number;
}

export interface SessionError {
  code: string;
  message: string;
}

export type ServerFrame =
  | { kind: "meta"; meta: SessionMeta }
  | { kind: "stdout"; data: Uint8Array }
  | { kind: "stderr"; data: Uint8Array }
  | { kind: "exit"; exit: SessionExit }
  | { kind: "error"; error: SessionError };

// encodeStdin 打一条输入帧。
export function encodeStdin(data: Uint8Array): Uint8Array {
  return concat(new Uint8Array([FRAME_STDIN]), data);
}

// encodeResize 打一条尺寸帧（payload JSON {cols,rows}）。
export function encodeResize(cols: number, rows: number): Uint8Array {
  return concat(new Uint8Array([FRAME_RESIZE]), new TextEncoder().encode(JSON.stringify({ cols, rows })));
}

// decodeServerFrame 解一条服务端帧；未知 kind 返回 null（信封只增——
// 旧客户端对新增 kind 的兼容面）。
export function decodeServerFrame(msg: ArrayBuffer): ServerFrame | null {
  const b = new Uint8Array(msg);
  if (b.length === 0) return null;
  const payload = b.subarray(1);
  switch (b[0]) {
    case FRAME_STDOUT:
      return { kind: "stdout", data: payload };
    case FRAME_STDERR:
      return { kind: "stderr", data: payload };
    case FRAME_META:
      return { kind: "meta", meta: parseJSON(payload) as SessionMeta };
    case FRAME_EXIT:
      return { kind: "exit", exit: parseJSON(payload) as SessionExit };
    case FRAME_ERROR:
      return { kind: "error", error: parseJSON(payload) as SessionError };
    default:
      return null;
  }
}

function parseJSON(payload: Uint8Array): unknown {
  return JSON.parse(new TextDecoder().decode(payload));
}

function concat(head: Uint8Array, tail: Uint8Array): Uint8Array {
  const out = new Uint8Array(head.length + tail.length);
  out.set(head, 0);
  out.set(tail, head.length);
  return out;
}
