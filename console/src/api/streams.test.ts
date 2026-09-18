// 流客户端纯函数单测：gateway result 包裹解包 + error 终帧上抛。

import { describe, expect, it } from "vitest";

import { unwrapFrame } from "@/api/streams";
import { StreamError } from "@/api/stream";

describe("unwrapFrame (gateway result envelope)", () => {
  it("unwraps {result:{entry:…}} frames as served by grpc-gateway", () => {
    const inner = unwrapFrame({
      result: { entry: { line: "hello", stderr: false, source: "container", app: "demo", service: "web" } },
    });
    expect(inner).toEqual({
      entry: { line: "hello", stderr: false, source: "container", app: "demo", service: "web" },
    });
  });

  it("unwraps {result:{event:…}} frames (int64 seq is a JSON string)", () => {
    const inner = unwrapFrame({
      result: { event: { seq: "1", name: "deployment.queued", subject: "deployment:x", payload: "{}" } },
    });
    expect(inner?.event?.seq).toBe("1");
  });

  it("returns null for frames without result (keep-alive/empty)", () => {
    expect(unwrapFrame({})).toBeNull();
  });

  it("throws StreamError for the terminal {error:…} frame", () => {
    expect(() =>
      unwrapFrame({ error: { code: "E_EVENT_CURSOR_EXPIRED", message: "cursor expired" } }),
    ).toThrow(StreamError);
  });
});
