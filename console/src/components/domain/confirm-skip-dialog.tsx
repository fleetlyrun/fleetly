import { useState, type ReactNode } from "react";
import { useCommand } from "@lynx-go/uikit-core";
import type { CommandDescriptor, ResourceScope } from "@lynx-go/uikit-core/kernel";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { describeApiError, isConfirmationRequired } from "@/kit/errors";

// ConfirmSkipDialog = 强制确认动词的「重发确认」双击共享件（B1 W4 确认
// 闭环，设计稿 §1.6 最简形态；自 features/deployments/rollback-confirm
// 抽出通用化，2026-10-31 遗留直发面收口批）：
//   第一击：无票直发 → 服务端 ConfirmableVerbs 强制集拒绝
//           E_CONFIRMATION_REQUIRED（428 → rejected 结果通道，kit/errors）
//           ——这是**诚实的预期路径**，不是故障；弹窗切「已请求确认」态。
//   第二击：confirmation.skip=true 显式跳过（--yes 逃生门通道，审计
//           proof=skipped）——操作者的第二次点击即确认动作本身。
// 适用面：重发既有 Spec、无表单可预览的动词（Rollback / Redeploy /
// Processes 暂存提交钮 / Spec 编辑器与 Variables 的 deploy step——按钮
// 英文文案属调用点钦定词，豁免面见 wording 守卫）。
// 预览化升级路径（B2a，非本批验收形态）：spec_file 重发族本可经
// PreviewDeploy 铸票接 DeploySheet 预览面板（预览 → 带票确认），届时调用
// 点换壳为本件退役；Rollback 无 Preview RPC，双击是终态形态。
// 弹窗壳沿用裸 AlertDialog（保持开窗态承接两段交互；ConfirmDialog 落定
// 即关窗，不适配双击节奏——反模式守卫 ⑩ 清点在案）。
export function ConfirmSkipDialog<Input, Payload>({
  // —— 触发钮 ——
  label,
  icon,
  variant = "outline",
  size = "sm",
  disabled = false,
  // —— 弹窗文案（按动词语义化，调用点各自措辞） ——
  title,
  description,
  confirmLabel,
  confirmAgainLabel,
  pendingLabel,
  askedSubject,
  askedNote,
  // —— 命令接线（写必经 useCommand） ——
  descriptor,
  scope,
  buildInput,
  onOk,
}: {
  /** 触发钮钮词（动词语义本词，如 Redeploy / 暂存提交钮的钦定文案） */
  label: string;
  /** 触发钮前置图标（lucide，data-icon-start-inline 形态由调用方给） */
  icon?: ReactNode;
  variant?: React.ComponentProps<typeof Button>["variant"];
  size?: React.ComponentProps<typeof Button>["size"];
  disabled?: boolean;
  title: ReactNode;
  description: ReactNode;
  /** 首击确认钮词（与触发钮同词或其动词形态） */
  confirmLabel: string;
  /** 「已请求确认」态的二击钮词（Confirm <verb> 形态） */
  confirmAgainLabel: string;
  /** 命令在途的钮词 */
  pendingLabel: string;
  /** 「已请求确认」状态行的主语（单数/动名词，保持 "requires" 主谓一致） */
  askedSubject: string;
  /** 「已请求确认」状态行的无预览说明（各面诚实措辞） */
  askedNote: string;
  /** 双击承接的命令 descriptor（调用点单源） */
  descriptor: CommandDescriptor<Input, Payload>;
  /** 命令 scope（descriptor 自带 scopeOf 时省略走闭包派生） */
  scope?: ResourceScope;
  /** 击发时构造命令输入（skip=false 首击 / true 二击；点击时取最新闭包态） */
  buildInput: (skip: boolean) => Input;
  /** ok 落定回调（toast/导航等调用面语义；弹窗已先行关闭复位） */
  onOk?: (payload: Payload) => void;
}) {
  const [open, setOpen] = useState(false);
  // 「已请求确认」态：首击已收 REQUIRED，二击发 skip。关窗即复位。
  const [askedConfirm, setAskedConfirm] = useState(false);

  const command = useCommand(descriptor, {
    ...(scope !== undefined ? { scope } : {}),
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setOpen(false);
        setAskedConfirm(false);
        onOk?.(result.payload);
        return;
      }
      // 首击无票：预期拒绝——切双击态，提示重试确认（不 toast 打扰，
      // 弹窗内的状态行承载）。
      if (isConfirmationRequired(result)) {
        setAskedConfirm(true);
        return;
      }
      if (result != null) toast.error(result.message);
      else if (failure != null) toast.error(describeApiError(failure));
    },
  });

  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setAskedConfirm(false);
      }}
    >
      <AlertDialogTrigger asChild>
        <Button size={size} variant={variant} disabled={disabled || command.pending}>
          {icon}
          {label}
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {askedConfirm ? (
          // 双击态状态行：交代服务端拒绝的事实 + 第二击的语义（skip 进
          // 审计 proof=skipped）——操作者知道第二击意味着什么。
          <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-warning)_40%,transparent)] bg-[var(--status-warning-bg)] px-3 py-2 text-left text-2xs leading-snug text-fg">
            {askedSubject} requires an explicit confirmation — {askedNote}. Click{" "}
            <span className="font-semibold">{confirmAgainLabel}</span> again to proceed; the resend is recorded as an
            intentional skip (proof = skipped) in the audit log.
          </div>
        ) : null}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={command.pending}
            onClick={(event) => {
              // preventDefault：两段交互都在本窗内完成（Radix 默认关窗会
              // 截断双击态；ok 分支显式关窗）。
              event.preventDefault();
              // 第二击才带 skip=true（显式跳过预览确认；第一击让服务端把
              // 「需要确认」的事实说出来——双击的每一击都有明确的用户意图）。
              command.mutate(buildInput(askedConfirm));
            }}
          >
            {command.pending ? pendingLabel : askedConfirm ? confirmAgainLabel : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
