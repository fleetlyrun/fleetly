import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppVariablesTab } from "./app-variables";
import { setToken } from "@/lib/token";

// Apply changes 受理后的导航锚（与 app-processes 同型同锚）：部署成功必须
// 落 typed 部署详情路由。回归锚：旧实现相对路径 window.location.assign
// 在 /p/<pid>/apps/<aid>/variables 落点解析成 /p/<pid>/apps/deployments/<id>
// （.. 吃掉 appId 段）——404 页。
const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  navigateMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

describe("AppVariablesTab apply changes", () => {
  it("navigates to the created deployment detail after staging a variable", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/apps/A1/spec")) {
        return new Response(JSON.stringify({ spec: { processes: [{ name: "web", replicas: "1" }] } }), { status: 200 });
      }
      if (path.startsWith("/v1/routes")) return new Response(JSON.stringify({ routes: [] }), { status: 200 });
      if (path === "/v1/deployments") return new Response(JSON.stringify({ deployment: { id: "D1" } }), { status: 200 });
      return new Response("{}", { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });
    // 单 process + filter=all 时 process 名只在下拉项里，等工具栏就绪。
    await waitFor(() => expect(screen.getByRole("button", { name: "+ Add variable" })).toBeTruthy());

    // Add variable 对话框：process 默认绑定当前筛选（首个 process），只填
    // 名值两格；Stage 落暂存面 → Apply changes 由禁用变可用。
    fireEvent.click(screen.getByRole("button", { name: "+ Add variable" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "LOG_LEVEL" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "debug" } });
    fireEvent.click(screen.getByRole("button", { name: "Stage" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply changes" }));

    await waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
        params: { projectId: "P1", appId: "A1", deploymentId: "D1" },
      }),
    );
    const [, init] = fetchMock.mock.calls.find(([p]) => p === "/v1/deployments") as [string, RequestInit];
    expect(JSON.parse(String(init.body)).spec_file).toContain("LOG_LEVEL");
    client.clear();
  });
});

// W-3 修复锚（2026-10-12 走查）：多进程分组头改为 `process web · 2
// replicas`——旧 `×2` 形似变量条数，副本数语义必须可读。
describe("AppVariablesTab process group header", () => {
  it("labels the replica count explicitly in multi-process groups", async () => {
    fetchMock.mockImplementation(async (path: string) => {
      if (path.startsWith("/v1/apps/A1/spec")) {
        return new Response(
          JSON.stringify({
            spec: {
              processes: [
                { name: "web", replicas: "2", env: { LOG_LEVEL: "info" } },
                { name: "worker", replicas: "1" },
              ],
            },
          }),
          { status: 200 },
        );
      }
      if (path.startsWith("/v1/routes")) return new Response(JSON.stringify({ routes: [] }), { status: 200 });
      return new Response("{}", { status: 200 });
    });
    const { client, wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });
    await waitFor(() => expect(screen.getByText("process web · 2 replicas")).toBeTruthy());
    expect(screen.getByText("process worker · 1 replica")).toBeTruthy();
    client.clear();
  });
});
