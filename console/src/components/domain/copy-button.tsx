import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

// CopyButton（UI v2 交互契约）：ID/URL/命令出现即可复制；成功走 toast
// 轻反馈 + 图标瞬态对勾。clipboard 不可达时降级选中提示（不假成功）。
export function CopyButton({
  value,
  label,
  className,
  toastMessage = "Copied to clipboard",
}: {
  value: string;
  label?: string;
  className?: string;
  toastMessage?: string;
}) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      toast(toastMessage);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Copy failed — clipboard unavailable");
    }
  }
  return (
    <button
      type="button"
      onClick={(event) => {
        event.stopPropagation();
        void copy();
      }}
      title={label ?? `Copy: ${value}`}
      className={cn(
        "inline-flex items-center gap-1 rounded-sm px-1 py-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground",
        className,
      )}
    >
      {copied ? <Check className="size-3" /> : <Copy className="size-3" />}
      {label ? <span className="text-xs font-medium">{label}</span> : null}
    </button>
  );
}
