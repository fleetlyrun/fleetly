import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Console UI 反模式守卫（UI v2 一致性契约的执法面）：新世界目录禁止——
// ① window.confirm（破坏性操作一律 AlertDialog）；
// ② 原生 <dialog>（Modal 面只经 shadcn Dialog/Radix）；
// ③ toLocaleString 时间散写（一律 lib/format 单源）；
// ④ window.location.hash 直写（导航一律 TanStack Router）；
// ⑤ "check the API token" 万金油错误文案（分状态诚实文案）。
// 执法范围：全量 src/**（批 5 起旧页消亡）。components/ui/**（shadcn 上游
// 手植层）按上游原样纪律天然不在扫描面。
// 范围例外（带理由，双向保鲜）：
// - components/ui/**：shadcn 上游手植层，按原样保留不自改（升级即 diff 干净）；
// - components/ui.tsx：旧共享层，尚在役服务 pre-v2 页面，批 5 消亡后移除例外；
// - lib/router.ts：旧 hash 路由，批 1 被 TanStack Router 取代后移除例外。
const SCANNED_ROOTS = ["src/components/domain", "src/lib", "src/features", "src/hooks", "src/routes", "src/pages"];
const EXEMPT_FILES = ["src/components/ui.tsx", "src/lib/router.ts"];

const RULES: Array<{ pattern: RegExp; message: string }> = [
  { pattern: /window\.confirm\s*\(/, message: "window.confirm is banned — use AlertDialog (destructive ops contract)" },
  { pattern: /<dialog[\s>]/, message: "native <dialog> is banned — use shadcn Dialog (W1/W3 lessons)" },
  { pattern: /\.toLocaleString\s*\(/, message: "local time formatting is banned — use lib/format.ts (time contract)" },
  { pattern: /window\.location\.hash\s*=/, message: "hash writes are banned — navigate via TanStack Router" },
  { pattern: /check the API token/i, message: "'check the API token' catch-all copy is banned — use describeError states" },
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
        if (EXEMPT_FILES.includes(path.replaceAll("\\", "/"))) continue;
        const lines = readFileSync(path, "utf8").split("\n");
        lines.forEach((line, index) => {
          for (const rule of RULES) {
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
