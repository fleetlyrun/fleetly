// 创建应用对话框测试（P0-1「创建应用入口」验收面）：渲染与名字校验、
// deploy 载荷断言（app 路径 / project 透传 / compose 缺 name 时前置注入）、
// compose 顶层 name 优先回填（与服务端 A1 一致性对齐）、错误信封渲染、
// 成功导航（DeployResponse 无应用 id → /apps + location.state 排队说明）。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CreateAppDialog, parseComposeName } from "@/components/create-app-dialog";

const COMPOSE_NO_NAME = "services:\n  web:\n    image: nginx:1.27-alpine";
const COMPOSE_WITH_NAME = "name: compose-app\nservices:\n  web:\n    image: nginx:1.27-alpine";

/** 与 client.utf8ToBase64 对称的解码（断言上行 compose 原文）。 */
function decodeBase64(b64: string): string {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

/** /apps 落地探测：渲染 pathname + location.state（导航断言面）。 */
function AppsLanding() {
  const location = useLocation();
  return (
    <p data-testid="apps-landing">
      {location.pathname} {JSON.stringify(location.state)}
    </p>
  );
}

interface PostedCall {
  url: string;
  body: Record<string, unknown>;
}

function renderDialog(opts: {
  projectRef?: string;
  postResponse?: { ok: boolean; status: number; json: Record<string, unknown> };
} = {}) {
  const onOpenChange = vi.fn();
  const posted: PostedCall[] = [];
  const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === "POST" && url.includes("/deployments")) {
      posted.push({ url, body: JSON.parse(String(init.body ?? "{}")) as Record<string, unknown> });
      const resp = opts.postResponse ?? {
        ok: true,
        status: 200,
        json: { deployment_id: "DEP1", app: "web-api", status: "queued" },
      };
      return Promise.resolve({
        ok: resp.ok,
        status: resp.status,
        statusText: "",
        json: () => Promise.resolve(resp.json),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
  vi.stubGlobal("fetch", fetchMock);

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter initialEntries={["/projects/01PRJ1"]}>
      <QueryClientProvider client={client}>
        <Routes>
          <Route
            path="/projects/01PRJ1"
            element={
              <CreateAppDialog
                open
                onOpenChange={onOpenChange}
                projectRef={opts.projectRef ?? "founder/staging"}
              />
            }
          />
          <Route path="/apps" element={<AppsLanding />} />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return { onOpenChange, posted };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("parseComposeName", () => {
  it("finds the unindented top-level name line only", () => {
    expect(parseComposeName("name: my-api\nservices: {}")).toBe("my-api");
    expect(parseComposeName("services:\n  web:\n    name: inner\nname: top")).toBe("top");
    expect(parseComposeName('name: "quoted"')).toBe("quoted");
    expect(parseComposeName(COMPOSE_NO_NAME)).toBeNull();
    expect(parseComposeName("")).toBeNull();
    // 缩进的 name: 是服务级键，不是顶层。
    expect(parseComposeName("services:\n  web:\n    name: inner")).toBeNull();
  });
});

describe("CreateAppDialog", () => {
  it("shows the target project and disables submit for an invalid name", async () => {
    renderDialog();
    const user = userEvent.setup();

    expect(screen.getByTestId("app-target-project")).toHaveTextContent("founder/staging");
    expect(screen.getByTestId("app-create-dialog")).toBeInTheDocument();

    await user.type(screen.getByTestId("app-create-name-input"), "Bad Name");
    await user.type(screen.getByTestId("app-create-compose-input"), COMPOSE_NO_NAME);
    expect(screen.getByTestId("app-create-submit")).toBeDisabled();
  });

  it("prepends name to compose without a top-level name and posts app+project", async () => {
    const { posted } = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByTestId("app-create-name-input"), "web-api");
    await user.type(screen.getByTestId("app-create-compose-input"), COMPOSE_NO_NAME);
    expect(screen.getByTestId("app-compose-name-note")).toBeInTheDocument();

    await user.click(screen.getByTestId("app-create-submit"));

    await waitFor(() => expect(posted).toHaveLength(1));
    // app 走 REST 路径段，project 走 body（proto deployments.proto body:"*"）。
    expect(posted[0].url).toContain("/v1/apps/web-api/deployments");
    expect(posted[0].body.project).toBe("founder/staging");
    expect(decodeBase64(String(posted[0].body.compose))).toBe(
      `name: web-api\n${COMPOSE_NO_NAME}`,
    );
  });

  it("lets the compose top-level name win and backfills the name field", async () => {
    const { posted } = renderDialog();
    const user = userEvent.setup();

    // 先填入冲突名，再输入带顶层 name 的 compose → 名字段被回填为 compose 名。
    await user.type(screen.getByTestId("app-create-name-input"), "web-api");
    await user.type(screen.getByTestId("app-create-compose-input"), COMPOSE_WITH_NAME);
    await waitFor(() =>
      expect(screen.getByTestId("app-create-name-input")).toHaveValue("compose-app"),
    );

    await user.click(screen.getByTestId("app-create-submit"));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].url).toContain("/v1/apps/compose-app/deployments");
    // compose 已带 name → 原文直传，不重复注入。
    expect(decodeBase64(String(posted[0].body.compose))).toBe(COMPOSE_WITH_NAME);
  });

  it("renders the server error envelope on failure and stays on the dialog", async () => {
    renderDialog({
      postResponse: {
        ok: false,
        status: 422,
        json: {
          code: "E_COMPOSE_INVALID",
          message: "compose is invalid",
          suggestion: "check the controlled subset",
        },
      },
    });
    const user = userEvent.setup();

    await user.type(screen.getByTestId("app-create-name-input"), "web-api");
    await user.type(screen.getByTestId("app-create-compose-input"), COMPOSE_NO_NAME);
    await user.click(screen.getByTestId("app-create-submit"));

    const envelope = await screen.findByTestId("error-envelope");
    expect(envelope).toHaveTextContent("E_COMPOSE_INVALID");
    expect(screen.queryByTestId("apps-landing")).not.toBeInTheDocument();
  });

  it("navigates to /apps with a queued notice after success (no app id in DeployResponse)", async () => {
    const { onOpenChange } = renderDialog();
    const user = userEvent.setup();

    await user.type(screen.getByTestId("app-create-name-input"), "web-api");
    await user.type(screen.getByTestId("app-create-compose-input"), COMPOSE_NO_NAME);
    await user.click(screen.getByTestId("app-create-submit"));

    const landing = await screen.findByTestId("apps-landing");
    expect(landing).toHaveTextContent("/apps");
    expect(landing).toHaveTextContent("DEP1");
    expect(landing).toHaveTextContent("web-api");
    // 成功后对话框关闭（onOpenChange(false)）。
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("omits project from the payload when no projectRef is given", async () => {
    const { posted } = renderDialog({ projectRef: "" });
    const user = userEvent.setup();

    expect(screen.getByTestId("app-target-project")).toHaveTextContent(
      "default (your personal team)",
    );
    await user.type(screen.getByTestId("app-create-name-input"), "web-api");
    await user.type(screen.getByTestId("app-create-compose-input"), COMPOSE_NO_NAME);
    await user.click(screen.getByTestId("app-create-submit"));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].body).not.toHaveProperty("project");
  });
});
