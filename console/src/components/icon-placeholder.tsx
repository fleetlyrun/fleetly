import type { LucideIcon } from "lucide-react";
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  CircleCheckIcon,
  InfoIcon,
  MoreHorizontalIcon,
  OctagonXIcon,
  PanelLeftIcon,
  SearchIcon,
  TriangleAlertIcon,
  XIcon,
} from "lucide-react";

// shadcn ui/** 图标注册表：仅收 vendored 组件 IconPlaceholder 实际引用的
// lucide 名（显式导入保 tree-shaking——`import * as icons` 会把全量图标
// 拖进 vendor，走查性能批口径不允许）。ui/** 新增 lucide="XIcon" 引用时
// 在此补一行。
const REGISTRY: Record<string, LucideIcon | undefined> = {
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  CircleCheckIcon,
  InfoIcon,
  MoreHorizontalIcon,
  OctagonXIcon,
  PanelLeftIcon,
  SearchIcon,
  TriangleAlertIcon,
  XIcon,
};

export interface IconPlaceholderProps {
  lucide: string;
  tabler?: string;
  hugeicons?: string;
  phosphor?: string;
  remixicon?: string;
  className?: string;
}

// shadcn/radix-nova 预设的图标抽象位：本仓钉死 lucide（components.json
// iconLibrary），按注册表直取渲染；未注册名渲染 null（开发期显性缺口）。
export function IconPlaceholder({ lucide, tabler: _t, hugeicons: _h, phosphor: _p, remixicon: _r, ...rest }: IconPlaceholderProps) {
  const Icon = REGISTRY[lucide];
  if (!Icon) return null;
  return <Icon {...rest} />;
}
