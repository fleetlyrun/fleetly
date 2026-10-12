import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppVariablesTab } from "./app-variables";
import { setToken } from "@/lib/token";

// Variables tab（ADR-0072 重做）：AppVariable store 编辑器——staged 行集
// + Apply（PUT 变更键 / DELETE 移除键 + 重发冻结 Spec 走部署）+ 生效 env
// 只读投影。Apply 受理后的导航锚与 app-processes 同型同锚（typed 部署
// 详情路由）。
const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  navigateMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

// vitest 未开 globals：testing-library 自动 cleanup 不生效，显式卸载。
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

const FROZEN = {
  processes: [{ name: "web", replicas: "1", env: { TIER: "appvar", ROLE: "edge" }, secret_refs: ["db-url"] }],
};

function stubStore(vars: Array<{ name: string; value: string }>) {
  const puts: Array<Record<string, string>> = [];
  const deletes: string[] = [];
  const deploys: Array<Record<string, unknown>> = [];
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path.startsWith("/v1/app-variables/A1/")) {
      deletes.push(path);
      return new Response(JSON.stringify({}), { status: 200 });
    }
    if (path.startsWith("/v1/app-variables")) {
      if ((init?.method ?? "GET") === "PUT") puts.push(JSON.parse(String(init?.body)));
      return new Response(JSON.stringify({ variables: vars.map((v, i) => ({ id: `av${i}`, ...v })) }), { status: 200 });
    }
    if (path.startsWith("/v1/apps/A1/spec")) {
      return new Response(JSON.stringify({ spec: FROZEN }), { status: 200 });
    }
    if (path === "/v1/deployments") {
      deploys.push(JSON.parse(String(init?.body)));
      return new Response(JSON.stringify({ deployment: { id: "D1" } }), { status: 200 });
    }
    return new Response(JSON.stringify({}), { status: 404 });
  });
  return { puts, deletes, deploys };
}

describe("AppVariablesTab (AppVariable store)", () => {
  it("renders store rows and the effective-env projection", async () => {
    stubStore([{ name: "TIER", value: "appvar" }]);
    const { wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });

    expect(await screen.findByDisplayValue("TIER")).toBeTruthy();
    expect(await screen.findByDisplayValue("appvar")).toBeTruthy();
    // 生效 env 投影（冻结体合成终态 + secret refs 计数）。
    expect(await screen.findByText("2 keys · 1 secret refs")).toBeTruthy();
    expect(screen.getByText("ROLE")).toBeTruthy();
  });

  it("applies staged edits via PUT + redeploy and navigates to the deployment", async () => {
    const { puts, deploys } = stubStore([{ name: "TIER", value: "appvar" }]);
    const { wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });

    await screen.findByDisplayValue("appvar");
    const valueInputs = screen.getAllByDisplayValue("appvar");
    fireEvent.change(valueInputs[valueInputs.length - 1], { target: { value: "rotated" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));

    await waitFor(() => expect(navigateMock).toHaveBeenCalled());
    await waitFor(() => expect(puts).toEqual([{ app_id: "A1", name: "TIER", value: "rotated" }]));
    expect(deploys.length).toBeGreaterThan(0);
    expect((deploys[0] as { spec_file?: string }).spec_file).toContain("processes");
    expect(navigateMock).toHaveBeenCalled();
  });

  it("deletes removed rows and skips unchanged keys", async () => {
    const { puts, deletes } = stubStore([
      { name: "TIER", value: "appvar" },
      { name: "KEEP", value: "as-is" },
    ]);
    const { wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });

    await screen.findByDisplayValue("KEEP");
    fireEvent.click(screen.getByRole("button", { name: "Remove TIER" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));

    await waitFor(() => expect(deletes).toEqual([`/v1/app-variables/A1/TIER`]));
    await waitFor(() => expect(puts).toEqual([])); // KEEP 未变不重写
  });
});
