import { createFileRoute, redirect } from "@tanstack/react-router";
import { getToken } from "@/lib/token";
import { ShellLayout } from "@/components/shell/shell-layout";

// 壳布局（pathless）：全部业务路由的父级。鉴权门在此一道（UI v2 契约
// §鉴权：无 token 一律回登录页带 next；401 全局闭环补 token 失效态）。
export const Route = createFileRoute("/_shell")({
  beforeLoad: () => {
    if (getToken() === "") {
      throw redirect({
        to: "/login",
        search: { next: `${window.location.pathname}${window.location.search}` },
      });
    }
  },
  component: ShellLayout,
});
