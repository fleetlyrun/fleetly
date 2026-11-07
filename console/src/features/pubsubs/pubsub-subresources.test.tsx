import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { setToken } from "@/lib/token";
import { formatBytes } from "@/features/buckets/bucket-subresources";
import { CreateTopicDialog, TopicDeleteButton, subresourceKindOf, subStatusTone } from "./pubsub-subresources";

// 子资源 tab 钉测（B8，ADR-0096 决策 10 / design §4.7）：① engine 分面
// （kafka 协议族 → topic / nats → stream）与收敛状态色调；② 创建对话框
// 落行载荷；③ 删除 wide 档两段流——预演披露行先行（disclosure 进确认框
// Consequences 段）→ 确认后带票 DELETE（票走 query——DELETE 无 body）。

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path === "/v1/pubsubs/topics/preview-delete" && method === "POST") {
      return jsonResponse({ disclosure: "Deleting topic orders destroys the partitioned data — unrecoverable.", ticket: "tkt-del" });
    }
    if (path === "/v1/pubsubs/p1/topics" && method === "POST") return jsonResponse({ topic: { id: "t9" } });
    return jsonResponse({});
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

describe("子资源分面与色调", () => {
  it("engine 决定面：nats → stream，其余（kafka 协议族）→ topic", () => {
    expect(subresourceKindOf("nats")).toBe("stream");
    expect(subresourceKindOf("kafka")).toBe("topic");
    expect(subresourceKindOf("redpanda")).toBe("topic");
  });
  it("收敛状态色调：ready=success / error=warning / pending=info", () => {
    expect(subStatusTone("ready")).toBe("success");
    expect(subStatusTone("error")).toBe("warning");
    expect(subStatusTone("pending")).toBe("info");
  });
  it("配额字节人类形态（0 = none）", () => {
    expect(formatBytes("0")).toBe("none");
    expect(formatBytes(1073741824)).toBe("1.0 GiB");
  });
});

describe("CreateTopicDialog", () => {
  it("提交落 POST /v1/pubsubs/{id}/topics（partitions/retention 载荷）", async () => {
    render(<CreateTopicDialog open onOpenChange={() => undefined} projectId="shop" pubsubId="p1" />, { wrapper: withClient() });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "orders" } });
    fireEvent.change(screen.getByLabelText("Partitions (only increase later)"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("retention.ms (empty = engine default)"), { target: { value: "3600000" } });
    fireEvent.click(screen.getByRole("button", { name: "Declare topic" }));
    await waitFor(() => {
      const post = fetchMock.mock.calls.find(([p, i]) => p === "/v1/pubsubs/p1/topics" && i?.method === "POST");
      expect(post).toBeDefined();
      expect(JSON.parse(String(post![1].body))).toEqual({ name: "orders", partitions: 3, retention_ms: 3600000 });
    });
  });
});

describe("TopicDeleteButton（wide 档两段流）", () => {
  it("预演披露行进确认框 → 确认带票 DELETE（query 携票）", async () => {
    render(<TopicDeleteButton projectId="shop" pubsubId="p1" topic={{ id: "t1", name: "orders" }} />, { wrapper: withClient() });
    fireEvent.click(screen.getByRole("button", { name: "Delete topic orders" }));
    await screen.findByText(/destroys the partitioned data/);
    fireEvent.click(screen.getByRole("button", { name: "Delete topic" }));
    await waitFor(() => {
      const del = fetchMock.mock.calls.find(([, i]) => i?.method === "DELETE");
      expect(del?.[0]).toBe("/v1/pubsubs/p1/topics/t1?confirmation.ticket=tkt-del");
    });
  });
});
