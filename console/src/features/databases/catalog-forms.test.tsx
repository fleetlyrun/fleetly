import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState, type ReactNode } from "react";
import { setToken } from "@/lib/token";
import type { KnobValues } from "@/kit/data-engines";
import type { DatabaseRow, PreviewUpdateDatabasePayload } from "@/kit/databases";
import { CreateDatabaseDialog } from "./databases";
import { KnobFields } from "./knob-fields";
import {
  DatabaseSettingsEditor,
  draftOf,
  rolloutRestarts,
  updateInputFor,
  UpdatePreviewSummary,
} from "./settings-editor";

// 目录驱动表单钉测（B3，design §3.5/§6.2；ADR-0094 决策 4 / ADR-0095 决策
// 10）：① KnobFields 按 Category 分组、Kind→控件映射、class 徽标；② 创建对
// 话框引擎/版本/旋钮全从 Engine Catalog 渲染（Default 标注、弃用灰显 +
// Note、带 restore 源时同版本放行）；③ Settings tab 的 Save changes =
// 预览 → 确认分级（replace =「将触发滚动重启」/ zero_touch =「即时生效」）
// → 带票 PATCH；STALE 自动重预览。夹具引擎名刻意非真引擎词（kvstore /
// sqlish）——页面零引擎字面量的证据：渲染全靠目录载荷。

const CATALOG = {
  engines: [
    {
      family: "database",
      engine: "kvstore",
      dialect: "kvstore",
      port: 7000,
      versions: [{ id: "6", deprecated: true, note: "removal after two releases" }, { id: "7" }, { id: "8", default: true }],
      knobs: [
        { key: "persist", kind: "bool", default_value: "true", class: "static", category: "persistence" },
        { key: "mem_cap", kind: "size", class: "static", category: "memory" },
        { key: "evict", kind: "enum", enum_values: ["none", "lru"], default_value: "none", class: "static", category: "memory" },
      ],
    },
    {
      family: "database",
      engine: "sqlish",
      dialect: "sqlish",
      port: 5000,
      versions: [{ id: "1" }, { id: "2", default: true }],
      knobs: [
        { key: "buffers", kind: "size", default_value: "128mb", class: "static", category: "memory" },
        { key: "max_clients", kind: "int", min: "1", max: "1000", default_value: "100", class: "static", category: "connections" },
        { key: "work_area", kind: "size", default_value: "4mb", class: "dynamic", category: "memory" },
        { key: "idle_timeout", kind: "duration", default_value: "30s", class: "dynamic", category: "connections" },
      ],
    },
  ],
};

const SQLISH_KNOBS = CATALOG.engines[1]!.knobs;
const KVSTORE_KNOBS = CATALOG.engines[0]!.knobs;

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  };
}

const fetchMock = vi.fn();
// 预演/更新响应序列（按调用次序消费；空 = 缺省成功体）。
let previewResponses: Array<{ status: number; body: unknown }> = [];
let updateResponses: Array<{ status: number; body: unknown }> = [];

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

function stubFetch() {
  fetchMock.mockImplementation(async (path: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    if (path === "/v1/data-engines") return jsonResponse(CATALOG);
    if (path === "/v1/databases" && method === "POST") return jsonResponse({ id: "db-new" });
    if (path === "/v1/databases/preview-update" && method === "POST") {
      const next = previewResponses.shift() ?? { status: 200, body: { rollout: "zero_touch", ticket: "tkt-default" } };
      return jsonResponse(next.body, next.status);
    }
    if (path === "/v1/databases/db1" && method === "PATCH") {
      const next = updateResponses.shift() ?? { status: 200, body: { database: { id: "db1" } } };
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

// Radix Switch 在 <form> 内渲染 BubbleInput（useSize → ResizeObserver）——
// jsdom 盲区，沿用 timezone-combobox.test 的桩。
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

// 受控壳：KnobFields 的 values 由测试持有，onChange 流水同步记录。
function KnobHarness({ knobs, initial, onChange }: { knobs: typeof SQLISH_KNOBS; initial: KnobValues; onChange: (next: KnobValues) => void }) {
  const [values, setValues] = useState<KnobValues>(initial);
  return (
    <KnobFields
      knobs={knobs}
      values={values}
      onChange={(next) => {
        setValues(next);
        onChange(next);
      }}
    />
  );
}

describe("KnobFields（Category 分组 / Kind→控件 / class 徽标）", () => {
  it("按 Category 分组（首现序），class 徽标 instant/rolling 逐键在场", () => {
    render(<KnobFields knobs={SQLISH_KNOBS} values={{}} onChange={() => undefined} />);
    const legends = screen.getAllByText(/^(memory|connections)$/).map((node) => node.textContent);
    expect(legends).toEqual(["memory", "connections"]);
    // 2 static（buffers/max_clients）→ rolling；2 dynamic（work_area/idle_timeout）→ instant。
    expect(screen.getAllByText("rolling")).toHaveLength(2);
    expect(screen.getAllByText("instant")).toHaveLength(2);
    const workArea = document.querySelector('[data-knob="work_area"]') as HTMLElement;
    expect(within(workArea).getByText("instant")).toBeTruthy();
    const buffers = document.querySelector('[data-knob="buffers"]') as HTMLElement;
    expect(within(buffers).getByText("rolling")).toBeTruthy();
  });

  it("int = 数字输入（min/max 透传）；size = 数字 + 单位；duration = 文本 + 缺省占位", () => {
    render(<KnobFields knobs={SQLISH_KNOBS} values={{}} onChange={() => undefined} />);
    const maxClients = screen.getByLabelText("max_clients") as HTMLInputElement;
    expect(maxClients.type).toBe("number");
    expect(maxClients.min).toBe("1");
    expect(maxClients.max).toBe("1000");
    expect(maxClients.placeholder).toBe("100");
    expect((screen.getByLabelText("buffers") as HTMLInputElement).type).toBe("number");
    expect(screen.getByLabelText("buffers unit")).toBeTruthy();
    const idle = screen.getByLabelText("idle_timeout") as HTMLInputElement;
    expect(idle.type).toBe("text");
    expect(idle.placeholder).toBe("30s");
  });

  it("bool = 开关（显示引擎缺省、拨动即显式设置）；enum = 下拉（首项 = 引擎缺省）", () => {
    const onChange = vi.fn();
    render(<KnobHarness knobs={KVSTORE_KNOBS} initial={{}} onChange={onChange} />);
    const toggle = screen.getByRole("switch");
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    fireEvent.click(toggle);
    expect(onChange).toHaveBeenLastCalledWith({ persist: "false" });
    const evict = screen.getByLabelText("evict") as HTMLSelectElement;
    expect([...evict.options].map((option) => option.textContent)).toEqual(["engine default (none)", "none", "lru"]);
    fireEvent.change(evict, { target: { value: "lru" } });
    expect(onChange).toHaveBeenLastCalledWith({ persist: "false", evict: "lru" });
  });

  it("size 拆合：数值 + 单位产出受理口径值；Use default 移除键（回到引擎缺省）", () => {
    const onChange = vi.fn();
    render(<KnobHarness knobs={SQLISH_KNOBS} initial={{ max_clients: "200" }} onChange={onChange} />);
    fireEvent.change(screen.getByLabelText("buffers"), { target: { value: "256" } });
    expect(onChange).toHaveBeenLastCalledWith({ max_clients: "200", buffers: "256mb" });
    fireEvent.change(screen.getByLabelText("buffers unit"), { target: { value: "gb" } });
    expect(onChange).toHaveBeenLastCalledWith({ max_clients: "200", buffers: "256gb" });
    fireEvent.click(screen.getByRole("button", { name: "Use the engine default for max_clients" }));
    expect(onChange).toHaveBeenLastCalledWith({ buffers: "256gb" });
  });
});

describe("CreateDatabaseDialog（目录驱动：引擎 / 版本 / 旋钮）", () => {
  function renderDialog() {
    const { wrapper } = withClient();
    const onCreated = vi.fn();
    render(<CreateDatabaseDialog projectId="P1" open onOpenChange={() => undefined} onCreated={onCreated} />, { wrapper });
    return { onCreated };
  }

  it("引擎下拉 = 目录现值（无缺省引擎——显式选择）；选引擎后版本下拉 Default 置顶预选、弃用灰显 + Note", async () => {
    renderDialog();
    const engine = (await screen.findByLabelText("Engine")) as HTMLSelectElement;
    await waitFor(() => expect(engine.disabled).toBe(false));
    expect([...engine.options].map((option) => option.value)).toEqual(["", "kvstore", "sqlish"]);
    expect(screen.queryByLabelText("Version")).toBeNull();

    fireEvent.change(engine, { target: { value: "kvstore" } });
    const version = (await screen.findByLabelText("Version")) as HTMLSelectElement;
    expect(version.value).toBe("8");
    expect([...version.options].map((option) => option.textContent)).toEqual(["8 (default)", "7", "6 (deprecated)"]);
    expect([...version.options].find((option) => option.value === "6")?.disabled).toBe(true);
    expect(screen.getByText(/6 is deprecated — removal after two releases/)).toBeTruthy();
    // 旋钮区随引擎渲染（kvstore 的 bool 开关在场）。
    expect(screen.getByRole("switch")).toBeTruthy();
  });

  it("带 restore 源时弃用版可选（同版本恢复放行——受理位同口径）", async () => {
    renderDialog();
    const engine = (await screen.findByLabelText("Engine")) as HTMLSelectElement;
    await waitFor(() => expect(engine.disabled).toBe(false));
    fireEvent.change(engine, { target: { value: "kvstore" } });
    fireEvent.change(screen.getByPlaceholderText("bk_01j…"), { target: { value: "bk_01" } });
    const version = (await screen.findByLabelText("Version")) as HTMLSelectElement;
    await waitFor(() => expect([...version.options].find((option) => option.value === "6")?.disabled).toBe(false));
  });

  it("提交载荷 = 引擎 + 预选 Default 版本 + 已设置旋钮（未触旋钮不出键）+ 资源轴", async () => {
    const { onCreated } = renderDialog();
    const engine = (await screen.findByLabelText("Engine")) as HTMLSelectElement;
    await waitFor(() => expect(engine.disabled).toBe(false));
    fireEvent.change(engine, { target: { value: "sqlish" } });
    fireEvent.change(screen.getByPlaceholderText("orders-db"), { target: { value: "orders" } });
    fireEvent.change(await screen.findByLabelText("max_clients"), { target: { value: "300" } });
    fireEvent.change(screen.getByLabelText("Memory limit (MiB)"), { target: { value: "1024" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(calls("/v1/databases", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/databases", "POST")[0])).toEqual({
      project_id: "P1",
      name: "orders",
      engine: "sqlish",
      version: "2",
      settings: { max_clients: "300" },
      resources: { memory_mb: 1024 },
    });
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith("db-new"));
  });

  it("未选引擎即提交 = 客户端必填拦截（零请求）", async () => {
    renderDialog();
    const engine = (await screen.findByLabelText("Engine")) as HTMLSelectElement;
    await waitFor(() => expect(engine.disabled).toBe(false));
    fireEvent.change(screen.getByPlaceholderText("orders-db"), { target: { value: "orders" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText("Pick an engine from the engine catalog")).toBeTruthy();
    expect(calls("/v1/databases", "POST")).toHaveLength(0);
  });
});

describe("Settings tab 纯函数半边", () => {
  const row: DatabaseRow = {
    id: "db1",
    settings: { max_clients: "200" },
    resources: { memory_mb: 512 },
  };

  it("draftOf：行 → 表单期望态（缺席资源轴 = 空串 = 无限制）", () => {
    expect(draftOf(row)).toEqual({ settings: { max_clients: "200" }, cpuMillis: "", memoryMb: "512" });
    expect(draftOf(undefined)).toEqual({ settings: {}, cpuMillis: "", memoryMb: "" });
  });

  it("updateInputFor：只携改动面（未动面缺席 = 服务端以行值织票面摘要）；无改动 = null", () => {
    const base = draftOf(row);
    expect(updateInputFor("db1", base, base)).toBeNull();
    expect(updateInputFor("db1", base, { ...base, settings: { max_clients: "300" } })).toEqual({
      id: "db1",
      settings: { max_clients: "300" },
    });
    expect(updateInputFor("db1", base, { ...base, cpuMillis: "1000" })).toEqual({
      id: "db1",
      resources: { cpuMillis: "1000", memoryMb: "512" },
    });
    // "0" 与空串同为无限制：不算改动。
    expect(updateInputFor("db1", base, { ...base, cpuMillis: "0" })).toBeNull();
  });

  it("rolloutRestarts：zero_touch 不重启；replace 与未知值一律按重启披露", () => {
    expect(rolloutRestarts({ rollout: "zero_touch" })).toBe(false);
    expect(rolloutRestarts({ rollout: "replace" })).toBe(true);
    expect(rolloutRestarts({})).toBe(true);
  });

  it("UpdatePreviewSummary：逐键 diff 带 class 徽标；replace 披露滚动重启 + 分次保存提示；zero_touch 披露即时生效", () => {
    const replace: PreviewUpdateDatabasePayload = {
      settings_changes: [
        { key: "max_clients", from: "200", to: "300", class: "static" },
        { key: "work_area", to: "64mb", class: "dynamic" },
      ],
      rollout: "replace",
      ticket: "t",
    };
    const { unmount } = render(<UpdatePreviewSummary payload={replace} />);
    const maxClients = document.querySelector('[data-change="max_clients"]') as HTMLElement;
    expect(within(maxClients).getByText("200 → 300")).toBeTruthy();
    expect(within(maxClients).getByText("rolling")).toBeTruthy();
    const workArea = document.querySelector('[data-change="work_area"]') as HTMLElement;
    expect(within(workArea).getByText("(default) → 64mb")).toBeTruthy();
    expect(within(workArea).getByText("instant")).toBeTruthy();
    expect(screen.getByText(/Saving triggers a rolling restart/)).toBeTruthy();
    expect(screen.getByText(/every save that carries rolling changes restarts the carrier once/)).toBeTruthy();
    expect(screen.queryByText(/Takes effect immediately/)).toBeNull();
    unmount();

    render(<UpdatePreviewSummary payload={{ settings_changes: [{ key: "work_area", to: "64mb", class: "dynamic" }], rollout: "zero_touch" }} />);
    expect(screen.getByText(/Takes effect immediately/)).toBeTruthy();
    expect(screen.queryByText(/rolling restart/)).toBeNull();
  });
});

describe("DatabaseSettingsEditor（Save changes = 预览 → 分级确认 → 带票 PATCH）", () => {
  const database: DatabaseRow = {
    id: "db1",
    name: "orders",
    engine: "sqlish",
    version: "2",
    status: "running",
    settings: { max_clients: "200" },
  };

  function renderEditor(row: DatabaseRow = database) {
    const { wrapper } = withClient();
    render(<DatabaseSettingsEditor database={row} projectId="P1" />, { wrapper });
  }

  it("目录驱动渲染行值；无改动时 Save/Discard 禁用", async () => {
    renderEditor();
    const maxClients = (await screen.findByLabelText("max_clients")) as HTMLInputElement;
    expect(maxClients.value).toBe("200");
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Discard changes" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("instant 全集（zero_touch）：确认框披露「即时生效」，确认带票发 PATCH（只携改动面 = settings 全集）", async () => {
    previewResponses.push({
      status: 200,
      body: {
        settings_changes: [{ key: "work_area", to: "64mb", class: "dynamic" }],
        dynamic_keys: ["work_area"],
        rollout: "zero_touch",
        ticket: "tkt-1",
        ticket_expires_in: 600,
      },
    });
    updateResponses.push({ status: 200, body: { database: { id: "db1" }, dynamic_applied: ["work_area"] } });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("work_area"), { target: { value: "64" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(calls("/v1/databases/preview-update", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/databases/preview-update", "POST")[0])).toEqual({
      update: { database_id: "db1", settings: { values: { max_clients: "200", work_area: "64mb" } } },
    });
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Takes effect immediately/)).toBeTruthy();
    expect(within(dialog).queryByText(/rolling restart/)).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls("/v1/databases/db1", "PATCH")).toHaveLength(1));
    expect(bodyOf(calls("/v1/databases/db1", "PATCH")[0])).toEqual({
      settings: { values: { max_clients: "200", work_area: "64mb" } },
      confirmation: { ticket: "tkt-1" },
    });
  });

  it("含 rolling 键（replace）：确认框披露「将触发滚动重启」+ 分次保存提示，确认钮 = Save and roll", async () => {
    previewResponses.push({
      status: 200,
      body: {
        settings_changes: [{ key: "max_clients", from: "200", to: "300", class: "static" }],
        static_keys: ["max_clients"],
        rollout: "replace",
        ticket: "tkt-roll",
        ticket_expires_in: 600,
      },
    });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("max_clients"), { target: { value: "300" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Saving triggers a rolling restart of the database carrier/)).toBeTruthy();
    expect(within(dialog).getByText(/every save that carries rolling changes restarts the carrier once/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save and roll" }));
    await waitFor(() => expect(calls("/v1/databases/db1", "PATCH")).toHaveLength(1));
    expect(bodyOf(calls("/v1/databases/db1", "PATCH")[0])).toEqual({
      settings: { values: { max_clients: "300" } },
      confirmation: { ticket: "tkt-roll" },
    });
  });

  it("资源面改动单独在场（settings 缺席——未动面由服务端行值织票面）", async () => {
    previewResponses.push({ status: 200, body: { resources_to: { cpu_millis: 1000 }, rollout: "replace", ticket: "tkt-r" } });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("CPU limit (millicores)"), { target: { value: "1000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls("/v1/databases/preview-update", "POST")).toHaveLength(1));
    expect(bodyOf(calls("/v1/databases/preview-update", "POST")[0])).toEqual({
      update: { database_id: "db1", resources: { cpu_millis: 1000 } },
    });
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Resources: cpu unlimited, memory unlimited → cpu 1000 millicores, memory unlimited.")).toBeTruthy();
  });

  it("STALE（票过期）→ 自动重预览：新票 + 漂移注记，确认仍须再按", async () => {
    previewResponses.push(
      { status: 200, body: { settings_changes: [{ key: "work_area", to: "64mb", class: "dynamic" }], rollout: "zero_touch", ticket: "tkt-old" } },
      { status: 200, body: { settings_changes: [{ key: "work_area", to: "64mb", class: "dynamic" }], rollout: "zero_touch", ticket: "tkt-new" } },
    );
    updateResponses.push(
      { status: 428, body: { code: "E_CONFIRMATION_STALE", message: "ticket expired" } },
      { status: 200, body: { database: { id: "db1" }, dynamic_applied: ["work_area"] } },
    );
    renderEditor();
    fireEvent.change(await screen.findByLabelText("work_area"), { target: { value: "64" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(calls("/v1/databases/preview-update", "POST")).toHaveLength(2));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/this is a fresh preview with a fresh ticket/)).toBeTruthy();
    // 重预览不自动确认：PATCH 仍只有首发一次。
    expect(calls("/v1/databases/db1", "PATCH")).toHaveLength(1);
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls("/v1/databases/db1", "PATCH")).toHaveLength(2));
    expect(bodyOf(calls("/v1/databases/db1", "PATCH")[1])).toMatchObject({ confirmation: { ticket: "tkt-new" } });
  });

  it("非 running：Save 禁用 + 诚实提示（更新在滚窗/未就绪被拒）", async () => {
    renderEditor({ ...database, status: "pending" });
    fireEvent.change(await screen.findByLabelText("max_clients"), { target: { value: "300" } });
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Saving needs a running database \(status: pending\)/)).toBeTruthy();
  });

  it("预演拒绝（受理位 E_INVALID_ARGUMENT）原样进结果盒（校验口径服务端同源）", async () => {
    previewResponses.push({
      status: 400,
      body: { code: "E_INVALID_ARGUMENT", message: 'settings: knob "max_clients": value 5000 is outside the allowed range [1, 1000]' },
    });
    renderEditor();
    fireEvent.change(await screen.findByLabelText("max_clients"), { target: { value: "5000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("[E_INVALID_ARGUMENT]");
    expect(alert.textContent).toContain("outside the allowed range [1, 1000]");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});
