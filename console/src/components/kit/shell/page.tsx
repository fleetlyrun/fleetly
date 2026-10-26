// vendored from @lynx-go/uikit registry/shell/page.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 其余与上游一致。
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * Page（shell 块）—— 所有页面（列表/详情/自定义内容）通用的页外壳。
 * 不是 CustomPage：自定义内容页是缺省状态（普通路由 + 任意组合），Page 只提供骨架。
 * 宽度三档（fleetly PageFrame 实证）：wide / narrow / flow（不限宽）。
 */
export interface PageProps {
  title: string;
  description?: string;
  actions?: ReactNode;
  width?: "wide" | "narrow" | "flow";
  children: ReactNode;
}

const WIDTHS: Record<NonNullable<PageProps["width"]>, string> = {
  wide: "max-w-6xl",
  narrow: "max-w-3xl",
  flow: "max-w-none",
};

export function Page({ title, description, actions, width = "wide", children }: PageProps) {
  return (
    <div className={cn("mx-auto w-full space-y-4 p-6", WIDTHS[width])}>
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold">{title}</h1>
          {description ? <p className="mt-0.5 text-xs text-muted">{description}</p> : null}
        </div>
        {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
      </header>
      {children}
    </div>
  );
}
