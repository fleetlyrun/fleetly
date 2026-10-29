import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Console UI 反模式守卫（UI v2 一致性契约的执法面）：新世界目录禁止——
// ① window.confirm（破坏性操作一律 AlertDialog/ConfirmDialog）；
// ② 原生 <dialog>（Modal 面只经 shadcn Dialog/Radix）；
// ③ toLocaleString 时间散写（一律 lib/format 单源）；
// ④ window.location.hash 直写（导航一律 TanStack Router）；
// ⑤ "check the API token" 万金油错误文案（分状态诚实文案）；
// ⑥ 手拼页面壳（壳 = PageFrame 家族单源——ListPage/ModulePage 内建）；
// ⑦ pre-v2 共享层死灰复燃（components/ui.tsx 已退役：Modal/RowButton/
//    DangerRowButton/formatTime 等被 shadcn 件 + lib/format + ConfirmDialog
//    取代；列表分页由 ListCard/DataTable 内建，页面不手接）。
// ⑧ kit 列表件携带页面 chrome（一页一壳律的机制面）：页面衬垫与页头
//    只归 PageFrame/ModulePage 壳单源；kit ListPage 是壳内模块配方，
//    必须零页面 chrome（padding/页头三件）。升版重拷把上游的 px-6
//    py-6/header 回拷即红——双壳回归的唯一通道在组件形状上堵死
//    （2026-10-28 机制收口，取代逐位点 embedded 白名单）。
// 执法范围：全量 src/**。components/ui/**（shadcn 上游手植层）按上游原样
// 纪律天然不在扫描面。
// 范围例外（带理由，双向保鲜）：
// - components/ui/**：shadcn 上游手植层，按原样保留不自改（升级即 diff 干净）；
// - features/fleet/backups-view.tsx 的快照小表特例已随批1 Wave2-A
//   useCollection 化摘除（快照 tab 收编 kit 四件套，分页走 URL）——本规则
//   不再保留文件级豁免。
// 旧 #/ 深链的一次性改写（ADR-0057）在 main.tsx 启动时经 lib/legacy-hash
// 完成——只读 hash + history.replaceState，无直写面，不在豁免之列。
const SCANNED_ROOTS = ["src/components/domain", "src/lib", "src/features", "src/hooks", "src/routes", "src/pages"];

const RULES: Array<{ pattern: RegExp; message: string; skip?: (path: string) => boolean }> = [
  { pattern: /window\.confirm\s*\(/, message: "window.confirm is banned — use AlertDialog (destructive ops contract)" },
  { pattern: /<dialog[\s>]/, message: "native <dialog> is banned — use shadcn Dialog (W1/W3 lessons)" },
  { pattern: /\.toLocaleString\s*\(/, message: "local time formatting is banned — use lib/format.ts (time contract)" },
  {
    pattern: /window\.location\.hash\s*=/,
    message: "hash writes are banned — navigate via TanStack Router (legacy #/ links are rewritten once at boot via lib/legacy-hash)",
  },
  { pattern: /check the API token/i, message: "'check the API token' catch-all copy is banned — use describeError states" },
  {
    // 2026-10-12 收紧：pt-6 形态之外，pb-8 变体（App 详情七 tab 内壳实咬
    // ——布局 PageFrame 之上再套 mx-auto max-w-7xl px-6 pb-8，内容宽比
    // Overview/Deployments 窄 24px×2）同样在禁。
    pattern: /mx-auto max-w-\w+ px-6/,
    message: "hand-rolled page shell is banned — the shell lives in PageFrame/ListPage/ModulePage (components/domain)",
    // 模板定义域自身合法（本规则的唯一合法宿主）
    skip: (path) => path.startsWith("src/components/domain/"),
  },
  {
    pattern: /from ["'](@\/components\/ui|\.\.?\/ui)["']/,
    message: "the pre-v2 shared layer (components/ui.tsx) is retired — use shadcn ui/* parts, lib/format, lib/use-api-mutation and domain templates",
  },
  {
    pattern: /\b(useClientPage|ListPagination)\b/,
    message: "hand-wired list pagination is banned — pagination is built into ListCard/DataTable",
    // 模板定义域合法（本规则的唯一合法宿主）
    skip: (path) => path.startsWith("src/components/domain/"),
  },
  {
    // 批2/批4 收官不变量（2026-10-27）：写面一律 useCommand/useCommandForm
    //（结果两分 + kit selector 失效），读面一律 kit 绑定（useCollection/
    // useRecord/collectionQueryOptions 伴读）。lib/use-api-mutation 与
    // lib/catalog 均已退役——防复活。
    pattern: /\buseMutation\s*\(|useApiMutation|from ["']\@\/lib\/catalog["']/,
    message:
      "bare mutation / catalog import is banned — writes go through useCommand (kit commands), reads through kit bindings (uikit data-plane contract)",
  },
];

function* walk(dir: string): Generator<string> {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) yield* walk(path);
    else if (/\.(tsx?|css)$/.test(name)) yield path;
  }
}

// kit 列表件 chrome 禁令（2026-10-28 机制收口）：页面衬垫与页头只归
// PageFrame/ModulePage 壳单源；kit ListPage 是壳内模块配方，零页面
// chrome。升版重拷回拷上游 padding/header 即红——这类"双壳"回归的
// 唯一通道在组件形状上堵死。只扫 className 字面量（散文注释不射程）。
const KIT_LIST_PAGE_FILE = "src/components/kit/query/list-page.tsx";

describe("console kit list-page chrome guard", () => {
  it("kit ListPage carries no page chrome (one shell per page — PageFrame/ModulePage owns padding)", () => {
    const lines = readFileSync(KIT_LIST_PAGE_FILE, "utf8").split("\n");
    const violations: string[] = [];
    lines.forEach((line, index) => {
      if (!/className=/.test(line)) return;
      const hit = line.match(/\b(?:px-6|py-6|pt-6|pb-8|mx-auto)\b/);
      if (hit != null) {
        violations.push(
          `${KIT_LIST_PAGE_FILE}:${index + 1} — page chrome (${hit[0]}) in kit ListPage; the shell (PageFrame/ModulePage) owns padding and the page header`,
        );
      }
    });
    expect(violations, `\n${violations.join("\n")}`).toEqual([]);
  });
});

describe("console UI anti-pattern guard", () => {
  for (const root of SCANNED_ROOTS) {
    it(`scans ${root}`, () => {
      if (!existsSync(root)) return; // 尚未落地的目录（features/routes 随批次创建）
      const violations: string[] = [];
      for (const path of walk(root)) {
        const normalized = path.replaceAll("\\", "/");
        const lines = readFileSync(path, "utf8").split("\n");
        lines.forEach((line, index) => {
          for (const rule of RULES) {
            if (rule.skip?.(normalized)) continue;
            if (rule.pattern.test(line)) {
              violations.push(`${path}:${index + 1} — ${rule.message}\n    ${line.trim()}`);
            }
          }
        });
      }
      expect(violations, `\n${violations.join("\n")}`).toEqual([]);
    });
  }
});
