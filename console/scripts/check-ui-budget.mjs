#!/usr/bin/env node
// console/scripts/check-ui-budget.mjs — console UI 体积预算断言（zeabur-oss 借鉴程序
// B4b，设计稿 docs/design/2026-10-30-zeabur-oss-borrowing.md §4）。
//
// 预算纪律：**预算上涨必须同 commit 修改 console/budget.json，且 commit message 带
// 理由**（zeabur-oss 借鉴程序 B4b，设计稿 §4）。本脚本只执法不解释——涨预算的
// 解释义务在 commit message。
//
// 体积口径：node:zlib brotliCompressSync（quality 11）量 dist 产物字节 ≈ 浏览器
// 实际下载口径（比 raw/gzip 更接近开启 br 压缩的服务面真实传输量）。
//
// 预算双层（console/budget.json，committed；首期按现产出定基线 = 实测值 + 5% 余量
// 向上取整到 KB）：
//   entry  — index.html 直接引用的入口 JS 与 CSS 各一项（modulepreload 的 chunk
//            归属其命名 chunk 条目，不重复计入 entry）；
//   chunks — vite manualChunks 命名的 chunk 逐项上限（charts/table/virtual/
//            xterm/forms/cmdk/vendor），匹配用名字模式（不含 content hash），
//            一个条目命中多个文件时按 brotli 求和；
//   兜底   — 任何未被预算条目覆盖且 brotli 后超过 fallback.maxBytes（50KB）的
//            资产即失败，防新 chunk 溜进预算外。
//
// 零外部依赖（node 内建 only）；用户可见输出英文。dist 与 budget 路径可用
// FLEETLY_CONSOLE_DIST / FLEETLY_CONSOLE_BUDGET 环境变量覆盖（自检/测试用）。
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliCompressSync, constants as zlibConstants } from "node:zlib";

const KIB = 1024;
const scriptDir = dirname(fileURLToPath(import.meta.url));
const consoleDir = join(scriptDir, "..");
const repoRoot = join(consoleDir, "..");
const distDir = process.env.FLEETLY_CONSOLE_DIST ?? join(repoRoot, "internal", "interfaces", "console", "dist");
const budgetPath = process.env.FLEETLY_CONSOLE_BUDGET ?? join(consoleDir, "budget.json");

function die(message) {
  console.error(`check-ui-budget: ${message}`);
  process.exit(1);
}

function fmt(bytes) {
  return `${bytes.toLocaleString("en-US")} B (${(bytes / KIB).toFixed(1)} KiB)`;
}

// 递归收集 dist 下全部文件（相对 dist 的路径）。
function walk(dir, prefix = "") {
  const out = [];
  for (const ent of readdirSync(dir, { withFileTypes: true })) {
    const rel = prefix ? `${prefix}/${ent.name}` : ent.name;
    if (ent.isDirectory()) out.push(...walk(join(dir, ent.name), rel));
    else out.push(rel);
  }
  return out;
}

// 名字模式（如 "charts-*.js"）→ 正则；`*` 匹配单段任意字符（不含 `/`）。
function patternToRegExp(pattern) {
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*");
  return new RegExp(`^${escaped}$`);
}

// --- 前置检查：dist 缺失时报错须可直接指导下一步（先 build） ---
if (!existsSync(distDir)) {
  die([
    `dist output not found at ${distDir}.`,
    "Build the console first:  mise run console:build",
  ].join("\n"));
}
const indexPath = join(distDir, "index.html");
if (!existsSync(indexPath)) {
  die(`index.html not found in ${distDir} — is this a console build output?`);
}
if (!existsSync(budgetPath)) {
  die([
    `budget file not found at ${budgetPath}.`,
    "Create it from a fresh build baseline (zeabur-oss borrowing program B4b, design doc section 4).",
  ].join("\n"));
}

let budget;
try {
  budget = JSON.parse(readFileSync(budgetPath, "utf8"));
} catch (err) {
  die(`invalid budget JSON at ${budgetPath}: ${err.message}`);
}
const chunks = Array.isArray(budget?.chunks) ? budget.chunks : null;
if (!Number.isFinite(budget?.entry?.js?.budgetBytes) || !Number.isFinite(budget?.entry?.css?.budgetBytes) || chunks === null || !Number.isFinite(budget?.fallback?.maxBytes)) {
  die(`budget file ${budgetPath} must define entry.js.budgetBytes, entry.css.budgetBytes, chunks[] and fallback.maxBytes`);
}
for (const c of chunks) {
  if (typeof c.name !== "string" || typeof c.pattern !== "string" || !Number.isFinite(c.budgetBytes)) {
    die(`budget chunks[] entries need string name, string pattern and numeric budgetBytes (got ${JSON.stringify(c)})`);
  }
}

// --- 逐文件量 brotli 字节（q11 = 浏览器下载口径） ---
const brotli = new Map();
for (const rel of walk(distDir)) {
  brotli.set(rel, brotliCompressSync(readFileSync(join(distDir, rel)), {
    params: { [zlibConstants.BROTLI_PARAM_QUALITY]: 11 },
  }).length);
}

// --- entry 层：从 index.html 解析入口（<script type=module src> 与 <link rel=stylesheet>），
//     不用名字 glob——assets 里 index-*.js 不唯一（路由级分包同名），index.html 才是入口真源 ---
function attr(tag, name) {
  return tag.match(new RegExp(`\\b${name}\\s*=\\s*["']([^"']+)["']`))?.[1] ?? null;
}
const html = readFileSync(indexPath, "utf8");
const entryJsFiles = [...html.matchAll(/<script\b[^>]*>/g)]
  .map((m) => m[0])
  .filter((t) => (attr(t, "type") ?? "").toLowerCase() === "module")
  .map((t) => attr(t, "src"))
  .filter(Boolean)
  .map((ref) => ref.replace(/[?#].*$/, "").replace(/^\/+/, ""));
const entryCssFiles = [...html.matchAll(/<link\b[^>]*>/g)]
  .map((m) => m[0])
  .filter((t) => (attr(t, "rel") ?? "").toLowerCase() === "stylesheet")
  .map((t) => attr(t, "href"))
  .filter(Boolean)
  .map((ref) => ref.replace(/[?#].*$/, "").replace(/^\/+/, ""));
if (entryJsFiles.length === 0) die("no module entry script found in dist/index.html — build output shape changed?");
if (entryCssFiles.length === 0) die("no stylesheet link found in dist/index.html — build output shape changed?");
for (const f of [...entryJsFiles, ...entryCssFiles]) {
  if (!brotli.has(f)) die(`entry file referenced by index.html not found in dist: ${f}`);
}

// --- 汇总条目（current = 命中文件 brotli 求和；covered 供兜底口径用） ---
const covered = new Set();
const items = [];
function addItem(layer, name, files, budgetBytes) {
  const current = files.reduce((sum, f) => sum + brotli.get(f), 0);
  for (const f of files) covered.add(f);
  items.push({ layer, name, files, current, budgetBytes });
}
addItem("entry", "js", entryJsFiles, budget.entry.js.budgetBytes);
addItem("entry", "css", entryCssFiles, budget.entry.css.budgetBytes);
for (const c of chunks) {
  const re = patternToRegExp(c.pattern);
  const matched = [...brotli.keys()]
    .filter((rel) => rel.startsWith("assets/") && re.test(rel.slice("assets/".length)))
    .sort();
  if (matched.length === 0) {
    die([
      `budget chunk "${c.name}" (pattern "${c.pattern}") matched no file under ${distDir}/assets.`,
      "Stale budget entry or chunk renamed — update console/budget.json together with console/vite.config.ts manualChunks, in the same commit.",
    ].join("\n"));
  }
  addItem("chunks", c.name, matched, c.budgetBytes);
}

// --- 兜底：未被任何条目覆盖且 brotli 超 fallback.maxBytes 的资产即红 ---
const fallbackMax = budget.fallback.maxBytes;
const uncovered = [...brotli.keys()].filter((rel) => !covered.has(rel));
const oversize = uncovered
  .filter((rel) => brotli.get(rel) > fallbackMax)
  .sort((a, b) => brotli.get(b) - brotli.get(a));

// --- 输出（英文）：逐项 current/budget 字节 + 超限项，非零退出 ---
console.log("fleetly console UI budget check (brotli q11, browser-download bytes)");
console.log(`dist: ${distDir}`);
console.log("");
let failures = 0;
const failedNames = [];
for (const item of items) {
  if (item.current > item.budgetBytes) {
    failures++;
    failedNames.push(`${item.layer}/${item.name}`);
    console.log(`[FAIL] ${item.layer}/${item.name}: current ${fmt(item.current)} exceeds budget ${fmt(item.budgetBytes)} by ${fmt(item.current - item.budgetBytes)}`);
  } else {
    console.log(`[ok]   ${item.layer}/${item.name}: ${fmt(item.current)} / ${fmt(item.budgetBytes)}`);
  }
  for (const f of item.files) {
    console.log(`         ${f}  ${fmt(brotli.get(f))}`);
  }
}
if (oversize.length > 0) {
  failures++;
  failedNames.push("fallback");
  console.log(`[FAIL] fallback: ${oversize.length} uncovered asset(s) exceed ${fmt(fallbackMax)}:`);
  for (const f of oversize) {
    console.log(`         ${f}  ${fmt(brotli.get(f))}`);
  }
  console.log("Add a budget entry for each asset above, or shrink it — assets outside the budget may not exceed the fallback cap.");
} else {
  const largest = uncovered.reduce((acc, rel) => (brotli.get(rel) > brotli.get(acc) ? rel : acc), uncovered[0] ?? "(none)");
  const largestBytes = uncovered.length > 0 ? brotli.get(largest) : 0;
  console.log(`[ok]   fallback: ${uncovered.length} uncovered asset(s), largest ${largest} at ${fmt(largestBytes)} / ${fmt(fallbackMax)}`);
}
console.log("");
if (failures > 0) {
  console.error(`UI budget check FAILED for ${failures} item(s): ${failedNames.join(", ")}`);
  console.error("A budget increase must land in the same commit as the code that grows it, with a justification in the commit message (zeabur-oss borrowing program B4b, design doc section 4).");
  process.exit(1);
}
console.log(`UI budget check passed: ${items.length} item(s) within budget, 0 uncovered asset(s) over ${fmt(fallbackMax)}.`);
