import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createTestRouter } from "@lynx-go/uikit-core/testing";
import { ConfigsPanel } from "./panels";
import { configsSearch } from "@/kit/variables";
import { setToken } from "@/lib/token";

// Configs 面板（详情/编辑批；useCollection 化批 3 补 URL 上下文——
// useCollection 需要 Router/Query 两上下文，裸 render 不再成立）：view
// 动词/行点击进 GetConfig 回读弹窗；编辑 PUT 同名冻结新版本，成功后回
// 视图并刷新到新版本号；内容未变更时 Save 诚实禁用（不铸无谓版本）。
// F1 回流批（M10）：内联最小路由退役——uikit-core 0.4.0 /testing 出口
// 修复严格度后 createTestRouter 是单源（0.3.1 时代 noUnusedLocals 违例的
// 内联理由不复存在）。

// CONFIGS_ROUTE_ID：path 即根路由 id——useSearch({ from }) 按此锚定活跃
// match（真实路由 id 是 /_shell/p/$projectId/variables，此处测试路由自
// 持同形 id）。
const CONFIGS_ROUTE_ID = "/p/P1/variables";

function renderConfigsPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createTestRouter({
    routes: [
      {
        path: CONFIGS_ROUTE_ID,
        component: () => (
          <QueryClientProvider client={queryClient}>
            <ConfigsPanel projectId="P1" routeId={CONFIGS_ROUTE_ID} />
          </QueryClientProvider>
        ),
        searchSchema: configsSearch,
      },
    ],
    initial: { path: CONFIGS_ROUTE_ID },
  });
  return render(<RouterProvider router={router} />);
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

// vitest 未开 globals：testing-library 自动 cleanup 不生效，显式卸载。
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

// stubConfigs 用可变 current 模拟版本轴：GET 详情回读当前版全文，PUT
// 冻结下一版（version+1），列表回当前版号。
function stubConfigs() {
  let current = { version: 3, content: "SELECT 1;\n" };
  const puts: Array<Record<string, unknown>> = [];
  const detailGets: string[] = [];
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path.startsWith("/v1/configs/P1/")) {
      detailGets.push(path);
      return new Response(
        JSON.stringify({
          config: { name: "config.yaml", version: String(current.version), created_at: "2026-10-18T00:00:00Z", content: current.content },
        }),
        { status: 200 },
      );
    }
    if (path.startsWith("/v1/configs?")) {
      return new Response(
        JSON.stringify({
          configs: [{ id: "c1", project_id: "P1", name: "config.yaml", version: String(current.version), created_at: "2026-10-18T00:00:00Z" }],
        }),
        { status: 200 },
      );
    }
    if (path === "/v1/configs" && (init?.method ?? "") === "PUT") {
      const body = JSON.parse(String(init?.body)) as { project_id: string; name: string; content: string };
      puts.push(body);
      current = { version: current.version + 1, content: body.content };
      return new Response(
        JSON.stringify({ config: { name: body.name, version: String(current.version), content: body.content } }),
        { status: 200 },
      );
    }
    return new Response(JSON.stringify({}), { status: 404 });
  });
  return { puts, detailGets };
}

async function openDetail() {
  renderConfigsPanel();
  expect(await screen.findByText("config.yaml")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "view" }));
  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/configs/P1/config.yaml", expect.anything()));
  // 等视图渲染出全文再返回——Edit 钮在回读完成前是禁用态。
  expect(await screen.findByText("SELECT 1;")).toBeTruthy();
}

describe("ConfigsPanel detail/edit", () => {
  it("opens the read view with content, version and size", async () => {
    const { detailGets } = stubConfigs();
    await openDetail();
    expect(detailGets).toEqual(["/v1/configs/P1/config.yaml"]);
    expect(await screen.findByText("SELECT 1;")).toBeTruthy();
    // 元信息行被 JSX 表达式拆成多个文本节点，按整段正则匹配。
    expect(screen.getByText(/10 bytes/)).toBeTruthy();
  });

  it("saves an edit as a new version and returns to the read view", async () => {
    const { puts } = stubConfigs();
    await openDetail();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const area = (await screen.findByRole("textbox")) as HTMLTextAreaElement;
    expect(area.value).toBe("SELECT 1;\n");
    fireEvent.change(area, { target: { value: "SELECT 2;\n" } });
    fireEvent.click(screen.getByRole("button", { name: "Save as new version" }));
    await waitFor(() => expect(puts).toEqual([{ project_id: "P1", name: "config.yaml", content: "SELECT 2;\n" }]));
    // 回视图：编辑器退场，详情失效重取后按新版本渲染新内容。
    await waitFor(() => expect(screen.queryByRole("textbox")).toBeNull());
    expect(await screen.findByText("SELECT 2;")).toBeTruthy();
  });

  it("keeps save disabled until the content changes", async () => {
    stubConfigs();
    await openDetail();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const save = (await screen.findByRole("button", { name: "Save as new version" })) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "SELECT 2;\n" } });
    expect(((await screen.findByRole("button", { name: "Save as new version" })) as HTMLButtonElement).disabled).toBe(false);
  });
});
