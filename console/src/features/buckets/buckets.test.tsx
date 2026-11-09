import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { setToken } from "@/lib/token";
import type { BucketStoreRow } from "@/kit/buckets";
import { CreateBucketStoreDialog } from "./buckets";
import { BucketStoreSettingsEditor } from "./bucket-settings";

// buckets 姐妹页钉测（B5，design §6.2/§3.5——pubsubs.test 形态）：① 创建
// 对话框目录驱动（bucketstore 族过滤、单版 Default、**零旋钮即无旋钮区**、
// 资源面、license 披露、**无 restore 源**）；② Settings tab 的 Save changes
// = 预览 → 分级确认（零旋钮族：资源变更即 replace =「将触发滚动重启」）→
// 带票 PATCH（bucket_store_id 键）；STALE 自动重预览。夹具引擎名刻意非真
// 引擎词（objkv）——页面零引擎硬编码字面量的证据：渲染全靠目录载荷。

const CATALOG = {
  engines: [
    {
      kind: "pubsub",
      engine: "msgkv",
      dialect: "msgkv",
      port: 4222,
      versions: [{ id: "2.11", default: true }],
      knobs: [{ key: "stream_max_file", kind: "size", class: "static", category: "persistence" }],
    },
    {
      kind: "bucketstore",
      engine: "objkv",
      dialect: "s3",
      port: 9000,
      license_note: "AGPLv3 (network clause covers platform-hosted deployments)",
      versions: [{ id: "2026-10", default: true }],
      knobs: [],
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
    if (path === "/v1/bucket-stores" && method === "POST") return jsonResponse({ id: "bs-new" });
    if (path === "/v1/bucket-stores/preview-update" && method === "POST") {
      const next = previewResponses.shift() ?? { status: 200, body: { rollout: "replace", ticket: "tkt-default" } };
      return jsonResponse(next.body, next.status);
    }
    if (path === "/v1/bucket-stores/bs1" && method === "PATCH") {
      const next = updateResponses.shift() ?? { status: 200, body: { bucket_store: { id: "bs1" } } };
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

const bucketStore: BucketStoreRow = {
  id: "bs1",
  name: "assets",
  engine: "objkv",
  version: "2026-10",
  status: "running",
};

describe("CreateBucketStoreDialog（目录驱动——bucketstore 族过滤 + 单版 + 零旋钮）", () => {
  it("引擎下拉只列目录 bucketstore 族（pubsub 族引擎不进表单）；dialect 异名露出", async () => {
    const { wrapper } = withClient();
    render(<CreateBucketStoreDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    const select = (await screen.findByLabelText("Engine")) as HTMLSelectElement;
    const options = [...select.querySelectorAll("option")].map((option) => option.value).filter(Boolean);
    expect(options).toEqual(["objkv"]);
    expect(options).not.toContain("msgkv");
    const label = [...select.querySelectorAll("option")].find((option) => option.value === "objkv")?.textContent;
    expect(label).toContain("(s3 dialect)");
  });

  it("选引擎即版本 Default + 资源面出现；零旋钮 = 无旋钮区；无 restore 源；license 披露", async () => {
    const { wrapper } = withClient();
    render(<CreateBucketStoreDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    fireEvent.change(await screen.findByLabelText("Engine"), { target: { value: "objkv" } });
    const version = (await screen.findByLabelText("Version")) as HTMLSelectElement;
    expect(version.value).toBe("2026-10");
    expect([...version.querySelectorAll("option")].map((option) => option.textContent)).toEqual(["2026-10 (default)"]);
    expect(screen.getByLabelText("CPU limit (millicores)")).toBeTruthy();
    expect(screen.queryByText("Engine knobs (optional)")).toBeNull();
    expect(screen.queryByLabelText(/Restore from backup/)).toBeNull();
    expect(screen.getByText(/AGPLv3/)).toBeTruthy();
    expect(screen.getByText(/No backup face for this kind/)).toBeTruthy();
  });

  it("create 提交载荷：目录值（version/resources）；零旋钮 settings 不进载荷", async () => {
    const { wrapper } = withClient();
    render(<CreateBucketStoreDialog projectId="P1" open onOpenChange={() => {}} />, { wrapper });
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "media" } });
    fireEvent.change(await screen.findByLabelText("Engine"), { target: { value: "objkv" } });
    fireEvent.change(screen.getByLabelText("CPU limit (millicores)"), { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(calls("/v1/bucket-stores", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/bucket-stores", "POST")[0])).toEqual({
      project_id: "P1",
      name: "media",
      engine: "objkv",
      version: "2026-10",
      resources: { cpu_millis: 500 },
    });
  });
});

describe("BucketStoreSettingsEditor（Save changes = 预览 → replace 确认 → 带票 PATCH）", () => {
  function renderEditor(row: BucketStoreRow = bucketStore) {
    const { wrapper } = withClient();
    render(<BucketStoreSettingsEditor bucketStore={row} projectId="P1" />, { wrapper });
  }

  it("零旋钮族：只渲染资源面；无改动时 Save/Discard 禁用", async () => {
    renderEditor();
    expect(await screen.findByLabelText("Memory limit (MiB)")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Discard changes" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("资源变更：预演 replace——确认框披露「将触发滚动重启」，带票 PATCH 用 bucket_store_id 键", async () => {
    previewResponses.push({
      status: 200,
      body: { resources_to: { memory_mb: 512 }, rollout: "replace", ticket: "tkt-roll", ticket_expires_in: 600 },
    });
    updateResponses.push({ status: 200, body: { bucket_store: { id: "bs1" }, static_applied: [] } });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("Memory limit (MiB)"), { target: { value: "512" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(calls("/v1/bucket-stores/preview-update", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/bucket-stores/preview-update", "POST")[0])).toEqual({
      update: { bucket_store_id: "bs1", resources: { memory_mb: 512 } },
    });
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Saving triggers a rolling restart of the carrier/)).toBeTruthy();
    expect(
      within(dialog).getByText("Resources: cpu unlimited, memory unlimited → cpu unlimited, memory 512 MiB."),
    ).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save and roll" }));

    await waitFor(() => expect(calls("/v1/bucket-stores/bs1", "PATCH")).toHaveLength(1));
    expect(bodyOf(calls("/v1/bucket-stores/bs1", "PATCH")[0])).toEqual({
      resources: { memory_mb: 512 },
      confirmation: { ticket: "tkt-roll" },
    });
  });

  it("STALE（票过期）→ 自动重预览：新票 + 漂移注记，确认仍须再按", async () => {
    previewResponses.push(
      { status: 200, body: { resources_to: { cpu_millis: 750 }, rollout: "replace", ticket: "tkt-old" } },
      { status: 200, body: { resources_to: { cpu_millis: 750 }, rollout: "replace", ticket: "tkt-new" } },
    );
    updateResponses.push(
      { status: 428, body: { code: "E_CONFIRMATION_STALE", message: "ticket expired" } },
      { status: 200, body: { bucket_store: { id: "bs1" } } },
    );
    renderEditor();
    fireEvent.change(await screen.findByLabelText("CPU limit (millicores)"), { target: { value: "750" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Save and roll" }));

    await waitFor(() => expect(calls("/v1/bucket-stores/preview-update", "POST")).toHaveLength(2));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/this is a fresh preview with a fresh ticket/)).toBeTruthy();
    expect(calls("/v1/bucket-stores/bs1", "PATCH")).toHaveLength(1);
    fireEvent.click(within(dialog).getByRole("button", { name: "Save and roll" }));
    await waitFor(() => expect(calls("/v1/bucket-stores/bs1", "PATCH")).toHaveLength(2));
    expect(bodyOf(calls("/v1/bucket-stores/bs1", "PATCH")[1])).toMatchObject({ confirmation: { ticket: "tkt-new" } });
  });

  it("非 running：Save 禁用 + 诚实提示（更新在滚窗/未就绪被拒）", async () => {
    renderEditor({ ...bucketStore, status: "pending" });
    fireEvent.change(await screen.findByLabelText("CPU limit (millicores)"), { target: { value: "750" } });
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Saving needs a running bucket store \(status: pending\)/)).toBeTruthy();
  });
});
