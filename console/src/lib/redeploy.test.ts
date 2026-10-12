import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { redeployFrozenSpec } from "./redeploy";
import { setToken } from "./token";

// hero Redeploy 数据链（ADR-0069 批）：回读冻结 Spec → 原样 spec_file
// 重发；未部署（404）精确报错。路由文件无单测先例（features/lib 承载
// 可测单元），提取本函数让重发链有同批锚。

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

it("re-posts the frozen spec verbatim as spec_file and returns the deployment id", async () => {
  const frozen = { app: { id: "A1" }, processes: [{ name: "web", replicas: "2" }] };
  fetchMock.mockImplementation(async (path: string) => {
    if (path === "/v1/apps/A1/spec") return new Response(JSON.stringify({ spec: frozen }), { status: 200 });
    if (path === "/v1/deployments") return new Response(JSON.stringify({ deployment: { id: "D9" } }), { status: 200 });
    throw new Error(`unexpected fetch: ${path}`);
  });

  const id = await redeployFrozenSpec("A1");

  expect(id).toBe("D9");
  const [, init] = fetchMock.mock.calls.find(([p]) => p === "/v1/deployments") as [string, RequestInit];
  const body = JSON.parse(String(init.body));
  expect(body.app_id).toBe("A1");
  expect(JSON.parse(body.spec_file)).toEqual(frozen);
});

it("rejects with an explicit message when the app has no frozen spec", async () => {
  fetchMock.mockImplementation(async () => new Response(JSON.stringify({ code: "not_found" }), { status: 404 }));
  await expect(redeployFrozenSpec("A1")).rejects.toThrow("no frozen spec");
  expect(fetchMock.mock.calls.filter(([p]) => p === "/v1/deployments")).toHaveLength(0);
});
