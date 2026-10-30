import { useId, useState } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { confirmNameMatches, type ConfirmRisk } from "@/model/risk";

// ConfirmDialog 是确认动作弹窗的薄封装（自 identity.tsx 的 AlertDialogLike
// 收编全站）：标题/描述/确认词参数化 + 确认回调 pending 态内建（确认钮
// 禁用转圈，落定自动关窗）。破坏性操作契约的执法载体：删除/吊销/排空
// 一律 AlertDialog 确认，window.confirm 是反模式（守卫执法）；非破坏性
// 但不可逆的动作（lift freeze 等）传 destructive=false 换常规钮色。
//
// B4a 风险分级（2026-10-30）：risk 元数据 prop 必填（守卫执法——调用点
// 必带 tier），档位即弹窗形态契约（分级真源 @/model/risk）：
// - standard：常规窄框（原形态零漂移）；
// - wide：宽框（max-w 升档承载后果清单）+ 描述区下方「后果先行」段
//   （consequences：级联/保留清单，操作者先读后果再落到钮上）；
// - type_to_confirm：确认钮被输入门闩住——用户须在对话框内输入
//   confirmName（资源名或等价确认词，匹配口径见 confirmNameMatches：
//   trim + 大小写不敏感精确相等）才激活。
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  pendingLabel = "Working…",
  destructive = true,
  risk,
  confirmDisabled = false,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  pendingLabel?: string;
  destructive?: boolean;
  risk: ConfirmRisk;
  /** 外部门闩（B1 W4）：拆除面在 Confirm Ticket 到手前置灰确认钮——确认的正是展示的，无票即无确认。 */
  confirmDisabled?: boolean;
  onConfirm: () => unknown;
}) {
  const [pending, setPending] = useState(false);
  const [typedName, setTypedName] = useState("");
  const nameInputId = useId();
  // type_to_confirm 的输入门：不匹配则确认钮恒禁用。
  const nameGate = risk.tier === "type_to_confirm" ? confirmNameMatches(typedName, risk.confirmName) : true;
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        // 关窗即复位输入门（下次打开从空白重新确认）。
        if (!next) setTypedName("");
        onOpenChange(next);
      }}
    >
      {/* wide 档宽框：base 的 data-[size=default]:sm:max-w-sm 升到 lg，
          承载后果先行段（移动端保持默认窄宽——屏幕本身受限）。 */}
      <AlertDialogContent className={risk.tier === "wide" ? "data-[size=default]:sm:max-w-lg" : undefined}>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {risk.tier === "wide" ? (
          // 后果先行段（wide 档必带）：级联/保留清单的显式披露块，置于
          // 描述之下、动作钮之上；左对齐阅读（标题/描述区移动端居中）。
          // B1 W4：consequences 放宽为 ReactNode——拆除面由 PreviewTeardown
          // 载荷组结构化清单（级联 ul/保留 ul/冲突警示），字符串调用点零感。
          <div className="rounded-lg border bg-panel-2/40 px-3 py-2.5 text-left">
            <p className="text-2xs font-semibold uppercase tracking-wide text-muted">Consequences</p>
            <div className="mt-1 text-xs leading-relaxed text-fg">{risk.consequences}</div>
          </div>
        ) : null}
        {risk.tier === "type_to_confirm" ? (
          <div className="flex flex-col gap-1.5 text-left">
            <label htmlFor={nameInputId} className="text-xs text-muted">
              Type <span className="font-mono font-semibold text-fg">{risk.confirmName}</span> to confirm
            </label>
            <Input
              id={nameInputId}
              className="font-mono text-xs"
              value={typedName}
              onChange={(event) => setTypedName(event.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
        ) : null}
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction asChild>
            <Button
              variant={destructive ? "destructive" : "default"}
              disabled={pending || !nameGate || confirmDisabled}
              onClick={() => {
                setPending(true);
                void Promise.resolve(onConfirm())
                  .catch(() => undefined)
                  .finally(() => {
                    setPending(false);
                    onOpenChange(false);
                  });
              }}
            >
              {pending ? pendingLabel : confirmLabel}
            </Button>
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
