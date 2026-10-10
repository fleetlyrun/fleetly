import { useMemo } from "react";
import { CopyButton } from "@/components/domain/copy-button";
import { Input } from "@/components/ui/input";

// 列表页规范件（IA v3 原型 screen-apps/anatomy 核对版）：工具栏 = 过滤
// 搜索 + 过滤 slot（引擎/类型下拉等）+ 右侧计数；表格下 CLI equivalent
// 行（开发者对照面——无 CLI 动词的页面不放该行）。
export function ListToolbar({
  label,
  value,
  onChange,
  placeholder,
  total,
  shown,
  children,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  total: number;
  shown: number;
  children?: React.ReactNode;
}) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-3 rounded-xl border bg-card px-3 py-2">
      <Input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className="h-8 w-56 bg-muted/40"
        aria-label={`Filter ${label}`}
      />
      {children}
      <span className="ml-auto text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
        {shown} of {total}
      </span>
    </div>
  );
}

// useListFilter 是 ListToolbar 配套的行过滤 hook（按可见字段子串匹配，
// 大小写不敏感；单 hook 单源——各列表页不再手写 filter 逻辑）。
export function useListFilter<T>(rows: T[], query: string, fieldsOf: (row: T) => string[]): T[] {
  return useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === "") return rows;
    return rows.filter((row) => fieldsOf(row).some((field) => field.toLowerCase().includes(q)));
  }, [rows, query, fieldsOf]);
}

// CliEquivalent 是列表页尾部的开发者对照行（原型 screen-apps 锚件）：
// 命令文本 + 一键复制。命令里的项目/资源锚由调用方注入（真实可执行）。
export function CliEquivalent({ command }: { command: string }) {
  return (
    <p className="mt-2.5 flex items-center gap-1.5 font-mono text-[11.5px] text-muted-foreground">
      <span aria-hidden>❯</span>
      <span className="text-foreground/80">CLI equivalent:</span>
      <span>{command}</span>
      <CopyButton value={command} />
    </p>
  );
}
