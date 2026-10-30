import { Undo2Icon } from "lucide-react";
import { toast } from "sonner";
import { ConfirmSkipDialog } from "@/components/domain/confirm-skip-dialog";
import { deploymentsResource } from "@/kit/deployments";

// Rollback 确认闭环（B1 W4，设计稿 §1.6 重裁）：Rollback 在 ConfirmableVerbs
// 强制集但**无 Preview RPC 铸票**（票面绑定 = 目标 id + 当前 revision
// digest + 操作者，§1.2——服务端验票面存在但 console 无铸票入口），走
// 「重发确认」双击形态——首击 REQUIRED → 二击 skip=true（--yes 逃生门
// 通道，审计 proof=skipped），以最小机制保住「强制确认」语义（一击发
// 不出去，二击是显式同意）。双击机制自本件抽出为通用
// ConfirmSkipDialog（components/domain——Rollback/Redeploy/暂存提交钮
// 同族共享），此处只剩 Rollback 的动词面：文案与受理后跳转锚。
export function RollbackButton({
  projectId,
  appId,
  appLabel,
  disabled = false,
  label = "Rollback",
  onDeployed,
}: {
  projectId: string;
  appId: string;
  appLabel: string;
  disabled?: boolean;
  /** 两个调用面的钮词差异（hero=Rollback / 详情页=Rollback app） */
  label?: string;
  /** 受理成功后的跳转锚（无回调面只吃 toast） */
  onDeployed?: (deploymentId: string) => void;
}) {
  return (
    <ConfirmSkipDialog
      label={label}
      icon={<Undo2Icon data-icon-start-inline />}
      disabled={disabled}
      title={`Roll back ${appLabel}?`}
      description={
        "Re-deploys the last successful baseline as a new deployment — the current generation keeps serving until the rollout completes."
      }
      confirmLabel="Roll back"
      confirmAgainLabel="Confirm rollback"
      pendingLabel="Rolling back…"
      askedSubject="Rollback"
      askedNote="there is no preview for rollbacks"
      descriptor={deploymentsResource.commands.rollback}
      scope={{ projectId }}
      buildInput={(skip) => ({ appId, skip: skip || undefined })}
      onOk={(payload) => {
        toast("Rollback requested — a new deployment is rolling the previous baseline forward");
        if (payload.deploymentId) onDeployed?.(payload.deploymentId);
      }}
    />
  );
}
