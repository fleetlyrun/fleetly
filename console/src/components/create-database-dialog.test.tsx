// 创建数据库对话框（抽出的可复用组件）测试：目标项目显式展示（审查发现的
// 「对话框不显示目标项目」可发现性缺口）与请求 project 透传断言。DatabasesPage
// 的页面级回归（含无上下文选择时的缺省形态）见 pages/DatabasesPage.test.tsx。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CreateDatabaseDialog } from "@/components/create-database-dialog";

interface PostedCall {
  body: Record<string, unknown>;
}

function renderDialog(projectRef: string) {
  const onOpenChange = vi.fn();
  const posted: PostedCall[] = [];
  const fetchMock = vi.fn().mockImplementation((_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "POST") {
      posted.push({ body: JSON.parse(String(init.body ?? "{}")) as Record<string, unknown> });
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () => Promise.resolve({ database: { id: "DB1" } }),
      });
    }
    return Promise.resolve({ ok: true, status: 200, statusText: "", json: () => Promise.resolve({}) });
  });
  vi.stubGlobal("fetch", fetchMock);

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CreateDatabaseDialog open onOpenChange={onOpenChange} projectRef={projectRef} />
    </QueryClientProvider>,
  );
  return posted;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("CreateDatabaseDialog", () => {
  it("shows the explicit target project in the dialog", () => {
    renderDialog("founder/staging");
    expect(screen.getByTestId("database-create-dialog")).toBeInTheDocument();
    expect(screen.getByTestId("database-target-project")).toHaveTextContent("founder/staging");
  });

  it("posts the create payload carrying the target project", async () => {
    const posted = renderDialog("founder/staging");
    const user = userEvent.setup();

    await user.type(screen.getByTestId("database-name-input"), "pg-new");
    await user.click(screen.getByTestId("database-create-submit"));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].body.name).toBe("pg-new");
    expect(posted[0].body.project).toBe("founder/staging");
  });

  it("falls back to the server default when no projectRef is given", async () => {
    const posted = renderDialog("");
    const user = userEvent.setup();

    expect(screen.getByTestId("database-target-project")).toHaveTextContent(
      "default (your personal team)",
    );
    await user.type(screen.getByTestId("database-name-input"), "pg-new");
    await user.click(screen.getByTestId("database-create-submit"));

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].body).not.toHaveProperty("project");
  });
});
