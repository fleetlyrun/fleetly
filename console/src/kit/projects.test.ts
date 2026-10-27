import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { fetchProjects, projectsAllQuery, projectsResource } from "./projects";

// 项目目录资源锚（批4 读面收尾 R1）：端点（GET /v1/projects?limit=100）与
// 收敛口径（id/name 双全才收——裸行不带名的宽松生成形不进目录）。

afterEach(() => {
  vi.unstubAllGlobals();
  setToken("");
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("projects fetcher（端点 + 收敛）", () => {
  it("打 GET /v1/projects?limit=100 并只收 id/name 双全的行（catalog useProjects 口径）", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse({
        projects: [
          { id: "p1", name: "fleet" },
          { id: "p2" }, // 缺 name——不收
          { name: "orphan" }, // 缺 id——不收
          undefined,
        ],
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchProjects({}, undefined);

    expect(rows).toEqual([{ id: "p1", name: "fleet" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/projects?limit=100");
  });

  it("伴读固定窗 size 100（queryKey 嵌窗形——共享 projectsAllQuery 即同缓存条目）", () => {
    expect(projectsAllQuery).toEqual({ window: { size: 100 } });
    expect(projectsResource.getId?.({ id: "p1", name: "fleet" })).toBe("p1");
  });
});
