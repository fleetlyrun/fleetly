// vendored from @lynx-go/uikit registry/command/form.tsx @0.3.1（2026-10-26 批2 写路径命令化）
// 拷贝适配（scripts/devtool.mjs --profile fleetly）：
// token 方言映射 本块无方言 token；cn 统一 @/lib/utils。
// 手工步骤（判断题，工具不自动改）：
// - 未映射方言 token text-err ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// - 未映射方言 token bg-err ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// - 未映射方言 token border-err ×1——每块语境判断（torchwood amber/emerald、fleetly status vars 先例）
// 其余与上游一致。
import type { CommandResult } from "@lynx-go/uikit-core/kernel";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";

/**
 * Form 内芯（command 块）—— 字段族 × 错误盒 × 提交生命周期；宿主无关
 * （Dialog/Sheet/整页由外壳决定，submit 由内芯渲染，宿主 footer 经 onCancel 协作）。
 * 结果分支渲染在此：rejected/conflict 展示 message-first 错误盒（结果通道，非异常）。
 */
export interface FormProps<Payload> {
  /**
   * 结构化：任何带 handleSubmit 的 form api（useCommandForm / 裸 TanStack Form 皆可）。
   * 注意：TanStack Form 1.33 的 handleSubmit(submitMeta?) 不接收事件、不 preventDefault
   * ——本块统一在此拦截原生提交（直连 onSubmit={form.handleSubmit} 会导致整页原生 GET 提交）。
   */
  form: { handleSubmit: (submitMeta?: unknown) => void | Promise<void> };
  submitting?: boolean;
  result?: CommandResult<Payload> | null;
  /** 结果盒内的动作槽（conflict 的"刷新重载"等页面编排；§12.10-⑤ 回流） */
  resultAction?: ReactNode;
  submitLabel?: string;
  /** 提交中标签；缺省 "Saving…" */
  submittingLabel?: string;
  cancelLabel?: string;
  onCancel?: () => void;
  children: ReactNode;
}

export function Form<Payload>({
  form,
  submitting = false,
  result = null,
  resultAction,
  submitLabel = "Save",
  submittingLabel = "Saving…",
  cancelLabel = "Cancel",
  onCancel,
  children,
}: FormProps<Payload>) {
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void form.handleSubmit();
      }}
      className="space-y-3"
      noValidate
    >
      {children}
      {result !== null && result.status !== "ok" ? (
        <div
          role="alert"
          className="flex items-center justify-between gap-3 rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err"
        >
          <span>
            {result.status === "rejected" && result.code ? (
              <span className="font-mono">[{result.code}] </span>
            ) : null}
            {result.message}
          </span>
          {resultAction}
        </div>
      ) : null}
      <div className="flex justify-end gap-2 pt-1">
        {onCancel ? (
          <Button type="button" variant="ghost" onClick={onCancel}>
            {cancelLabel}
          </Button>
        ) : null}
        <Button type="submit" disabled={submitting}>
          {submitting ? submittingLabel : submitLabel}
        </Button>
      </div>
    </form>
  );
}
