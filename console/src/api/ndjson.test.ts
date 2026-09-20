// NDJSON 解析器单测：常规帧、跨 chunk 断帧拼接、无结尾换行的尾帧、
// 空行容忍、flush 语义。gateway 流形态 = 每帧 JSON + "\n"（grpc-gateway
// JSONPb.Delimiter）。

import { describe, expect, it } from "vitest";

import { NdjsonParser } from "@/api/stream";

interface Frame {
  entry?: { line: string };
  event?: { seq: number };
}

describe("NdjsonParser", () => {
  it("parses complete newline-delimited frames", () => {
    const p = new NdjsonParser<Frame>();
    const frames = p.push('{"entry":{"line":"a"}}\n{"entry":{"line":"b"}}\n');
    expect(frames).toHaveLength(2);
    expect(frames[0].entry?.line).toBe("a");
    expect(frames[1].entry?.line).toBe("b");
    expect(p.flush()).toHaveLength(0);
  });

  it("reassembles a frame split across chunks (frame split across chunk boundary)", () => {
    const p = new NdjsonParser<Frame>();
    expect(p.push('{"entry":{"li')).toHaveLength(0);
    expect(p.push('ne":"split"}}\n')).toHaveLength(1);
  });

  it("handles multiple frames in one chunk", () => {
    const p = new NdjsonParser<Frame>();
    const frames = p.push(
      '{"event":{"seq":1}}\n{"event":{"seq":2}}\n{"event":{"seq":3}}\n',
    );
    expect(frames.map((f) => f.event?.seq)).toEqual([1, 2, 3]);
  });

  it("buffers partial trailing frame without newline until flush", () => {
    const p = new NdjsonParser<Frame>();
    expect(p.push('{"event":{"seq":7}}\n{"event":{"seq":8}}')).toHaveLength(1);
    const tail = p.flush();
    expect(tail).toHaveLength(1);
    expect(tail[0].event?.seq).toBe(8);
  });

  it("skips empty lines and keeps objects with nested braces", () => {
    const p = new NdjsonParser<Frame>();
    const frames = p.push(
      '\n{"entry":{"line":"a(){\\n}"}}\n\n{"entry":{"line":"c"}}\n',
    );
    expect(frames).toHaveLength(2);
    expect(frames[0].entry?.line).toBe("a(){\n}");
  });

  it("rejects malformed JSON frames (fail loud, not silent drop)", () => {
    const p = new NdjsonParser<Frame>();
    expect(() => p.push("{not json}\n")).toThrow();
  });
});
