import type { ComponentProps } from "react";
import { cn } from "cn";

// fleet-ui 块的 Select 基元桥（拷贝制底座接入裁决 §12.1-5）：registry 块按
// demo 方言假设 Select 是原生 select 的样式包装（FilterBar/Pagination 直用
// value/onChange/<option>）；本仓 @/components/ui/select 是 Radix compound，
// 形态不匹配。块不改源，在 fleet 基元目录落一个等价原生包装（样式对齐本仓
// Input 的 token 面），供块 import——这是方言缺口的唯一桥，不进 ui/**。
export function Select({ className, ...props }: ComponentProps<"select">) {
  return (
    <select
      className={cn(
        "h-8 w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1 text-base transition-colors outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 md:text-sm dark:bg-input/30",
        className,
      )}
      {...props}
    />
  );
}
