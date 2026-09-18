// 路由总装：登录态门卫（未登录一律落 /login）+ 全局 401 监听（会话失效
// 回登录页并提示）。

import { QueryClientProvider } from "@tanstack/react-query";
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
import { AppsPage } from "@/pages/AppsPage";
import { AppDetailLayout } from "@/pages/AppDetailLayout";
import { AppOverviewPage } from "@/pages/AppOverviewPage";
import { AppDeploymentsPage } from "@/pages/AppDeploymentsPage";
import { AppLogsPage } from "@/pages/AppLogsPage";
import { AppEnvPage } from "@/pages/AppEnvPage";
import { AppDomainsPage } from "@/pages/AppDomainsPage";
import { EventsPage } from "@/pages/EventsPage";
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

function Gate() {
  const { authed, logout } = useAuth();
  const [authError, setAuthError] = useState<string>("");

  useEffect(() => {
    // App 级 401 监听：任意请求被拒（token 失效/被吊销）→ 清凭据回登录页。
    setUnauthorizedListener((envelope) => {
      setAuthError(
        envelope.message
          ? `Session invalid: ${envelope.message}`
          : "Session invalid: token rejected (401)",
      );
      logout();
    });
    return () => setUnauthorizedListener(null);
  }, [logout]);

  if (!authed) {
    return <LoginPage authError={authError} onAuthErrorSeen={() => setAuthError("")} />;
  }

  return (
    <QueryClientProvider client={queryClient}>
      <Routes>
        <Route element={<RequireAuth><Layout /></RequireAuth>}>
          <Route path="/" element={<Navigate to="/apps" replace />} />
          <Route path="/apps" element={<AppsPage />} />
          <Route path="/apps/:name" element={<AppDetailLayout />}>
            <Route index element={<AppOverviewPage />} />
            <Route path="deployments" element={<AppDeploymentsPage />} />
            <Route path="logs" element={<AppLogsPage />} />
            <Route path="env" element={<AppEnvPage />} />
            <Route path="domains" element={<AppDomainsPage />} />
          </Route>
          <Route path="/system" element={<SystemPage />} />
          <Route path="/events" element={<EventsPage />} />
          <Route path="*" element={<Navigate to="/apps" replace />} />
        </Route>
      </Routes>
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
