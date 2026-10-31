import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QuickstartPage } from "./quickstart-page";
import { setToken } from "@/lib/token";

// Quickstart 向导 deploy 载荷钉测（B1 确认闭环后的存量调用面止血）：
// 无人值守向导 = CLI --yes 语义——POST /v1/deployments 必须携带
// confirmation.skip=true（与 cmd/fleetly/cmd/quickstart.go 的 Skip: true
// 对齐；缺 confirmation 会被 ConfirmGuard 428 E_CONFIRMATION_REQUIRED 拒）。
// 全链 ensure 步骤桩空（项目/App/网络/路由全走新建腿），wait 流单帧
// NDJSON 直终态。

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

function json(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200 });
}

function stubFetch() {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path.startsWith("/v1/projects") && method === "GET") return json({ projects: [] });
    if (path.startsWith("/v1/projects") && method === "POST") return json({ project: { id: "P1" } });
    if (path.startsWith("/v1/apps") && method === "GET") return json({ apps: [] });
    if (path.startsWith("/v1/apps") && method === "POST") return json({ app: { id: "A1" } });
    if (path.startsWith("/v1/networks") && method === "GET") return json({ networks: [] });
    if (path.startsWith("/v1/networks") && method === "POST") return json({ network: { id: "N1" } });
    if (path.startsWith("/v1/routes") && method === "GET") return json({ routes: [] });
    if (path.startsWith("/v1/routes") && method === "POST") return json({ route: { id: "R1" } });
    if (path === "/v1/deployments" && method === "POST") return json({ deployment: { id: "D1", state: "queued" } });
    // wait 流：单帧直终态（succeeded）后收流。
    if (path === "/v1/deployments/D1/wait") {
      return new Response(`${JSON.stringify({ result: { deployment: { state: "succeeded" } } })}\n`, { status: 200 });
    }
    return json({});
  });
}

function deployInits(): Array<RequestInit | undefined> {
  return fetchMock.mock.calls
    .filter(([p, i]) => p === "/v1/deployments" && i?.method === "POST")
    .map(([, i]) => i);
}

describe("Quickstart 向导 deploy 载荷", () => {
  it("sends the deploy with confirmation.skip (unattended --yes semantics)", async () => {
    stubFetch();
    render(<QuickstartPage />);

    fireEvent.click(screen.getByRole("button", { name: "Run quickstart" }));

    await waitFor(() => expect(deployInits()).toHaveLength(1));
    expect(JSON.parse(String(deployInits()[0]?.body))).toEqual({
      app_id: "A1",
      image: "nginx:1.27",
      port: 80,
      confirmation: { skip: true },
    });
    // 全链收口：wait 流到终态，向导日志落定（后续步骤未被 deploy 步阻塞）。
    expect(await screen.findByText(/deployment reached succeeded/)).toBeTruthy();
  });
});
