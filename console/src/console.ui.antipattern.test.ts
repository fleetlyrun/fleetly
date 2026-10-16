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
// 执法范围：全量 src/**。components/ui/**（shadcn 上游手植层）按上游原样
// 纪律天然不在扫描面。
// 范围例外（带理由，双向保鲜）：
// - components/ui/**：shadcn 上游手植层，按原样保留不自改（升级即 diff 干净）；
// - features/fleet/backups-view.tsx：平台快照只读小表无快筛需求，分页
//   直挂 useClientPage（ListCard 会强加快筛输入——台账形态特例）。
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
    // 模板定义域合法；backups-view 快照小表是带理由的特例（见文件头例外表）
    skip: (path) => path.startsWith("src/components/domain/") || path === "src/features/fleet/backups-view.tsx",
  },
];

function* walk(dir: string): Generator<string> {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) yield* walk(path);
    else if (/\.(tsx?|css)$/.test(name)) yield path;
  }
}

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
