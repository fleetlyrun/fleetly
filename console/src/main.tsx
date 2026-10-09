import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter } from "@tanstack/react-router";
import { ThemeProvider } from "next-themes";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { setUnauthorizedHandler } from "./api/client";
import { hashToPath } from "./lib/legacy-hash";
import { getToken, setToken } from "./lib/token";
import { routeTree } from "./routeTree.gen";
import { Toaster } from "@/components/ui/sonner";
import "./styles.css";

// 轮询面统一 5s（部署页）；失败重试退避交给 react-query 默认策略。
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

// UI v2（ADR-0057）：TanStack Router 接管导航；hover 预取（intent）。
const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

// 401 全局闭环：持凭证态收到 401 = 会话失效——清 token/缓存，回登录页
// 带 next。登录页自身的验证失败不触发（无 token），不打断表单报错。
setUnauthorizedHandler(() => {
  if (getToken() === "") return;
  setToken("");
  queryClient.clear();
  const current = router.state.location.pathname;
  if (current !== "/login") {
    router.history.push(`/login?next=${encodeURIComponent(current)}`);
  }
});

// 旧 hash 深链一次性迁移（ADR-0057）：首渲染前把 #/... 重写为干净路径。
const migrated = hashToPath(window.location.hash);
if (migrated) window.history.replaceState(null, "", migrated);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* 双主题（ADR-0057）：暗默认亮可切；class 形态 + index.html 防闪同键 */}
      <ThemeProvider attribute="class" defaultTheme="dark" enableSystem={false} storageKey="fleetly_console_theme">
        <RouterProvider router={router} />
        <Toaster position="bottom-right" />
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
);
