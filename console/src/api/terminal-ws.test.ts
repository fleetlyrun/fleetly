// terminal-ws 帧编解码单测：与 Go 侧 internal/execrelay/protocol.go 的线
// 路形态逐字节对齐（u32 BE 长度 + u8 类型 + 载荷；流载荷 = u16 BE idLen +
// id + data）——往返 + 违例拒收 + 已知字节序列锚（防两端漂移）。

import { describe, expect, it } from "vitest";

import {
  CloseCode,
  FrameType,
  SESSION_ID_PLACEHOLDER,
  encodeCloseFrame,
  encodeResizeFrame,
  encodeStreamFrame,
  decodeFrame,
  decodeStreamPayload,
  terminalWebSocketUrl,
} from "./terminal-ws";

describe("terminal-ws frame codec", () => {
  it("round-trips a stream frame", () => {
    const data = new TextEncoder().encode("echo hello\n");
    const frame = encodeStreamFrame(FrameType.Stdin, SESSION_ID_PLACEHOLDER, data);
    const { type, payload } = decodeFrame(frame);
    expect(type).toBe(FrameType.Stdin);
    const { id, data: out } = decodeStreamPayload(payload);
    expect(id).toBe(SESSION_ID_PLACEHOLDER);
    expect(new TextDecoder().decode(out)).toBe("echo hello\n");
  });

  it("matches the Go wire layout byte-for-byte (known sequence anchor)", () => {
    // encodeStreamFrame(stdin, "-", "hi") 的 Go 对应产物字节序列（手算锚）：
    // [0,0,0,5]=len(2+1+2) [3]=stdin [0,1]=idLen(1) ['-'] 'h' 'i'
    const frame = encodeStreamFrame(FrameType.Stdin, "-", new TextEncoder().encode("hi"));
    expect(Array.from(frame)).toEqual([0, 0, 0, 5, 3, 0, 1, 45, 104, 105]);
  });

  it("round-trips a resize frame", () => {
    const frame = encodeResizeFrame(SESSION_ID_PLACEHOLDER, 120, 40);
    const { type, payload } = decodeFrame(frame);
    expect(type).toBe(FrameType.Resize);
    expect(JSON.parse(new TextDecoder().decode(payload))).toEqual({
      id: SESSION_ID_PLACEHOLDER,
      cols: 120,
      rows: 40,
    });
  });

  it("round-trips a close frame with the reason text", () => {
    const frame = encodeCloseFrame(SESSION_ID_PLACEHOLDER, CloseCode.TIMEOUT, "idle timeout");
    const { type, payload } = decodeFrame(frame);
    expect(type).toBe(FrameType.SessionClose);
    expect(JSON.parse(new TextDecoder().decode(payload))).toEqual({
      id: SESSION_ID_PLACEHOLDER,
      code: CloseCode.TIMEOUT,
      reason: "idle timeout",
    });
  });

  it("rejects length-prefix mismatches (fail-closed, mirrors Go)", () => {
    const frame = encodeStreamFrame(FrameType.Stdin, "-", new Uint8Array([1, 2, 3]));
    // 篡改长度前缀 → 解码必须抛错（不猜测续读）。
    frame[2] = 9;
    expect(() => decodeFrame(frame)).toThrow(/length prefix/);
  });

  it("rejects unknown frame types", () => {
    const raw = new Uint8Array([0, 0, 0, 0, 0x7f]);
    expect(() => decodeFrame(raw)).toThrow(/unknown frame type/);
  });

  it("rejects empty session ids in stream payloads", () => {
    expect(() => encodeStreamFrame(FrameType.Stdin, "", new Uint8Array([1]))).toThrow(/out of range/);
  });
});

describe("terminal-ws url assembly", () => {
  it("builds a same-origin ws URL from the ticket path", () => {
    // jsdom location = http://localhost:3000/ → 同源 /v1 base → ws://localhost:3000。
    expect(terminalWebSocketUrl("/v1/terminal?ticket=t1")).toBe(
      "ws://localhost:3000/v1/terminal?ticket=t1",
    );
  });
});
