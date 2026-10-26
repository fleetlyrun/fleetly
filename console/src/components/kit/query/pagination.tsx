// vendored from @lynx-go/uikit registry/query/pagination.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 其余与上游一致。
import type { CollectionHandle } from "@lynx-go/uikit-core/react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/kit/ui/native-select"; /* 方言桥 §12.1-5：demo Select=原生包装，本仓 ui/select 是 Radix compound */

/**
 * Pagination（query 块）—— offset/total 与客户端求值方言。
 * keyset 方言用 KeysetPagination（无 total：显示 x–y、空页仍渲染）。
 */
export interface PaginationProps<T> {
  list: CollectionHandle<T>;
  sizeOptions?: ReadonlyArray<number>;
}

export function Pagination<T>({ list, sizeOptions = [10, 20, 50] }: PaginationProps<T>) {
  const query = list.query;
  const total = list.total;
  const segment = list.segment;
  const size = query.window.size;
  const offset = query.window.range?.offset ?? 0;
  const page = Math.floor(offset / size) + 1;
  const count = total ?? segment?.rows.length ?? 0;
  const pageCount = Math.max(1, Math.ceil(count / size));

  return (
    <div className="flex items-center justify-between gap-3 px-3 py-2 text-xs text-muted">
      <div>
        {count === 0
          ? "No rows"
          : `Showing ${(page - 1) * size + 1}–${Math.min(page * size, count)} of ${count}`}
      </div>
      <div className="flex items-center gap-2">
        <Select
          aria-label="Page size"
          value={String(size)}
          onChange={(e) => list.setWindow({ size: Number(e.target.value) })}
        >
          {sizeOptions.map((option) => (
            <option key={option} value={String(option)}>
              {option} / page
            </option>
          ))}
        </Select>
        <Button
          variant="outline"
          size="icon"
          aria-label="Previous page"
          disabled={page <= 1}
          onClick={() => list.prev()}
        >
          <ChevronLeft className="size-4" />
        </Button>
        <span>
          Page {page} / {pageCount}
        </span>
        <Button
          variant="outline"
          size="icon"
          aria-label="Next page"
          disabled={page >= pageCount}
          onClick={() => list.next()}
        >
          <ChevronRight className="size-4" />
        </Button>
      </div>
    </div>
  );
}
