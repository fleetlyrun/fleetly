import type { LucideIcon } from "lucide-react";
import * as icons from "lucide-react";
import type { ComponentProps } from "react";

// shadcn/radix-nova 预设的图标抽象位：本仓钉死 lucide（components.json
// iconLibrary），按 lucide 命名（含 `XIcon` 别名导出）直取渲染；其余图标
// 库名（tabler/hugeicons/phosphor/remixicon）按预设契约收下即弃。
export interface IconPlaceholderProps extends ComponentProps<"svg"> {
  lucide: string;
  tabler?: string;
  hugeicons?: string;
  phosphor?: string;
  remixicon?: string;
}

const REGISTRY = icons as unknown as Record<string, LucideIcon | undefined>;

export function IconPlaceholder({ lucide, tabler: _t, hugeicons: _h, phosphor: _p, remixicon: _r, ...rest }: IconPlaceholderProps) {
  const Icon = REGISTRY[lucide];
  if (!Icon) return null;
  return <Icon {...rest} />;
}
