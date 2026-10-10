import { useState } from "react";
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

// ConfirmDialog 是确认动作弹窗的薄封装（自 identity.tsx 的 AlertDialogLike
// 收编全站）：标题/描述/确认词参数化 + 确认回调 pending 态内建（确认钮
// 禁用转圈，落定自动关窗）。破坏性操作契约的执法载体：删除/吊销/排空
// 一律 AlertDialog 确认，window.confirm 是反模式（守卫执法）；非破坏性
// 但不可逆的动作（lift freeze 等）传 destructive=false 换常规钮色。
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  pendingLabel = "Working…",
  destructive = true,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  pendingLabel?: string;
  destructive?: boolean;
  onConfirm: () => unknown;
}) {
  const [pending, setPending] = useState(false);
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction asChild>
            <Button
              variant={destructive ? "destructive" : "default"}
              disabled={pending}
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
