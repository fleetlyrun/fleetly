import { useMemo, useState } from "react";
import { ChevronLeftIcon, ChevronRightIcon } from "lucide-react";
import { CopyButton } from "@/components/domain/copy-button";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

// 列表页规范件（IA v3 原型 screen-apps/anatomy 核对版；对齐批 5 复裁）：
// 工具栏 = 过滤搜索 + 过滤 slot（引擎/类型下拉等）+ 右侧计数 + **主动作
// 槽**（创建/上传等列表行动作钉在工具栏行最右——用户裁决：页头右侧钮位
// 退役，列表头右侧兼容性更好）；表格下 CLI equivalent 行（开发者对照
// 面——无 CLI 动词的页面不放该行）。
export function ListToolbar({
  label,
  value,
  onChange,
  placeholder,
  total,
  shown,
  children,
  actions,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  total: number;
  shown: number;
  children?: React.ReactNode;
  actions?: React.ReactNode;
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
      {actions ? <div className="flex flex-none items-center gap-2">{actions}</div> : null}
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

// useClientPage 是客户端分页切片 hook（对齐批 5：所有列表页统一分页——
// REST 面整取的行在页内翻页，页码越界自动钳制，过滤收缩不空页）。
export function useClientPage<T>(rows: T[], pageSize = 20) {
  const [page, setPage] = useState(0);
  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
  const safePage = Math.min(page, pageCount - 1);
  const pageRows = rows.slice(safePage * pageSize, safePage * pageSize + pageSize);
  return { page: safePage, pageCount, pageRows, setPage };
}

// ListPagination 是列表卡底部的分页行（右下页码 + 前后翻页，超页才显
// ——与用户标注的分页位一致）。
export function ListPagination({
  page,
  pageCount,
  setPage,
  total,
}: {
  page: number;
  pageCount: number;
  setPage: (page: number) => void;
  total: number;
}) {
  if (pageCount <= 1) return null;
  return (
    <div className="flex items-center justify-end gap-2 border-t px-3 py-2 text-xs text-muted-foreground">
      <span>
        {total} rows · page {page + 1} of {pageCount}
      </span>
      <Button variant="outline" size="icon-sm" disabled={page === 0} onClick={() => setPage(page - 1)} aria-label="Previous page">
        <ChevronLeftIcon />
      </Button>
      <Button variant="outline" size="icon-sm" disabled={page >= pageCount - 1} onClick={() => setPage(page + 1)} aria-label="Next page">
        <ChevronRightIcon />
      </Button>
    </div>
  );
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
