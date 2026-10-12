import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { AppSpecTab } from "./app-spec";
import { setToken } from "@/lib/token";

// Spec tab（ADR-0071 配置可观测面）：三段式渲染——生效锚（缺省寻址 =
// /v1/apps/{id}/workloads 无 seq）、冻结全文（GetRevision）、在服观测
//（/workloads/live）；"最新冻结 ≠ 当前生效"提示条的分野锚。

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>();
  return { ...actual, useNavigate: () => navigateMock };
});

function withClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> };
}

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  navigateMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
  setToken("flt_test_token");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setToken("");
});

const VIEW = {
  revision: { seq: "2", digest: "sha256:eff", created_at: "t", source_kind: "SOURCE_KIND_IMAGE" },
  workloads: [
    {
      id: "A1-web",
      process: "web",
      image: "nginx:1.27",
      replicas: "3",
      command: ["nginx", "-g", "daemon off;"],
      env: { ROLE: "edge" },
      ports: [{ port: 8080, protocol: "PROTOCOL_HTTP" }],
      networks: ["default"],
      addressing: ["web"],
    },
  ],
};

function stubFetch(opts?: { latestFrozenSeq?: number; live?: unknown }) {
  const latest = opts?.latestFrozenSeq ?? 2;
  fetchMock.mockImplementation(async (path: string) => {
    if (path === "/v1/apps/A1/workloads") {
      return new Response(JSON.stringify(VIEW), { status: 200 });
    }
    if (path === "/v1/apps/A1/workloads/live") {
      return new Response(JSON.stringify({ observations: opts?.live ?? [] }), { status: 200 });
    }
    if (path.startsWith("/v1/revisions/A1/2")) {
      return new Response(JSON.stringify({ spec: { processes: [{ name: "web" }] } }), { status: 200 });
    }
    if (path.startsWith("/v1/revisions")) {
      return new Response(JSON.stringify({ revisions: [{ id: "r1", seq: "1", digest: "d" }, { id: "r2", seq: String(latest), digest: "d" }] }), { status: 200 });
    }
    return new Response(JSON.stringify({}), { status: 404 });
  });
}

describe("AppSpecTab", () => {
  it("renders the three observability layers from the effective revision", async () => {
    stubFetch({ live: [{ workload_id: "A1-web", generation: "4", image: "nginx:1.27", replicas: "3", state: "running" }] });
    const { wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" />, { wrapper });

    await waitFor(() => expect(screen.getByText("R2")).toBeTruthy());
    expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/workloads", expect.anything());
    expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/workloads/live", expect.anything());
    expect(fetchMock).toHaveBeenCalledWith("/v1/revisions/A1/2", expect.anything());
    expect(screen.getByText("Frozen Spec")).toBeTruthy();
    expect(screen.getByText("Projected Workloads")).toBeTruthy();
    expect(screen.getByText("Live Carriers")).toBeTruthy();
    expect(screen.getAllByText(/nginx:1.27/).length).toBeGreaterThan(0);
    expect(screen.getByText("ROLE")).toBeTruthy();
  });

  it("warns when the latest frozen revision is not effective", async () => {
    stubFetch({ latestFrozenSeq: 3 });
    const { wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" />, { wrapper });

    await waitFor(() =>
      expect(screen.getByText(/Latest frozen R3 is not effective yet — the cluster is running R2/)).toBeTruthy(),
    );
  });

  it("addresses an explicit seq via the query parameter", async () => {
    stubFetch();
    const { wrapper } = withClient();
    render(<AppSpecTab projectId="P1" appId="A1" seq={1} />, { wrapper });

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/v1/apps/A1/workloads?seq=1", expect.anything()));
  });
});
