// vendored from @lynx-go/uikit registry/shell/section-card.tsx @0.3.1（2026-10-26 批0 底座重拷（demo 视觉原生落地））
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-muted ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 其余与上游一致。
import type { ReactNode } from "react";
import { Card, CardContent } from "@/components/ui/card";

/**
 * SectionCard（shell 块）——带标题的分区容器（设置页 = SectionCard × n；详情页
 * 内容槽的分区）。命名政策裁决：呈现的是布局分区而非值——落 shell 族，
 * 不进 values（§6.1 表格已同步）。内容零假设：任意 ReactNode。
 */

export interface SectionCardProps {
  title: string;
  description?: string;
  /** 标题行右侧动作槽（如区级开关/更多） */
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}

export function SectionCard({
  title,
  description,
  actions,
  children,
  className,
}: SectionCardProps) {
  return (
    <Card className={className} data-testid="section-card">
      <div className="flex items-start justify-between gap-3 px-4 pt-4">
        <div>
          <h2 className="text-sm font-medium">{title}</h2>
          {description ? <p className="mt-0.5 text-xs text-muted">{description}</p> : null}
        </div>
        {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
      </div>
      <CardContent className="pt-3">{children}</CardContent>
    </Card>
  );
}
