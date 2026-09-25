// ACME 设置卡测试（B 线 W5-S3，D-V3W5-3/D-V3W5-4）：凭证指纹形态（读面
// 永不回填明文、留空保留语义提示）、通配开关门禁（provider 未配/base_domain
// 缺失禁用）、通配域集展示、候选凭证先测后存（结构化步骤结果）+ 失败信封。
//
// 交互纪律：radix Select 的跨用例 jsdom 状态会吞后续用例的开启/选中点击
//（s3-settings-card.test.tsx 注记同源问题）——本文件不驱动 Select UI；
// 表单 provider 直接由服务端已存设置（fetch 桩返回值）给定。

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { AcmeSettingsCard } from "@/components/acme-settings-card";
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

const STORED_DNSPOD = {
  dns_provider: "dnspod",
  credentials_fingerprint: "ab12cd34",
  wildcard: false,
  base_domain: "example.test",
};

const STORED_DNSPOD_WILDCARD = {
  dns_provider: "dnspod",
  credentials_fingerprint: "ab12cd34",
  wildcard: true,
  base_domain: "example.test",
  wildcard_domains: [
    "*.example.test",
    "console.example.test",
    "ctrl.example.test",
    "registry.example.test",
  ],
};

const STORED_NONE = {
  dns_provider: "none",
  wildcard: false,
  base_domain: "example.test",
};

const PROBE_OK = {
  ok: true,
  dns_provider: "dnspod",
  record_name: "_acme-challenge-test.example.test",
  steps: [
    { step: "create", ok: true, duration_ms: "45" },
    { step: "delete", ok: true, duration_ms: "20" },
  ],
};

type FetchLog = { url: string; method: string; body: Record<string, unknown> };

/**
 * 组装 fetch 桩：记录每次调用（url/method/body），按 URL 形态回设定响应。
 */
function stubAcmeFetch(opts?: {
  settings?: Record<string, unknown>;
  testResponse?: { status: number; body: Record<string, unknown> };
  saveResponse?: Record<string, unknown>;
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

      if (url.includes("/system/acme/dns:test")) {
        const t = opts?.testResponse ?? { status: 200, body: { result: PROBE_OK } };
        return Promise.resolve({
          ok: t.status === 200,
          status: t.status,
          statusText: "",
          json: () => Promise.resolve(t.body),
        });
      }
      if (url.includes("/system/acme") && method === "PUT") {
        return Promise.resolve({
          ok: true,
          status: 200,
          statusText: "",
          json: () =>
            Promise.resolve({ settings: opts?.saveResponse ?? STORED_DNSPOD }),
        });
      }
      // GET /system/acme
      return Promise.resolve({
        ok: true,
        status: 200,
        statusText: "",
        json: () =>
          Promise.resolve({
            settings: opts?.settings ?? STORED_DNSPOD,
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
      <AcmeSettingsCard />
    </QueryClientProvider>,
  );
}

/**
 * 等服务端设置到达（指纹进入 placeholder——组件初始化回放已存形态）：
 * 在此之前的交互会命中组件的 dirty 竞态保护（表单不被迟到回读覆盖），
 * provider 停留缺省 none——先等后动是断言前置。
 */
async function awaitStoredLoaded(marker: string) {
  await waitFor(() =>
    expect(
      screen.getByTestId("acme-token-input")?.getAttribute("placeholder"),
    ).toContain(marker),
  );
}

describe("AcmeSettingsCard (W5-S3)", () => {
  it("renders stored provider settings: token shows fingerprint placeholder, never a value", async () => {
    setToken("flt_test");
    const { fetchMock } = stubAcmeFetch();
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("acme-settings-card");
    await screen.findByTestId("acme-token-input");
    // 先等已存设置回放到 placeholder（本文件其余用例同款前置），再断言。
    await awaitStoredLoaded("ab12cd34");

    // 凭证读面：恒空值 + 指纹 placeholder（write-only 形态）。
    const token = screen.getByTestId("acme-token-input");
    expect(token).toHaveValue("");
    expect(token.getAttribute("placeholder")).toContain("ab12cd34");
    expect(token.getAttribute("placeholder")).toContain("never read back");
    // 留空保留语义的提示在案。hint 走 waitFor 终态一致：全量并发下
    // 直接读会命中「query 数据已到（placeholder 已含指纹）、effect
    // 同步表单未落」的瞬态窗口（W5 收官与 W6 回归各 flake 一次）。
    await waitFor(() =>
      expect(screen.getByTestId("acme-token-hint").textContent).toContain(
        "Leaving this blank keeps the stored token",
      ),
    );
    // wildcard 关：开关未勾选（provider 就位可点）。
    expect(screen.getByTestId("acme-wildcard-toggle")).toBeEnabled();
    expect(screen.getByTestId("acme-wildcard-toggle")).not.toBeChecked();
    // 域集展示缺席（wildcard off）。
    expect(screen.queryByTestId("acme-wildcard-domains")).not.toBeInTheDocument();
  });

  it("disables token input and wildcard toggle when no provider is configured", async () => {
    setToken("flt_test");
    const { fetchMock } = stubAcmeFetch({ settings: STORED_NONE });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("acme-settings-card");

    await waitFor(() =>
      expect(screen.getByTestId("acme-token-input")).toBeDisabled(),
    );
    // base_domain 在位但 provider=none：通配开关禁用（联动门的前端形态）。
    expect(screen.getByTestId("acme-wildcard-toggle")).toBeDisabled();
    // 探针按钮同步禁用（无 provider 无可测）。
    expect(screen.getByTestId("acme-test-button")).toBeDisabled();
  });

  it("shows the derived wildcard domain set when wildcard is on", async () => {
    setToken("flt_test");
    const { fetchMock } = stubAcmeFetch({ settings: STORED_DNSPOD_WILDCARD });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("acme-settings-card");

    const panel = await screen.findByTestId("acme-wildcard-domains");
    expect(panel.textContent).toContain("*.example.test");
    expect(panel.textContent).toContain("console.example.test");
    expect(panel.textContent).toContain("ctrl.example.test");
    expect(panel.textContent).toContain("registry.example.test");
    expect(panel.textContent).toContain("per-app HTTP-01 issuance stays for custom domains");
    expect(screen.getByTestId("acme-wildcard-toggle")).toBeChecked();
  });

  it("probes with the candidate token (blank token probes the stored credentials) and renders structured steps", async () => {
    setToken("flt_test");
    const { fetchMock, log } = stubAcmeFetch();
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("acme-token-input");
    await awaitStoredLoaded("ab12cd34");

    // 候选凭证：输入 token 后直接测（不保存）。
    fireEvent.change(screen.getByTestId("acme-token-input"), {
      target: { value: "1,candidate-token" },
    });
    fireEvent.click(screen.getByTestId("acme-test-button"));

    await waitFor(() =>
      expect(screen.getByTestId("acme-test-result")).toBeInTheDocument(),
    );
    const probe = log.find((e) => e.url.includes("/system/acme/dns:test"));
    expect(probe?.method).toBe("POST");
    expect(probe?.body.dns_provider).toBe("dnspod");
    expect(probe?.body.api_token).toBe("1,candidate-token");
    const result = screen.getByTestId("acme-test-result");
    expect(result.textContent).toContain("Provider OK");
    expect(result.textContent).toContain("create");
    expect(result.textContent).toContain("delete");
    expect(result.textContent).toContain("_acme-challenge-test.example.test");
  });

  it("renders the E_ACME_DNS_TEST_FAILED envelope with suggestion when the probe fails", async () => {
    setToken("flt_test");
    const { fetchMock } = stubAcmeFetch({
      testResponse: {
        status: 503,
        body: {
          code: "E_ACME_DNS_TEST_FAILED",
          message: "dns provider test failed at step create",
          suggestion: "Fix the provider credentials per the failed probe step and test again.",
        },
      },
    });
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("acme-token-input");
    await awaitStoredLoaded("ab12cd34");
    fireEvent.click(screen.getByTestId("acme-test-button"));

    await waitFor(() =>
      expect(screen.getByText(/E_ACME_DNS_TEST_FAILED/)).toBeInTheDocument(),
    );
    expect(
      screen.getByText(/Fix the provider credentials per the failed probe step/),
    ).toBeInTheDocument();
  });

  it("saves with the blank-token keep semantics and re-syncs the form from the response", async () => {
    setToken("flt_test");
    const { fetchMock, log } = stubAcmeFetch();
    vi.stubGlobal("fetch", fetchMock);

    renderCard();
    await screen.findByTestId("acme-token-input");
    await awaitStoredLoaded("ab12cd34");

    // 输入候选 token + 开 wildcard 后保存。
    fireEvent.change(screen.getByTestId("acme-token-input"), {
      target: { value: "1,new-token" },
    });
    fireEvent.click(screen.getByTestId("acme-wildcard-toggle"));
    fireEvent.click(screen.getByText("Save"));

    await waitFor(() =>
      expect(screen.getByText(/Settings saved/)).toBeInTheDocument(),
    );
    const put = log.find((e) => e.method === "PUT");
    expect(put?.body.dns_provider).toBe("dnspod");
    expect(put?.body.api_token).toBe("1,new-token");
    expect(put?.body.wildcard).toBe(true);
    // 保存即新基线：表单 token 清空（响应已存形态回放）。
    await waitFor(() =>
      expect(screen.getByTestId("acme-token-input")).toHaveValue(""),
    );
  });
});
