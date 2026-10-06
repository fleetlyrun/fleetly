import { describe, expect, it } from "vitest";
import { decodeServerFrame, encodeResize, encodeStdin, type ServerFrame } from "./execFrames";

// 帧编解码契约（F3.2，ADR-0049）：与 gateway 原生入口常量集对拍——
// [1B kind][payload]；stdout/stderr 原始字节、meta/exit/error JSON。
// 这是手写消费面唯一的协议钉子（原生入口不进 swagger）。

function frameOf(kind: number, payload: string): ArrayBuffer {
  const head = new Uint8Array([kind]);
  const body = new TextEncoder().encode(payload);
  const out = new Uint8Array(head.length + body.length);
  out.set(head, 0);
  out.set(body, head.length);
  return out.buffer;
}

function raw(frame: ServerFrame | null): string {
  if (frame === null) return "";
  if (frame.kind === "stdout" || frame.kind === "stderr") return new TextDecoder().decode(frame.data);
  return "";
}

describe("encodeStdin", () => {
  it("prefixes the stdin kind byte", () => {
    const encoded = encodeStdin(new TextEncoder().encode("ls -la\n"));
    expect(encoded[0]).toBe(0x01);
    expect(new TextDecoder().decode(encoded.subarray(1))).toBe("ls -la\n");
  });
});

describe("encodeResize", () => {
  it("carries cols/rows JSON payload", () => {
    const encoded = encodeResize(120, 34);
    expect(encoded[0]).toBe(0x04);
    expect(JSON.parse(new TextDecoder().decode(encoded.subarray(1)))).toEqual({ cols: 120, rows: 34 });
  });
});

describe("decodeServerFrame", () => {
  it("decodes stdout frames as raw bytes", () => {
    expect(raw(decodeServerFrame(frameOf(0x02, "argv: echo hi\n")))).toBe("argv: echo hi\n");
  });
  it("decodes stderr frames as raw bytes", () => {
    expect(raw(decodeServerFrame(frameOf(0x03, "boom")))).toBe("boom");
  });
  it("decodes meta/exit/error as JSON payloads", () => {
    const meta = decodeServerFrame(frameOf(0x07, '{"instance":"t1","node_id":"n1"}'));
    expect(meta).toEqual({ kind: "meta", meta: { instance: "t1", node_id: "n1" } });
    const exit = decodeServerFrame(frameOf(0x05, '{"code":7}'));
    expect(exit).toEqual({ kind: "exit", exit: { code: 7 } });
    const err = decodeServerFrame(frameOf(0x06, '{"code":"agent_disconnected","message":"lost"}'));
    expect(err).toEqual({ kind: "error", error: { code: "agent_disconnected", message: "lost" } });
  });
  it("returns null for unknown kinds and empty frames (envelope is append-only)", () => {
    expect(decodeServerFrame(frameOf(0x42, "{}"))).toBeNull();
    expect(decodeServerFrame(new ArrayBuffer(0))).toBeNull();
  });
});
