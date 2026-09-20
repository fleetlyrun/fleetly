// 归一化 compose 快照的字段级 diff（T0-V2.4，R3 借鉴：zane-ops
// DeploymentChange 的 old/new 变更日志）：两版 GetRevisionSpec 快照
// （canonical JSON，compose.Spec 同构——services/volumes/networks/secrets/
// deploy 副本数与资源/域名/env hash 等，见 internal/compose/spec.go）逐
// 字段对比，产出 old → new 变更行。
//
// 取舍（本票内裁决）：
// - spec_hash 不进 diff：它是 canonical JSON 的派生哈希，任何真实变更都会
//   连带翻转、单独翻转不存在——列出来只是噪音；
// - 数组元素带 name/key 标识（services/environment/volumes 声明）时按标识
//   逐元素对比，元素增删呈现为 added/removed；无稳定标识的数组（挂载点、
//   command/expose 等原序数组）整体作为叶子值对比——增量粒度诚实降级；
// - env 值在快照中即 key:sha256+来源（明文结构性不在快照），diff 呈现的是
//   哈希变化，与快照实际维度一致。

/** 单条字段级变更：kind + 点分路径 + old/new 值（JSON 渲染形态）。 */
export interface FieldDiff {
  path: string;
  kind: "added" | "removed" | "changed";
  old: string;
  new: string;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** 标识键：数组元素带 name 或 key（非空字符串）时可作为 diff 身份。 */
function elementKey(v: unknown): string | null {
  if (!isPlainObject(v)) return null;
  for (const k of ["name", "key"]) {
    const id = v[k];
    if (typeof id === "string" && id !== "") return id;
  }
  return null;
}

/** 叶子值的渲染：标量直出，容器走 JSON（canonical JSON 键序稳定）。 */
function renderValue(v: unknown): string {
  if (v === undefined) return "—";
  if (isPlainObject(v) || Array.isArray(v)) return JSON.stringify(v);
  return String(v);
}

/**
 * 两版归一化快照（canonical JSON 文本）的字段级 diff。解析失败返回 null
 * （快照损坏/形态漂移——调用方呈现诚实提示，不伪造空 diff）。
 */
export function diffSpecJSONs(
  oldJSON: string,
  newJSON: string,
): FieldDiff[] | null {
  let oldSpec: unknown;
  let newSpec: unknown;
  try {
    oldSpec = JSON.parse(oldJSON);
    newSpec = JSON.parse(newJSON);
  } catch {
    return null;
  }
  if (!isPlainObject(oldSpec) || !isPlainObject(newSpec)) return null;

  const out: FieldDiff[] = [];
  diffTrees(oldSpec, newSpec, "", out);
  // 确定性呈现：路径字典序。
  out.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  return out;
}

function diffTrees(
  oldVal: unknown,
  newVal: unknown,
  path: string,
  out: FieldDiff[],
): void {
  // 缺失侧单行整值呈现（新增整个服务 = services.web 一行、env 组从无到有
  // = environment 一行）：可读且诚实；两侧俱在时才逐字段下钻。
  if (oldVal === undefined) {
    out.push({ path, kind: "added", old: "—", new: renderValue(newVal) });
    return;
  }
  if (newVal === undefined) {
    out.push({ path, kind: "removed", old: renderValue(oldVal), new: "—" });
    return;
  }
  if (isPlainObject(oldVal) && isPlainObject(newVal)) {
    const keys = new Set([...Object.keys(oldVal), ...Object.keys(newVal)]);
    for (const k of keys) {
      // spec_hash 是 canonical JSON 的派生哈希：任何真实变更都会连带翻转
      // （单独翻转不存在），进 diff 只是噪音——按本票裁决排除。
      if (path === "" && k === "spec_hash") continue;
      diffTrees(oldVal[k], newVal[k], path ? `${path}.${k}` : k, out);
    }
    return;
  }
  if (Array.isArray(oldVal) && Array.isArray(newVal)) {
    // 两侧元素全部带标识（空数组对空全称真）才走身份对比：一侧为空、另一
    // 侧从无到有时仍能逐元素呈现 added/removed，而非整组降级为一个叶子。
    const keyed =
      oldVal.every((v) => elementKey(v) !== null) &&
      newVal.every((v) => elementKey(v) !== null);
    if (keyed) {
      const oldMap = new Map(
        (oldVal as unknown[]).map((v) => [elementKey(v) as string, v]),
      );
      const newMap = new Map(
        (newVal as unknown[]).map((v) => [elementKey(v) as string, v]),
      );
      const ids = new Set([...oldMap.keys(), ...newMap.keys()]);
      for (const id of ids) {
        diffTrees(oldMap.get(id), newMap.get(id), `${path}.${id}`, out);
      }
      return;
    }
  }
  if (JSON.stringify(oldVal) !== JSON.stringify(newVal)) {
    out.push({
      path,
      kind: "changed",
      old: renderValue(oldVal),
      new: renderValue(newVal),
    });
  }
}
