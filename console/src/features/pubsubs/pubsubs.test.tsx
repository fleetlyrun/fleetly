import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { setToken } from "@/lib/token";
import type { PubSubRow } from "@/kit/pubsubs";
import { CreatePubSubDialog } from "./pubsubs";
import { PubSubSettingsEditor } from "./pubsub-settings";

// pubsubs 姐妹页钉测（B4c，design §6.2/§3.5——catalog-forms.test 形态）：
// ① 创建对话框目录驱动（pubsub 族过滤、nats 版本 Default 置顶、旋钮区/
// 资源面/restore 源、license 披露）；② Settings tab 的 Save changes =
// 预览 → 分级确认（本族首批旋钮全 static：任何变更恒 replace =「将触发
// 滚动重启」）→ 带票 PATCH；STALE 自动重预览。夹具引擎名刻意非真引擎词
// （msgkv）——页面零引擎硬编码字面量的证据：渲染全靠目录载荷。

const CATALOG = {
  engines: [
    {
      kind: "database",
      engine: "sqlish",
      dialect: "sqlish",
      port: 5000,
      versions: [{ id: "1" }, { id: "2", default: true }],
      knobs: [],
    },
    {
      kind: "pubsub",
      engine: "msgkv",
      dialect: "msgkv",
      port: 4222,
      license_note: "BSL 1.1 (four-year change date)",
      versions: [{ id: "2.10", deprecated: true, note: "removal after two releases" }, { id: "2.11", default: true }],
      knobs: [
        { key: "stream_max_file", kind: "size", default_value: "1gb", class: "static", category: "persistence" },
        { key: "stream_max_memory", kind: "size", class: "static", category: "memory" },
      ],
    },
  ],
};

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  };
}

const fetchMock = vi.fn();
let previewResponses: Array<{ status: number; body: unknown }> = [];
let updateResponses: Array<{ status: number; body: unknown }> = [];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function stubFetch() {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path === "/v1/data-engines") return jsonResponse(CATALOG);
    if (path === "/v1/pubsubs" && method === "POST") return jsonResponse({ id: "ps-new" });
    if (path === "/v1/pubsubs/preview-update" && method === "POST") {
      const next = previewResponses.shift() ?? { status: 200, body: { rollout: "replace", ticket: "tkt-default" } };
      return jsonResponse(next.body, next.status);
    }
    if (path === "/v1/pubsubs/ps1" && method === "PATCH") {
      const next = updateResponses.shift() ?? { status: 200, body: { pubsub: { id: "ps1" } } };
      return jsonResponse(next.body, next.status);
    }
    return jsonResponse({});
  });
}

function calls(path: string, method: string): Array<[string, RequestInit]> {
  return fetchMock.mock.calls.filter(([p, i]) => p === path && (i?.method ?? "GET") === method) as Array<[string, RequestInit]>;
}

function bodyOf(call: [string, RequestInit] | undefined): unknown {
  return JSON.parse(String(call?.[1].body));
}

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  fetchMock.mockReset();
  previewResponses = [];
  updateResponses = [];
  vi.stubGlobal("ResizeObserver", ResizeObserverStub);
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
  stubFetch();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

const pubsub: PubSubRow = {
  id: "ps1",
  name: "events",
  engine: "msgkv",
  version: "2.11",
  status: "running",
  settings: { stream_max_file: "1gb" },
};

describe("CreatePubSubDialog（目录驱动——pubsub 族过滤 + 版本 + 旋钮）", () => {
  it("引擎下拉只列目录 pubsub 族（database 族引擎不进 pubsub 表单）", async () => {
    const { wrapper } = withClient();
    render(<CreatePubSubDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    const select = (await screen.findByLabelText("Engine")) as HTMLSelectElement;
    const options = [...select.querySelectorAll("option")].map((option) => option.value).filter(Boolean);
    expect(options).toEqual(["msgkv"]);
    expect(options).not.toContain("sqlish");
  });

  it("选引擎即重置版本为该引擎 Default + 旋钮区/资源面出现（KnobFields/ResourcesFields 复用）", async () => {
    const { wrapper } = withClient();
    render(<CreatePubSubDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    fireEvent.change(await screen.findByLabelText("Engine"), { target: { value: "msgkv" } });
    const version = (await screen.findByLabelText("Version")) as HTMLSelectElement;
    expect(version.value).toBe("2.11");
    expect([...version.querySelectorAll("option")].map((option) => option.textContent)).toEqual([
      "2.11 (default)",
      "2.10 (deprecated)",
    ]);
    expect(await screen.findByLabelText("stream_max_file")).toBeTruthy();
    expect(screen.getByLabelText("CPU limit (millicores)")).toBeTruthy();
    // license_note 显著露出（目录披露）。
    expect(screen.getByText(/BSL 1\.1/)).toBeTruthy();
  });

  it("弃用版本灰显（无 restore 源时禁选）；弃用理由行在场", async () => {
    const { wrapper } = withClient();
    render(<CreatePubSubDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    fireEvent.change(await screen.findByLabelText("Engine"), { target: { value: "msgkv" } });
    const version = (await screen.findByLabelText("Version")) as HTMLSelectElement;
    const deprecated = [...version.querySelectorAll("option")].find((option) => option.value === "2.10") as HTMLOptionElement;
    expect(deprecated.disabled).toBe(true);
    expect(screen.getByText(/2\.10 is deprecated — removal after two releases/)).toBeTruthy();
  });

  it("create 提交载荷：目录值（version/knobs/resources）+ restore 源可选", async () => {
    const { wrapper } = withClient();
    render(<CreatePubSubDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "bus" } });
    fireEvent.change(await screen.findByLabelText("Engine"), { target: { value: "msgkv" } });
    fireEvent.change(screen.getByLabelText("stream_max_file"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("CPU limit (millicores)"), { target: { value: "500" } });
    fireEvent.change(screen.getByLabelText("Restore from backup (optional)"), { target: { value: "bk_01" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(calls("/v1/pubsubs", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/pubsubs", "POST")[0])).toEqual({
      project_id: "P1",
      name: "bus",
      engine: "msgkv",
      version: "2.11",
      settings: { stream_max_file: "2gb" },
      resources: { cpu_millis: 500 },
      restore_from_backup: "bk_01",
    });
  });
});

describe("PubSubSettingsEditor（Save changes = 预览 → 恒 replace 确认 → 带票 PATCH）", () => {
  function renderEditor(row: PubSubRow = pubsub) {
    const { wrapper } = withClient();
    render(<PubSubSettingsEditor pubsub={row} projectId="P1" />, { wrapper });
  }

  it("目录驱动渲染行值；无改动时 Save/Discard 禁用", async () => {
    renderEditor();
    const maxFile = (await screen.findByLabelText("stream_max_file")) as HTMLInputElement;
    expect(maxFile.value).toBe("1");
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Discard changes" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("static 旋钮变更：预演恒 replace——确认框披露「将触发滚动重启」+ 分次保存提示，确认钮 = Save and roll", async () => {
    previewResponses.push({
      status: 200,
      body: {
        settings_changes: [{ key: "stream_max_file", from: "1gb", to: "2gb", class: "static" }],
        static_keys: ["stream_max_file"],
        rollout: "replace",
        ticket: "tkt-roll",
        ticket_expires_in: 600,
      },
    });
    updateResponses.push({ status: 200, body: { pubsub: { id: "ps1" }, static_applied: ["stream_max_file"] } });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("stream_max_file"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(calls("/v1/pubsubs/preview-update", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/pubsubs/preview-update", "POST")[0])).toEqual({
      update: { pubsub_id: "ps1", settings: { values: { stream_max_file: "2gb" } } },
    });
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Saving triggers a rolling restart of the carrier/)).toBeTruthy();
    expect(within(dialog).getByText(/every save that carries rolling changes restarts the carrier once/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save and roll" }));

    await waitFor(() => expect(calls("/v1/pubsubs/ps1", "PATCH")).toHaveLength(1));
    expect(bodyOf(calls("/v1/pubsubs/ps1", "PATCH")[0])).toEqual({
      settings: { values: { stream_max_file: "2gb" } },
      confirmation: { ticket: "tkt-roll" },
    });
  });

  it("资源面改动单独在场（settings 缺席——未动面由服务端行值织票面摘要）", async () => {
    previewResponses.push({
      status: 200,
      body: { resources_to: { memory_mb: 512 }, rollout: "replace", ticket: "tkt-r" },
    });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("Memory limit (MiB)"), { target: { value: "512" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls("/v1/pubsubs/preview-update", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/pubsubs/preview-update", "POST")[0])).toEqual({
      update: { pubsub_id: "ps1", resources: { memory_mb: 512 } },
    });
    const dialog = await screen.findByRole("alertdialog");
    expect(
      within(dialog).getByText("Resources: cpu unlimited, memory unlimited → cpu unlimited, memory 512 MiB."),
    ).toBeTruthy();
  });

  it("STALE（票过期）→ 自动重预览：新票 + 漂移注记，确认仍须再按", async () => {
    previewResponses.push(
      { status: 200, body: { settings_changes: [{ key: "stream_max_memory", to: "256mb", class: "static" }], rollout: "replace", ticket: "tkt-old" } },
      { status: 200, body: { settings_changes: [{ key: "stream_max_memory", to: "256mb", class: "static" }], rollout: "replace", ticket: "tkt-new" } },
    );
    updateResponses.push(
      { status: 428, body: { code: "E_CONFIRMATION_STALE", message: "ticket expired" } },
      { status: 200, body: { pubsub: { id: "ps1" }, static_applied: ["stream_max_memory"] } },
    );
    renderEditor();
    fireEvent.change(await screen.findByLabelText("stream_max_memory"), { target: { value: "256" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Save and roll" }));

    await waitFor(() => expect(calls("/v1/pubsubs/preview-update", "POST")).toHaveLength(2));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/this is a fresh preview with a fresh ticket/)).toBeTruthy();
    expect(calls("/v1/pubsubs/ps1", "PATCH")).toHaveLength(1);
    fireEvent.click(within(dialog).getByRole("button", { name: "Save and roll" }));
    await waitFor(() => expect(calls("/v1/pubsubs/ps1", "PATCH")).toHaveLength(2));
    expect(bodyOf(calls("/v1/pubsubs/ps1", "PATCH")[1])).toMatchObject({ confirmation: { ticket: "tkt-new" } });
  });

  it("非 running：Save 禁用 + 诚实提示（更新在滚窗/未就绪被拒）", async () => {
    renderEditor({ ...pubsub, status: "pending" });
    fireEvent.change(await screen.findByLabelText("stream_max_file"), { target: { value: "2" } });
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Saving needs a running pubsub \(status: pending\)/)).toBeTruthy();
  });

  it("预演拒绝（受理位 E_INVALID_ARGUMENT）原样进结果盒（校验口径服务端同源）", async () => {
    previewResponses.push({
      status: 400,
      body: {
        code: "E_INVALID_ARGUMENT",
        message: 'settings: knob "stream_max_file": size must carry a unit (b, kb, mb, gb)',
      },
    });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("stream_max_file"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("[E_INVALID_ARGUMENT]");
    expect(alert.textContent).toContain("size must carry a unit");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
