// openapi-typescript 生成链（ADR-0044）：从 genproto 的 per-context
// swagger.json 生成 src/api/<context>.ts，逐文件镜像。proto/swagger 是
// 契约源（手改生成物即错）——再生成走本脚本（mise run console:gen）。
//
// 两步确定性预处理（生成物零漂移的前提）：
// 1. 悬空 $ref 改写：openapiv2 插件把 responses.default 渲染为外部形态
//    `$ref: ".fleetly.shared.v1.ErrorResponse"`（definitions 为空、仓内无
//    target 文件）。改写为本文件内注入的 `#/definitions/ErrorResponse`
//    最小信封（code/message，对齐 apperr 网关渲染面）。键序保持原文件
//    （JSON.parse 保留插入序）。
// 2. Swagger 2.0 → OpenAPI 3：openapi-typescript 7.x 只吃 3.x，经
//    swagger2openapi 机械转换（definitions→components.schemas、body
//    参数→requestBody、responses.schema→content）。
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import converter from "swagger2openapi";

// F3.1 写面扩面：identity/automation/proxy 入生成清单（audit/tokens 与
// tasks/schedules/routes 的消费类型面；system/governance 的 freeze 面小，
// 按需再入——消费哪些上下文就生成哪些，同 ADR-0044 口径）。
const CONTEXTS = ["structure", "delivery", "telemetry", "identity", "automation", "proxy", "runtime", "exec"];

const GENPROTO = "../genproto/fleetly";
const OUT_DIR = "src/api";
const DANGLING_REF = ".fleetly.shared.v1.ErrorResponse";

const errorDefinition = {
  type: "object",
  properties: {
    code: { type: "string" },
    message: { type: "string" },
  },
};

// rewriteRefs 深度遍历改写悬空 ref（值精确匹配；对象/数组递归）。
function rewriteRefs(node) {
  if (Array.isArray(node)) {
    for (const item of node) rewriteRefs(item);
    return;
  }
  if (node === null || typeof node !== "object") return;
  for (const [key, value] of Object.entries(node)) {
    if (key === "$ref" && value === DANGLING_REF) {
      node[key] = "#/definitions/ErrorResponse";
    } else {
      rewriteRefs(value);
    }
  }
}

const convert = (spec) =>
  new Promise((resolve, reject) => {
    converter.convertObj(spec, {}, (err, options) => {
      if (err) reject(err);
      else resolve(options.openapi);
    });
  });

mkdirSync(OUT_DIR, { recursive: true });
const tmp = join(OUT_DIR, ".gen.tmp.openapi.json");
rmSync(tmp, { force: true });
try {
  for (const ctx of CONTEXTS) {
    // exec 上下文按 proto 文件镜像（runtime/v1/exec.proto 独立 swagger——
    // openapiv2 插件按文件产出；其余上下文 = 目录同名文件）。
    const specPath = ctx === "exec" ? join(GENPROTO, "runtime", "v1", "exec.swagger.json") : join(GENPROTO, ctx, "v1", `${ctx}.swagger.json`);
    const spec = JSON.parse(readFileSync(specPath, "utf8"));
    rewriteRefs(spec);
    spec.definitions ??= {};
    spec.definitions.ErrorResponse = errorDefinition;
    const oas3 = await convert(spec);
    writeFileSync(tmp, JSON.stringify(oas3));
    execFileSync("node_modules/.bin/openapi-typescript", [tmp, "-o", `${OUT_DIR}/${ctx}.ts`], { stdio: "inherit" });
    console.log(`generated ${OUT_DIR}/${ctx}.ts from genproto/${ctx}/v1/${ctx}.swagger.json`);
  }
} finally {
  rmSync(tmp, { force: true });
}
