// vendored from fleet-ui registry/query/filter-bar.tsx @0.1.0（2026-10-24 试点切片）
// 拷贝适配（design.md §12.1）：token 方言类名按映射表改写；Select 基元改用
// fleet 本地原生 select 桥（本仓 ui/select 是 Radix compound，形态不匹配）；
// 其余与上游一致。
import type { Predicate } from "@fleetly/ui-kit/kernel";
import { textPredicate } from "@fleetly/ui-kit/kernel";
import type { CollectionHandle } from "@fleetly/ui-kit/react";
import type { FilterFieldDef, QuerySearchSchema } from "@fleetly/ui-kit/schema";
import { Search, X } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/fleet/ui/native-select";

/**
 * FilterBar（query 块）—— 文本搜索（q）+ 下拉过滤（filter=field:op:value）。
 * 提交走 list.setPredicate（replace + 清游标回第一页）；清空恢复默认。
 * 过滤字段清单来自与路由共用的 schema 工厂（同一实例，单一事实源）。
 */
export interface FilterBarProps<T> {
  list: CollectionHandle<T>;
  schema: QuerySearchSchema;
  textPlaceholder?: string;
}

function currentFilters(
  list: CollectionHandle<never> | CollectionHandle<unknown>,
): Record<string, string | undefined> {
  const predicate = (list as CollectionHandle<unknown>).query.predicate;
  const out: Record<string, string | undefined> = {};
  const leaves =
    predicate && "all" in predicate
      ? predicate.all
      : predicate && "field" in predicate
        ? [predicate]
        : [];
  for (const leaf of leaves) {
    if ("field" in leaf && typeof leaf.value === "string") out[leaf.field as string] = leaf.value;
  }
  return out;
}

export function FilterBar<T>({ list, schema, textPlaceholder }: FilterBarProps<T>) {
  const spec: ReadonlyArray<FilterFieldDef> = schema.spec.filters ?? [];
  const [text, setText] = useState(() =>
    typeof schema.encode(list.query).q === "string" ? (schema.encode(list.query).q as string) : "",
  );
  const selects = currentFilters(list);

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
    list.setPredicate(parts.length > 0 ? (parts.length === 1 ? parts[0]! : { all: parts }) : null);
  };

  return (
    <div className="flex flex-wrap items-center gap-2 px-3 py-2">
      <div className="relative w-64">
        <Search className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
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
