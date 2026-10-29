import { afterEach, describe, expect, it, vi } from "vitest";
import { textPredicate } from "@lynx-go/uikit-core/kernel";
import { setToken } from "@/lib/token";
import { databasesSearch, fetchDatabaseBackups, fetchPlatformSnapshots } from "./backups";

// Backups 域适配器单测（批1 Wave2-A）：跨项目 fan-out 复刻（projects →
// 逐项目 databases 合并 + staleness 业务排序 + 单项目失败吞空段）、平台
// 快照端点锚、URL 编解码往返。fetch 面 stub 在 global（fetchRows 走
// apiFetch 裸 fetch 管线）。last_backup_at 时间锚走过去/未来时刻驱动
// backupHealth 的 48h 阈值（不依赖 now 冻结——相对 now 取值）。

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

function hoursAgo(hours: number): string {
  return new Date(Date.now() - hours * 60 * 60_000).toISOString();
}

describe("fetchDatabaseBackups（跨项目 fan-out 复刻）", () => {
  it("projects → 逐项目 /v1/databases?project_id 合并，staleness 业务排序（过期先于健康，组内最久未备份最前）", async () => {
    const fetchMock = vi.fn(async (input: unknown) => {
      const url = String(input);
      if (url.includes("/v1/projects")) {
        return jsonResponse({
          projects: [
            { id: "shop", name: "Shop" },
            { id: "lab", name: "Lab" },
            undefined,
            { name: "no-id" },
          ],
        });
      }
      if (url.includes("project_id=shop")) {
        return jsonResponse({
          databases: [
            { id: "db-fresh", project_id: "shop", name: "pg", engine: "postgres", last_backup_at: hoursAgo(2) },
            undefined,
          ],
        });
      }
      if (url.includes("project_id=lab")) {
        return jsonResponse({
          databases: [
            { id: "db-stale", project_id: "lab", name: "mongo", engine: "mongo", last_backup_at: hoursAgo(72) },
            { id: "db-never", project_id: "lab", name: "redis", engine: "redis", last_backup_at: "" },
          ],
        });
      }
      return jsonResponse({}, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchDatabaseBackups({});

    expect(rows.map((row) => row.id)).toEqual(["db-never", "db-stale", "db-fresh"]);
    expect(rows[0]).toMatchObject({ project_id: "lab", project_name: "Lab", name: "redis" });
    const urls = fetchMock.mock.calls.map(([input]) => String(input));
    expect(urls.some((url) => url.includes("/v1/databases?project_id=shop"))).toBe(true);
    expect(urls.some((url) => url.includes("/v1/databases?project_id=lab"))).toBe(true);
  });

  it("单项目 databases 失败吞为空段（fan-out 局部失败不塌）", async () => {
    const fetchMock = vi.fn(async (input: unknown) => {
      const url = String(input);
      if (url.includes("/v1/projects")) {
        return jsonResponse({ projects: [{ id: "shop", name: "Shop" }] });
      }
      return jsonResponse({ code: "E_X", message: "boom" }, 503);
    });
    vi.stubGlobal("fetch", fetchMock);
    expect(await fetchDatabaseBackups({})).toEqual([]);
  });

  it("projects 轴失败收敛为错误（不再静默空表）", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ code: "E_X", message: "boom" }, 503)));
    await expect(fetchDatabaseBackups({})).rejects.toThrowError(/boom/);
  });
});

describe("fetchPlatformSnapshots（端点 + 收敛）", () => {
  it("整取 /v1/platform/backups 并展平 undefined 行", async () => {
    const taken = hoursAgo(1);
    const fetchMock = vi.fn(async () =>
      jsonResponse({ snapshots: [{ id: "s1", hostname: "node-1", time: taken }, undefined] }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const rows = await fetchPlatformSnapshots({});

    expect(rows).toEqual([{ id: "s1", hostname: "node-1", time: taken }]);
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(String(url)).toBe("/v1/platform/backups");
  });
});

describe("databasesSearch/snapshotsSearch（URL 编解码契约，双 namespace 实例）", () => {
  it("q 编码往返为并集字段集的 contains 谓词；decode 容忍缺省", () => {
    const fields = ["name", "engine", "project_name", "id", "hostname"];
    const encoded = databasesSearch.encode({
      predicate: textPredicate(fields, "pg"),
      window: { size: 20 },
    });
    // namespace 物理键（F1 回流批）：databases.q；decode 不读他命名空间键
    expect(encoded["databases.q"]).toBe("pg");
    const decoded = databasesSearch.decode({ "databases.q": "pg" });
    expect(decoded.predicate).toEqual(textPredicate(fields, "pg"));
    expect(decoded.window.size).toBe(20);
  });

  it("清除谓词的补丁显式置空 q（函数式 search 展开不残留旧值）", () => {
    const patch = databasesSearch.patch({ predicate: null });
    expect("databases.q" in patch).toBe(true);
    expect(patch["databases.q"]).toBeUndefined();
    expect(patch["databases.page"]).toBeUndefined();
  });
});
