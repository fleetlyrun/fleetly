import { defineResource, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { apiFetch } from "@/api/client";
import type { components as Templates } from "@/api/templates";
import { toApiError } from "./errors";

// 模板目录资源（Templates 页向导 + 详情抽屉；收尾批手写 useQuery 收编）。
// 目录收敛形（name/digest 双全才收 + variables 声明投影）与 detail 映射
// 照抄旧查询；source 是平台侧模板源标注（UI 披露面）。

export interface TemplateVariableDecl {
  name: string;
  type: string;
  description: string;
  default: string;
  required: boolean;
}

export interface TemplateEntry {
  name: string;
  version: string;
  digest: string;
  description: string;
  variables: TemplateVariableDecl[];
}

export interface TemplateDetail extends TemplateEntry {
  body: string;
}

function toVariableDecl(v: NonNullable<NonNullable<Templates["schemas"]["v1Template"]>["variables"]>[number]): TemplateVariableDecl | null {
  if (!v?.name) return null;
  return {
    name: v.name,
    type: v.type ?? "string",
    description: v.description ?? "",
    default: v.default ?? "",
    required: v.required ?? false,
  };
}

export const templatesResource = defineResource({
  name: "templates",
  record: (_id, _ctx, signal) =>
    apiFetch<{ source?: string; templates?: Array<Templates["schemas"]["v1Template"] | undefined> }>("/v1/templates", {
      signal,
    })
      .then(({ source, templates }) => {
        const rows: TemplateEntry[] = [];
        for (const t of templates ?? []) {
          if (!t?.name) continue;
          const variables: TemplateVariableDecl[] = [];
          for (const v of t.variables ?? []) {
            const decl = toVariableDecl(v);
            if (decl != null) variables.push(decl);
          }
          rows.push({
            name: t.name,
            version: t.version ?? "",
            digest: t.digest ?? "",
            description: t.description ?? "",
            variables,
          });
        }
        return { source: source ?? "", templates: rows };
      })
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});

// 伴读消费面（2026-10-29 F1 回流批）：useTemplates/useTemplateDetail 伴读
// 钩子退役——Templates 页直接 useRecord(templatesResource, { id: "current",
// query: { refetchInterval: 60_000 } })（目录行集 = data.templates，source
// 披露留资源面）与 useRecord(templateDetailResource, { id: name ?? undefined })
//（undefined id 自动停查 = 旧 enabled 门）。

export const templateDetailResource = defineResource({
  name: "template-detail",
  record: (name: string, _ctx: ResourceScope, signal?: AbortSignal) =>
    apiFetch<{ template?: Templates["schemas"]["v1Template"]; body?: string }>(
      `/v1/templates/${encodeURIComponent(name)}`,
      { signal },
    )
      .then((res): TemplateDetail | null => {
        const t = res.template;
        if (!t?.name) return null;
        const variables: TemplateVariableDecl[] = [];
        for (const v of t.variables ?? []) {
          const decl = toVariableDecl(v);
          if (decl != null) variables.push(decl);
        }
        return {
          name: t.name,
          version: t.version ?? "",
          digest: t.digest ?? "",
          description: t.description ?? "",
          variables,
          body: res.body ?? "",
        };
      })
      .catch((err: unknown) => {
        throw toApiError(err);
      }),
});
