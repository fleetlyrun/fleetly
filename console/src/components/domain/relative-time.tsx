import { formatAbsolute, formatRelative } from "@/lib/format";

// RelativeTime（UI v2 时间契约）：主显相对形态，悬浮出绝对时间——
// 全站唯一时间渲染入口（页面级时间散写的收敛位）。
export function RelativeTime({ value, className }: { value: string | undefined; className?: string }) {
  if (!value) return <span className={className}>—</span>;
  return (
    <span title={formatAbsolute(value)} className={className}>
      {formatRelative(value)}
    </span>
  );
}
