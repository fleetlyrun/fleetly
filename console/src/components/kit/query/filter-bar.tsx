// vendored from @lynx-go/uikit registry/query/filter-bar.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 其余与上游一致。
import { textPredicate, type Predicate } from "@lynx-go/uikit-core/kernel";
import type { CollectionHandle } from "@lynx-go/uikit-core/react";
import { filterValues, predicateOf } from "@lynx-go/uikit-core/schema";
import type { FilterFieldDef, QuerySearchSchema } from "@lynx-go/uikit-core/schema";
import { Search, X } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/kit/ui/native-select"; /* 方言桥 §12.1-5：demo Select=原生包装，本仓 ui/select 是 Radix compound */

/**
 * FilterBar（query 块）—— 文本搜索（q）+ 下拉过滤（filter=field:op:value）。
 * 提交走 list.setPredicate（replace + 清游标回第一页）；清空恢复默认。
 * 过滤字段清单来自与路由共用的 schema 工厂（同一实例，单一事实源）。
 * 谓词读写经 schema 纯函数层（filterValues/predicateOf）——本块只是薄壳，
 * 消费方自绘过滤条时复用同一对纯函数（§12.10-④）。
 */
export interface FilterBarProps<T> {
  list: CollectionHandle<T>;
  schema: QuerySearchSchema;
  textPlaceholder?: string;
}

export function FilterBar<T>({ list, schema, textPlaceholder }: FilterBarProps<T>) {
  const spec: ReadonlyArray<FilterFieldDef> = schema.spec.filters ?? [];
  const [text, setText] = useState(() =>
    typeof schema.encode(list.query).q === "string" ? (schema.encode(list.query).q as string) : "",
  );
  // 读路径：单值投影收窄到 spec 字段（过滤下拉即单值）
  const values = filterValues(list.query.predicate);
  const selects: Record<string, string | undefined> = {};
  for (const def of spec) {
    const v = values[def.field];
    if (typeof v === "string") selects[def.field] = v;
  }

  const apply = (nextText: string, nextSelects: Record<string, string | undefined>) => {
    const parts: Predicate[] = [];
    const tp = textPredicate(schema.spec.text ?? [], nextText);
    if (tp) parts.push(tp);
    for (const def of spec) {
      const value = nextSelects[def.field];
      if (value !== undefined && value !== "") {
        parts.push({ field: def.field, op: def.op ?? "eq", value });
      }
    }
    list.setPredicate(predicateOf(parts));
  };

  return (
    <div className="flex flex-wrap items-center gap-2 px-3 py-2">
      <div className="relative w-64">
        <Search className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted" />
        <Input
          className="pl-8"
          placeholder={textPlaceholder ?? "Search…"}
          value={text}
          aria-label="Search"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") apply(text, selects);
          }}
          onBlur={() => apply(text, selects)}
        />
      </div>
      {spec.map((def) => (
        <Select
          key={def.field}
          aria-label={def.label ?? def.field}
          value={selects[def.field] ?? ""}
          onChange={(e) => apply(text, { ...selects, [def.field]: e.target.value || undefined })}
        >
          <option value="">{def.label ?? def.field}: all</option>
          {(def.options ?? []).map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      ))}
      <Button
        variant="ghost"
        size="sm"
        onClick={() => {
          setText("");
          apply("", {});
        }}
      >
        <X className="size-3.5" /> Clear
      </Button>
    </div>
  );
}
