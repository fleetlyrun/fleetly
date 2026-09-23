// 登录态（v0.3 RBAC W1 双凭据口径）：主路径 = 服务端会话（邮箱+口令登录
// /注册，HttpOnly cookie fleetly_session，浏览器侧零持久）；高级路径 =
// API token（localStorage 持久，Authorization Bearer——运维/CLI 直连）。
// 启动经 GET /v1/auth/me 探测归位（loading → authed | anon，Gate 据此渲染
// splash / 登录面 / 应用面，避免登录页闪烁）；会话失效的 401 由 api 层
// 全局处置（清凭据 + App 级监听回登录页），本模块只提供 clearSession 给
// 监听侧调用。

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { clearToken, setToken } from "@/api/client";
import {
  login as loginRequest,
  logout as logoutRequest,
  logoutAll as logoutAllRequest,
  me as meRequest,
  register as registerRequest,
} from "@/api/endpoints";
import { isApiError } from "@/api/errors";

export type AuthStatus = "loading" | "anon" | "authed";

export interface RegisterInput {
  email: string;
  password: string;
  /** 可选显示名（空 = 服务端缺省取 email 本地部分）。 */
  displayName?: string;
}

interface AuthContextValue {
  /** 启动探测进行中（Gate 渲染 splash，不闪登录页）。 */
  status: AuthStatus;
  /** status === "authed" 的布尔投影（路由门卫沿用）。 */
  authed: boolean;
  /** 高级路径：持久化 API token 并进入（调用方先自行校验——M9-1 先验后存）。 */
  loginWithToken(token: string): void;
  /** 主路径：邮箱+口令登录（成功 = 服务端已下发会话 cookie）。 */
  loginWithPassword(email: string, password: string): Promise<void>;
  /** 自助注册（成功即下发会话——自动登录）。 */
  registerWithPassword(input: RegisterInput): Promise<void>;
  /** 注销当前会话（POST /auth/logout 尽力而为 + 清本地态）。 */
  logout(): Promise<void>;
  /** 全部注销（POST /auth/logout-all；返回吊销会话行数）。 */
  logoutAll(): Promise<number>;
  /** 仅清本地态（401 全局处置路径——服务端会话已失效，无需再请求）。 */
  clearSession(): void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  // 存量 token 直接进入 authed 候选态？否——token 有效性也统一走 me 探测
  // （Bearer 双凭据：S2 起 me 同示两种凭据，Bearer 由 api 层自动携带）；
  // 探测期间 Gate 渲染 splash。
  const [status, setStatus] = useState<AuthStatus>("loading");

  // 启动探测：GET /v1/auth/me。optionalAuth——「未登录」是探测的正常结果
  // 而非会话失效事件，不得触发全局登出横幅。
  useEffect(() => {
    let cancelled = false;
    meRequest({ optionalAuth: true })
      .then(() => {
        if (!cancelled) setStatus("authed");
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        // 401 = 凭据失效（cookie 过期 / 存量 token 被吊销）——清残留 token
        // 归位匿名；其余（网络/5xx）同样落匿名，登录页可如实重试。
        if (isApiError(err) && err.status === 401) clearToken();
        setStatus("anon");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const loginWithToken = useCallback((next: string) => {
    const trimmed = next.trim();
    setToken(trimmed);
    setStatus("authed");
  }, []);

  const loginWithPassword = useCallback(
    async (email: string, password: string) => {
      // 服务端成功即经 Set-Cookie 下发会话（HttpOnly——浏览器侧无凭据可
      // 持久）；本地存 token 的话（高级路径用过）不影响：双凭据 Bearer 优先。
      await loginRequest(email, password);
      setStatus("authed");
    },
    [],
  );

  const registerWithPassword = useCallback(async (input: RegisterInput) => {
    // Register 同样下发会话 cookie——注册即自动登录（S2 契约）。
    await registerRequest({
      email: input.email,
      password: input.password,
      ...(input.displayName ? { display_name: input.displayName } : {}),
    });
    setStatus("authed");
  }, []);

  const clearSession = useCallback(() => {
    clearToken();
    setStatus("anon");
  }, []);

  const logout = useCallback(async () => {
    // 尽力而为：会话可能已失效（POST 自身 401，optionalAuth 已豁免全局
    // 处置）——本地态照清，登录页即归宿。
    try {
      await logoutRequest();
    } catch {
      // 忽略：失败的唯一后果是服务端会话行多活一瞬（TTL 兜底）。
    }
    clearToken();
    setStatus("anon");
  }, []);

  const logoutAll = useCallback(async () => {
    let revoked = 0;
    try {
      const res = await logoutAllRequest();
      // int64 的 proto3 JSON 形态是字符串。
      revoked = Number(res.sessions_revoked ?? 0);
    } catch {
      // 同 logout：尽力而为。
    }
    clearToken();
    setStatus("anon");
    return revoked;
  }, []);

  const value = useMemo(
    () => ({
      status,
      authed: status === "authed",
      loginWithToken,
      loginWithPassword,
      registerWithPassword,
      logout,
      logoutAll,
      clearSession,
    }),
    [status, loginWithToken, loginWithPassword, registerWithPassword, logout, logoutAll, clearSession],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
