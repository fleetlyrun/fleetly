import { createFileRoute, redirect } from "@tanstack/react-router";
import { z } from "zod";
import { getToken } from "@/lib/token";
import { LoginBridge } from "@/components/shell/login-bridge";

// 登录页（壳外路由）。?next= 仅收站内路径（防 open redirect）；
// 已持凭证访问登录页回总览。
const loginSearch = z.object({
  next: z
    .string()
    .optional()
    .refine((value) => value === undefined || (value.startsWith("/") && !value.startsWith("//")), {
      message: "next must be an in-app path",
    }),
});

export const Route = createFileRoute("/login")({
  validateSearch: loginSearch,
  beforeLoad: () => {
    if (getToken() !== "") throw redirect({ to: "/overview" });
  },
  component: LoginBridge,
});
