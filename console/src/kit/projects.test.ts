import { afterEach, describe, expect, it, vi } from "vitest";
import { setToken } from "@/lib/token";
import { projectsResource } from "./projects";

// 项目目录资源锚（批4 读面收尾 R1；uikit 0.5.0 回流批改 restFetchAll
// 口径）：端点（GET /v1/projects?limit=100）与收敛口径（id/name 双全才收
// ——裸行不带名的宽松生成形不进目录，envelope 投影）。断言经资源适配器
// 调用取数。

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

const ALL = { window: { size: 100 } } as const;

describe("projectsResource 读面（端点 + 收敛）", () => {
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

    const rows = (await projectsResource.collection!(ALL, {}, undefined)).rows;

    expect(rows).toEqual([{ id: "p1", name: "fleet" }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/projects?limit=100");
  });

  it("getId 锚（伴读窗约定见 kit/projects.ts 注——size 100 整表窗）", () => {
    expect(projectsResource.getId?.({ id: "p1", name: "fleet" })).toBe("p1");
  });
});
