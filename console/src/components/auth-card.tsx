// 认证面卡片壳（登录页 / 邀请链接页共用）：fleetly 标识 + 居中单卡布局。

import type { ReactNode } from "react";

import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

export function AuthCard({
  description,
  children,
}: {
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-6">
      <Card className="w-full max-w-md">
        <CardHeader>
          <div className="mb-1 flex items-center gap-2.5">
            <span className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
              f
            </span>
            <div className="leading-tight">
              <CardTitle className="text-base">fleetly</CardTitle>
              <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
                console
              </span>
            </div>
          </div>
          {description ? <CardDescription>{description}</CardDescription> : null}
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </div>
  );
}
