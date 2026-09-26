// S3 设置卡测试（E3-8，设计 §5.5 锚点契约）：三模式呈现与切换、secret 指纹
// 形态（读面永不回填明文）、公网开关门禁（非 rustfs 禁用）、诚实标注常驻、
// 候选配置先测后存（结构化步骤结果）+ 失败信封 + PUT 全量保存语义。
//
// 交互纪律：radix Select 的跨用例 jsdom 状态会吞后续用例的开启/选中点击
//（AppLogsPage.test.tsx 注记同源问题）——本文件只在一个用例里驱动 Select
// UI；其余用例的表单模式直接由服务端已存设置（fetch 桩返回值）给定，组件
// 初始化即应回放该模式。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { S3SettingsCard } from "@/components/s3-settings-card";
import { setToken } from "@/api/client";

// jsdom 缺 radix Select 依赖的 pointer capture / scrollIntoView API
//（与 AppLogsPage.test.tsx 同款注入）。
if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
  Element.prototype.releasePointerCapture = () => undefined;
  Element.prototype.setPointerCapture = () => undefined;
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => undefined;
}

const STORED_EXTERNAL = {
  mode: "external",
  endpoint_url: "https://s3.example.test",
  region: "us-east-1",
  bucket: "fleetly-backups",
  access_key_id: "AKIDEXAMPLE",
  secret_fingerprint: "ab12cd34",
  path_style: true,
};

const STORED_RUSTFS = {
  mode: "rustfs",
  endpoint_url: "http://rustfs:9000",
  bucket: "fleetly",
  access_key_id: "fleetly",
  secret_fingerprint: "cd34ef90",
  public_exposed: false,
};

const PROBE_OK = {
  ok: true,
  endpoint_url: "https://s3.example.test",
  region: "us-east-1",
  bucket: "fleetly-backups",
  path_style: true,
  steps: [
    { step: "put", ok: true, duration_ms: "12" },
    { step: "get", ok: true, duration_ms: "3" },
    { step: "delete", ok: true, duration_ms: "2" },
  ],
};

type FetchLog = { url: string; method: string; body: Record<string, unknown> };

/**
 * 组装 fetch 桩：记录每次调用（url/method/body），按 URL 形态回设定响应。
 * isPlatformAdmin=false 时按 viewer 视角（S3 读写面整体 admin scope——
 * 2026-09-25 走查：viewer 只见整卡只读说明，且不发 GET /system/s3）。
 */
function stubS3Fetch(opts?: {
  settings?: Record<string, unknown>;
  testResponse?: { status: number; body: Record<string, unknown> };
  saveResponse?: Record<string, unknown>;
  isPlatformAdmin?: boolean;
}) {
  const log: FetchLog[] = [];
  const fetchMock = vi.fn().mockImplementation(
    (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      let body: Record<string, unknown> = {};
      if (typeof init?.body === "string") {
        try {
          body = JSON.parse(init.body) as Record<string, unknown>;
        } catch {
          body = {};
        }
      }
      log.push({ url, method, body });

      if (url.endsWith("/auth/me")) {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () =>
            Promise.resolve({
              user: {
                id: "01U1",
                email: "f@t.test",
                is_platform_admin: opts?.isPlatformAdmin ?? true,
              },
              teams: [
                { team_id: "01TEAM", team_slug: "acme", team_name: "Acme", role: "owner" },
              ],
              project_overrides: [],
            }),
        });
      }
      if (url.includes("/system/s3:test")) {
        const t = opts?.testResponse ?? { status: 200, body: { result: PROBE_OK } };
        return Promise.resolve({
          ok: t.status === 200,
          status: t.status,
          statusText: "",
          json: () => Promise.resolve(t.body),
        });
      }
      if (url.includes("/system/s3") && method === "PUT") {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () =>
            Promise.resolve({ settings: opts?.saveResponse ?? STORED_EXTERNAL }),
        });
      }
      // GET /system/s3
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            settings: opts?.settings ?? STORED_EXTERNAL,
          }),
      });
    },
  );
  return { fetchMock, log };
}

function renderCard() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <S3SettingsCard />
    </QueryClientProvider>,
  );
}

describe("S3SettingsCard (E3-8)", () => {
  it("renders stored external settings: secret shows fingerprint, public toggle gated off", async () => {
    setToken("flt_test");
    const { fetchMock } = stubS3Fetch();
    vi.stubGlobal("fetch", fetchMock);

    renderCard();

    await screen.findByTestId("s3-settings-card");
    // 外部模式字段随服务端设置到达后渲染。
    await screen.findByLabelText("Secret access key");
    // secret 读面：指纹形态，永不回填明文。
    const secret = screen.getByLabelText("Secret access key");
    expect(secret).toHaveValue("");
    expect(secret.getAttribute("placeholder")).toContain("ab12cd34");
    expect(secret.getAttribute("placeholder")).toContain("never read back");
    // 公网开关门禁：非 rustfs 模式禁用。
    expect(screen.getByTestId("s3-public-toggle")).toBeDisabled();
    // 诚实标注只属 rustfs 模式：external 模式不出现。
    expect(screen.queryByTestId("s3-honesty-note")).not.toBeInTheDocument();
    // 路径寻址回填。
    expect(screen.getByTestId("s3-path-style-toggle")).toBeChecked();
  });

  it("renders stored rustfs settings: honesty note is persistent and public toggle unlocks", async () => {
    setToken("flt_test");
    const { fetchMock } = stubS3Fetch({ settings: STORED_RUSTFS });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("s3-settings-card");

    // 诚实口径常驻（非可关闭提示）， D-S3-8 语义可读。
    const note = await screen.findByTestId("s3-honesty-note");
    expect(note.textContent).toContain("convenience layer");
    expect(note.textContent).toContain("not disaster recovery");
    // 公网开关解锁并可开。
    const toggle = screen.getByTestId("s3-public-toggle");
    expect(toggle).toBeEnabled();
    fireEvent.click(toggle);
    expect(toggle).toBeChecked();
  });

  it("renders unset mode without external credential fields", async () => {
    setToken("flt_test");
    const { fetchMock } = stubS3Fetch({ settings: { mode: "unset" } });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("s3-settings-card");

    await waitFor(() =>
      expect(screen.getByTestId("s3-public-toggle")).toBeDisabled(),
    );
    expect(screen.queryByLabelText("Endpoint URL")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Secret access key")).not.toBeInTheDocument();
  });

  it("tests the candidate configuration before saving and renders structured steps", async () => {
    setToken("flt_test");
    const { fetchMock, log } = stubS3Fetch();
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByLabelText("Endpoint URL");

    // 候选改动：换 endpoint 后直接测（不保存）。
    const endpoint = screen.getByLabelText("Endpoint URL");
    fireEvent.change(endpoint, { target: { value: "https://alt.example.test" } });
    fireEvent.click(screen.getByTestId("s3-test-button"));

    await waitFor(() =>
      expect(screen.getByTestId("s3-test-result")).toBeInTheDocument(),
    );
    // 探针请求带候选配置（先测后存）。
    const probe = log.find((e) => e.url.includes("/system/s3:test"));
    expect(probe?.method).toBe("POST");
    expect(probe?.body.endpoint_url).toBe("https://alt.example.test");
    // 结构化步骤：各步 ok + 耗时。
    const result = screen.getByTestId("s3-test-result");
    expect(result.textContent).toContain("Connection OK");
    expect(result.textContent).toContain("put");
    expect(result.textContent).toContain("12 ms");
    expect(result.textContent).toContain("delete");
  });

  it("renders the E_S3_TEST_FAILED envelope with suggestion when the probe fails", async () => {
    setToken("flt_test");
    const { fetchMock } = stubS3Fetch({
      testResponse: {
        status: 400,
        body: {
          code: "E_S3_TEST_FAILED",
          message: "S3 probe failed at step put",
          suggestion: "Check the endpoint URL and credentials.",
        },
      },
    });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByLabelText("Endpoint URL");
    fireEvent.click(screen.getByTestId("s3-test-button"));

    await waitFor(() =>
      expect(screen.getByText(/E_S3_TEST_FAILED/)).toBeInTheDocument(),
    );
    expect(screen.getByText(/Check the endpoint URL and credentials\./)).toBeInTheDocument();
  });

  it("saves with PUT full-replace semantics (blank secret stays blank on the wire)", async () => {
    setToken("flt_test");
    const { fetchMock, log } = stubS3Fetch({
      saveResponse: { ...STORED_EXTERNAL, secret_fingerprint: "ef901234" },
    });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByLabelText("Endpoint URL");

    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(screen.getByText(/Settings saved/)).toBeInTheDocument(),
    );
    const save = log.find((e) => e.url.includes("/system/s3") && e.method === "PUT");
    expect(save).toBeTruthy();
    expect(save?.body.mode).toBe("external");
    expect(save?.body.endpoint_url).toBe("https://s3.example.test");
    // PUT 全量语义：secret 留空即清除（上行空串，无「保留旧值」旁路）。
    expect(save?.body.secret_access_key).toBe("");
  });

  it("switches mode via the select: unset hides credential fields, rustfs shows the note", async () => {
    setToken("flt_test");
    const { fetchMock } = stubS3Fetch();
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByLabelText("Endpoint URL");

    // 唯一驱动 Select UI 的用例（见文件头交互纪律）：全程 fireEvent。
    fireEvent.keyDown(screen.getByTestId("s3-mode-select"), { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("option", { name: "Not configured" }));
    await waitFor(() =>
      expect(screen.queryByLabelText("Endpoint URL")).not.toBeInTheDocument(),
    );
    expect(screen.getByTestId("s3-public-toggle")).toBeDisabled();

    fireEvent.keyDown(screen.getByTestId("s3-mode-select"), { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("option", { name: "Managed RustFS" }));
    await waitFor(() =>
      expect(screen.getByTestId("s3-honesty-note")).toBeInTheDocument(),
    );
    expect(screen.getByTestId("s3-public-toggle")).toBeEnabled();
  });

  it("viewer（写面门，2026-09-25 走查）：整卡替换为只读说明，不发 GET /system/s3", async () => {
    setToken("flt_test");
    const { fetchMock, log } = stubS3Fetch({ isPlatformAdmin: false });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();

    const note = await screen.findByTestId("s3-settings-readonly-note");
    expect(note).toHaveTextContent("Platform administrator required.");
    // 整卡替换：表单/按钮/读面字段一概不渲染。
    expect(screen.queryByTestId("s3-mode-select")).not.toBeInTheDocument();
    expect(screen.queryByTestId("s3-test-button")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
    // 读面即 admin scope：不发 GET /system/s3（enabled:false，零 403 噪音）。
    expect(log.some((e) => e.url.includes("/system/s3"))).toBe(false);
  });
});
