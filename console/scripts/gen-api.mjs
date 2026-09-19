// D4-② types 生成化：合并 console 消费的 server/v1 swagger 清单 → 经
// openapi-typescript 生成 src/api/schema.d.ts（`pnpm gen:api`，package.json）。
//
// 输入是 protoc-gen-swagger 的 Swagger 2.0 文档（每 proto 服务一份）；console
// 的 REST 消费面跨多份文件，openapi-typescript 一次只吃一份 spec，故先在
// 内存里合并 paths/definitions（同名 definition 出现在多份文件时断言深度
// 相等后去重——shared 错误信封等公共消息必然重复）。openapi-typescript v7
// 只吃 OpenAPI 3.x，合并后的 Swagger 2.0 文档经 swagger2openapi 就地升格。
//
// CI 门禁（pr.yml console job）：再生成后 `git diff --exit-code` 断言无漂移。

import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import openapiTS, { astToString } from "openapi-typescript";
import converter from "swagger2openapi";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "..", "..");
const outPath = path.resolve(here, "..", "src", "api", "schema.d.ts");

// console 实际消费的服务面（endpoints.ts / streams.ts 的端点来源 proto）。
const SPEC_FILES = [
  "apps.swagger.json",
  "deployments.swagger.json",
  "revisions.swagger.json",
  "env.swagger.json",
  "domains.swagger.json",
  "logs.swagger.json",
  "events.swagger.json",
  "system.swagger.json",
  "placement.swagger.json",
];

const merged = {
  swagger: "2.0",
  info: { title: "fleetly console consumed API (merged)", version: "generated" },
  consumes: ["application/json"],
  produces: ["application/json"],
  paths: {},
  definitions: {},
};

for (const file of SPEC_FILES) {
  const spec = JSON.parse(
    await readFile(
      path.join(repoRoot, "genproto", "fleetly", "server", "v1", file),
      "utf8",
    ),
  );
  for (const [p, item] of Object.entries(spec.paths ?? {})) {
    if (merged.paths[p]) {
      throw new Error(`duplicate path across specs: ${p} (${file})`);
    }
    merged.paths[p] = item;
  }
  for (const [name, def] of Object.entries(spec.definitions ?? {})) {
    const prev = merged.definitions[name];
    if (prev !== undefined) {
      // 同名 definition（公共消息，如 v1ErrorResponse）：必须深度一致，
      // 否则 proto 间出现了同 名 不同形 的漂移。
      if (JSON.stringify(prev) !== JSON.stringify(def)) {
        throw new Error(`conflicting definition: ${name} (${file})`);
      }
      continue;
    }
    merged.definitions[name] = def;
  }
}

// Swagger 2.0 → OpenAPI 3.x（openapi-typescript v7 不再收 2.0）。
const { openapi } = await converter.convertObj(merged, {
  patch: true, // 修 protoc-gen-swagger 的轻微不规范处（缺 operationId 唯一性等）
  warnOnly: true,
  resolve: false,
});

const ast = await openapiTS(openapi);
const banner = `// 本文件由 openapi-typescript 从 genproto/fleetly/server/v1/*.swagger.json 生成
// （console/scripts/gen-api.mjs，\`pnpm gen:api\`）——不要手改；proto 变更后
// 重新生成并提交。CI（pr.yml console job）以"再生成无 diff"门禁拦截漂移。
// 字段名/类型语义：UseProtoNames（snake_case 声明名）+ proto3 JSON 映射
// （int64 → 字符串；EmitUnpopulated=false → 零值字段缺省，全部属性可选）。

`;
await mkdir(path.dirname(outPath), { recursive: true });
await writeFile(outPath, banner + astToString(ast) + "\n", "utf8");
process.stdout.write(`wrote ${path.relative(repoRoot, outPath)}\n`);
