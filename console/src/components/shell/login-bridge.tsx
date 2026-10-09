import { useEffect } from "react";
import { getRouteApi, useRouter } from "@tanstack/react-router";
import { LoginPage } from "@/pages/Login";
import { useToken } from "@/lib/token";

const loginRoute = getRouteApi("/login");

// 登录桥（UI v2）：旧 LoginPage 纯靠 useToken 重渲染切壳（无路由概念），
// 路由化后由本桥监听凭证变化跳 ?next（缺省 /overview）。W1' 守卫
// （恰一 form 且无 form 祖先）锚在 LoginPage 本体，桥不包 form。
export function LoginBridge() {
  const [token] = useToken();
  const router = useRouter();
  const next = loginRoute.useSearch({ select: (search) => search.next }) ?? "/overview";

  useEffect(() => {
    if (token !== "") {
      router.history.push(next);
    }
  }, [token, next, router]);

  return <LoginPage />;
}
