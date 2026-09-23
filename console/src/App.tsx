// 路由总装：认证门卫三态（启动 Me 探测 splash → 未登录〔登录页/邀请页/
// 深链暂存〕→ 已登录应用面）+ 全局 401 监听（会话失效清本地态回登录页
// 并提示）。

import { QueryClientProvider } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import {
  BrowserRouter,
  Navigate,
  Route,
  Routes,
  useLocation,
} from "react-router-dom";

import { AuthProvider, useAuth } from "@/auth";
import { setUnauthorizedListener } from "@/api/client";
import { Layout } from "@/components/layout";
import { HomePage } from "@/pages/HomePage";
import { AppsPage } from "@/pages/AppsPage";
import { AppDetailLayout } from "@/pages/AppDetailLayout";
import { AppOverviewPage } from "@/pages/AppOverviewPage";
import { AppDeploymentsPage } from "@/pages/AppDeploymentsPage";
import { AppLogsPage } from "@/pages/AppLogsPage";
import { AppEnvPage } from "@/pages/AppEnvPage";
import { AppSecretsPage } from "@/pages/AppSecretsPage";
import { AppDomainsPage } from "@/pages/AppDomainsPage";
import { AppTerminalPage } from "@/pages/AppTerminalPage";
import { DatabasesPage } from "@/pages/DatabasesPage";
import { DatabaseDetailPage } from "@/pages/DatabaseDetailPage";
import { EventsPage } from "@/pages/EventsPage";
import { InvitePage } from "@/pages/InvitePage";
import { LoginPage } from "@/pages/LoginPage";
import { SystemPage } from "@/pages/SystemPage";
import { queryClient } from "@/query";

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { authed } = useAuth();
  const location = useLocation();
  if (!authed) {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />;
  }
  return <>{children}</>;
}

/** 启动 Me 探测进行中：占位 splash（避免登录页闪烁）。 */
function AuthSplash() {
  return (
    <div
      className="flex min-h-screen items-center justify-center bg-muted/30"
      data-testid="auth-splash"
    >
      <Loader2 aria-hidden className="h-6 w-6 animate-spin text-muted-foreground" />
    </div>
  );
}

/**
 * 匿名态路由：/login 直达登录页；邀请链接页可达（未登录分支——提示先
 * 登录/注册）；其余深链（/apps/...）经 Navigate 暂存 from（含查询串，
 * 邀请链接回跳依赖它）后落登录页，登录成功 navigate(from) 恢复。
 */
function AnonRoutes({
  authError,
  onAuthErrorSeen,
}: {
  authError?: string;
  onAuthErrorSeen?: () => void;
}) {
  const location = useLocation();
  if (location.pathname === "/login") {
    return <LoginPage authError={authError} onAuthErrorSeen={onAuthErrorSeen} />;
  }
  return (
    <Routes>
      <Route path="/auth/invite" element={<InvitePage />} />
      <Route
        path="*"
        element={
          <Navigate
            to="/login"
            state={{ from: location.pathname + location.search }}
            replace
          />
        }
      />
    </Routes>
  );
}

/** 已登录应用面（含邀请页的已登录分支——进入即自动 accept）。 */
function AuthedRoutes() {
  return (
    <Routes>
      <Route element={<RequireAuth><Layout /></RequireAuth>}>
        <Route path="/" element={<HomePage />} />
        <Route path="/apps" element={<AppsPage />} />
        <Route path="/apps/:name" element={<AppDetailLayout />}>
          <Route index element={<AppOverviewPage />} />
          <Route path="deployments" element={<AppDeploymentsPage />} />
          <Route path="logs" element={<AppLogsPage />} />
          <Route path="env" element={<AppEnvPage />} />
          <Route path="secrets" element={<AppSecretsPage />} />
          <Route path="domains" element={<AppDomainsPage />} />
          <Route path="terminal" element={<AppTerminalPage />} />
        </Route>
        <Route path="/databases" element={<DatabasesPage />} />
        <Route path="/databases/:name" element={<DatabaseDetailPage />} />
        <Route path="/system" element={<SystemPage />} />
        <Route path="/events" element={<EventsPage />} />
        <Route path="/auth/invite" element={<InvitePage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

function Gate() {
  const { status, clearSession } = useAuth();
  const [authError, setAuthError] = useState<string>("");

  useEffect(() => {
    // App 级 401 监听：任意资源请求被拒（会话/令牌失效或被吊销）→ 清本地
    // 凭据回登录页（服务端 cookie 由失效响应自身清，不另发注销请求）。
    setUnauthorizedListener((envelope) => {
      setAuthError(
        envelope.message
          ? `Session invalid: ${envelope.message}`
          : "Session invalid: credentials rejected (401)",
      );
      clearSession();
    });
    return () => setUnauthorizedListener(null);
  }, [clearSession]);

  return (
    <QueryClientProvider client={queryClient}>
      {status === "loading" ? (
        <AuthSplash />
      ) : status === "anon" ? (
        <AnonRoutes authError={authError} onAuthErrorSeen={() => setAuthError("")} />
      ) : (
        <AuthedRoutes />
      )}
    </QueryClientProvider>
  );
}

export function App() {
  return (
    <AuthProvider>
      <BrowserRouter basename="/ui">
        <Gate />
      </BrowserRouter>
    </AuthProvider>
  );
}
