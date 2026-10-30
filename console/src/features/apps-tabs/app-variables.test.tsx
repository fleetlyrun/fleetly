import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppVariablesTab } from "./app-variables";
import { setToken } from "@/lib/token";

// Variables tab（ADR-0072 重做）：AppVariable store 编辑器——staged 行集
// + Apply（PUT 变更键 / DELETE 移除键 + 重发冻结 Spec 走部署）+ 生效 env
// 只读投影。Apply 受理后的导航锚与 app-processes 同型同锚（typed 部署
// 详情路由）。B1 W4 遗留直发面收口（2026-10-31）：Apply & deploy 接
// ConfirmSkipDialog 双击 skip——首击写 store + 无票部署被拒（428）→
// 「已请求确认」态 → 二击全量重放（PUT 幂等 / DELETE 已删键 404 容忍）
// 带 confirmation.skip=true。
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

function stubStore(vars: Array<{ name: string; value: string }>, deployResponses?: Array<{ status: number; body: unknown }>) {
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
      const next = deployResponses?.shift();
      if (next != null && next.status !== 200) {
        return new Response(JSON.stringify(next.body), { status: next.status });
      }
      return new Response(JSON.stringify({ deployment: { id: "D1" } }), { status: 200 });
    }
    return new Response(JSON.stringify({}), { status: 404 });
  });
  return { puts, deletes, deploys };
}

// 双击壳的第二击（确认钮词 = Confirm apply；首击钮词与触发钮同词，portal
// 渲染在末尾，取末个）。
async function clickApplyConfirm() {
  fireEvent.click(screen.getAllByRole("button", { name: "Apply & deploy" }).at(-1)!);
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
    // 双击 skip 接线：首击 PUT + 无票部署被拒（428）→ 「已请求确认」态 →
    // 二击全量重放（PUT 幂等）带 skip=true → 导航落部署详情。
    const { puts, deploys } = stubStore(
      [{ name: "TIER", value: "appvar" }],
      [{ status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation" } }],
    );
    const { wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });

    await screen.findByDisplayValue("appvar");
    const valueInputs = screen.getAllByDisplayValue("appvar");
    fireEvent.change(valueInputs[valueInputs.length - 1], { target: { value: "rotated" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    expect(await screen.findByText("Apply variables and deploy?")).toBeTruthy();
    await clickApplyConfirm();

    // 首击：PUT 落库 + 部署无 confirmation 键。
    await waitFor(() => expect(deploys).toHaveLength(1));
    expect((deploys[0] as { confirmation?: unknown }).confirmation).toBeUndefined();
    expect(await screen.findByText(/Deploying these variable changes requires an explicit confirmation/)).toBeTruthy();

    // 二击：Confirm apply → skip=true。
    fireEvent.click(screen.getByRole("button", { name: "Confirm apply" }));
    await waitFor(() => expect(deploys).toHaveLength(2));
    expect(deploys[1]).toMatchObject({ confirmation: { skip: true } });
    expect((deploys[1] as { spec_file?: string }).spec_file).toContain("processes");

    await waitFor(() => expect(navigateMock).toHaveBeenCalled());
    await waitFor(() => expect(puts).toEqual([
      { app_id: "A1", name: "TIER", value: "rotated" },
      { app_id: "A1", name: "TIER", value: "rotated" }, // 二击幂等重放
    ]));
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
    expect(await screen.findByText("Apply variables and deploy?")).toBeTruthy();
    await clickApplyConfirm();

    await waitFor(() => expect(deletes).toEqual([`/v1/app-variables/A1/TIER`]));
    await waitFor(() => expect(puts).toEqual([])); // KEEP 未变不重写
  });

  it("second click replays the delete of an already-removed key (404 tolerated as idempotent)", async () => {
    // 双击二击的全量重放会重放 DELETE：已删键 404 容忍（目标态已达成），
    // 链路继续走到带 skip 的部署受理。
    const { deletes, deploys } = stubStore(
      [{ name: "TIER", value: "appvar" }],
      [{ status: 428, body: { code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation" } }],
    );
    fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
      if (path.startsWith("/v1/app-variables/A1/TIER")) {
        deletes.push(path);
        // 一击已删：二击重放收到 404。
        return new Response(JSON.stringify({ code: "not_found", message: "no such variable" }), {
          status: deletes.length > 1 ? 404 : 200,
        });
      }
      if (path.startsWith("/v1/app-variables")) {
        return new Response(JSON.stringify({ variables: [{ id: "av0", name: "TIER", value: "appvar" }] }), { status: 200 });
      }
      if (path.startsWith("/v1/apps/A1/spec")) {
        return new Response(JSON.stringify({ spec: FROZEN }), { status: 200 });
      }
      if (path === "/v1/deployments") {
        deploys.push(JSON.parse(String(init?.body)));
        if (deploys.length === 1) {
          return new Response(JSON.stringify({ code: "E_CONFIRMATION_REQUIRED", message: "requires confirmation" }), {
            status: 428,
          });
        }
        return new Response(JSON.stringify({ deployment: { id: "D1" } }), { status: 200 });
      }
      return new Response(JSON.stringify({}), { status: 404 });
    });
    const { wrapper } = withClient();
    render(<AppVariablesTab projectId="P1" appId="A1" />, { wrapper });

    await screen.findByDisplayValue("appvar");
    fireEvent.click(screen.getByRole("button", { name: "Remove TIER" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply & deploy" }));
    expect(await screen.findByText("Apply variables and deploy?")).toBeTruthy();
    await clickApplyConfirm();
    expect(await screen.findByText(/requires an explicit confirmation/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Confirm apply" }));

    // 二击 DELETE 收 404 仍走完链路：部署受理（skip）+ staged 清空。
    await waitFor(() => expect(deploys).toHaveLength(2));
    expect(deploys[1]).toMatchObject({ confirmation: { skip: true } });
    expect(deletes).toEqual(["/v1/app-variables/A1/TIER", "/v1/app-variables/A1/TIER"]);
  });
});
