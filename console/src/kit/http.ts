import { apiFetch } from "@/api/client";

// kit 资源适配族的共享读取帮手（src/kit/<domain>.ts 统一经此取数，不再
// 各自复制 catalog.ts 的信封处理）。鉴权（Bearer 注入 + 401 全局闭环）由
// api/client.ts 的 apiFetch 单源承载——kit 面（uikit createHttp）与 fetch
// 面在此并轨为同一管线，两条数据面共用一个 401 闭环。

// list 帮手：响应信封里的行数组展平（undefined 行过滤——生成类型的宽松
// 形态收敛为非空行；与 lib/catalog.ts rowsOf 同口径，catalog 退役时此处
// 是唯一持有者）。
export function rowsOf<T>(list: Array<T | undefined> | undefined): T[] {
  return (list ?? []).flatMap((item) => (item != null ? [item] : []));
}

// qs 组查询串（仅含非空值；与 lib/catalog.ts qs 同口径）。
export function qs(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const encoded = search.toString();
  return encoded === "" ? "" : `?${encoded}`;
}

// envelope 帮手：取数 + 行展平一步（kit 资源适配器的标准形）。
export async function fetchRows<T>(
  path: string,
  envelope: (res: unknown) => Array<T | undefined> | undefined,
  signal?: AbortSignal,
): Promise<T[]> {
  const res = await apiFetch<Parameters<typeof envelope>[0]>(path, { signal });
  return rowsOf(envelope(res));
}
