import type { ComponentProps } from "react";
import { Checkbox as RadixCheckbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";

// uikit 块的 Checkbox 基元桥（批0 re-vendor 新增面）：demo 的 data-table 行
// 选择按 Base UI 方言直用 { checked, indeterminate, onChange }；本仓
// @/components/ui/checkbox 是 Radix 形态（onCheckedChange，半选经
// checked="indeterminate"）。块不改源，桥在此做 props 方言映射。
export interface KitCheckboxProps extends Omit<ComponentProps<"button">, "onChange" | "checked"> {
  checked?: boolean;
  indeterminate?: boolean;
  onChange?: (next: boolean) => void;
  disabled?: boolean;
}

export function Checkbox({ checked, indeterminate, onChange, disabled, className, ...rest }: KitCheckboxProps) {
  return (
    <RadixCheckbox
      className={cn(className)}
      checked={indeterminate ? "indeterminate" : (checked ?? false)}
      onCheckedChange={(value) => onChange?.(value === true)}
      disabled={disabled}
      aria-invalid={rest["aria-invalid"]}
    />
  );
}
