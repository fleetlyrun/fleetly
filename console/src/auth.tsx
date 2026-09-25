// 登录态（v0.3 RBAC W1 双凭据口径）：主路径 = 服务端会话（邮箱+口令登录
// /注册，HttpOnly cookie fleetly_session，浏览器侧零持久）；高级路径 =
// API token（sessionStorage 持久——P1-4 降权，Authorization Bearer——运维/
// CLI 直连）。启动经 GET /v1/auth/me 探测归位（loading → authed | anon，
// Gate 据此渲染 splash / 登录面 / 应用面，避免登录页闪烁）；会话失效的
// 401 由 api 层全局处置（清凭据 + App 级监听回登录页），本模块只提供
// clearSession 给监听侧调用。登录/注册成功路径整树清 react-query 缓存
// （P1-2：换身份不得复用上一账号的 Me 等投影）。

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { clearToken } from "@/api/client";
import {
  login as loginRequest,
  logout as logoutRequest,
  logoutAll as logoutAllRequest,
  me as meRequest,
  register as registerRequest,
} from "@/api/endpoints";
import { isApiError } from "@/api/errors";
import { queryClient } from "@/query";

export type AuthStatus = "loading" | "anon" | "authed";

export interface RegisterInput {
  email: string;
  password: string;
  /** 可选显示名（空 = 服务端缺省取 email 本地部分）。 */
  displayName?: string;
  /** 可选一次性邀请 token（P1-1 受邀注册：服务端豁免注册窗、同事务入队）。 */
  inviteToken?: string;
}

interface AuthContextValue {
  /** 启动探测进行中（Gate 渲染 splash，不闪登录页）。 */
  status: AuthStatus;
  /** status === "authed" 的布尔投影（路由门卫沿用）。 */
  authed: boolean;
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

  const loginWithPassword = useCallback(
    async (email: string, password: string) => {
      // 服务端成功即经 Set-Cookie 下发会话（HttpOnly——浏览器侧无凭据可
      // 持久）；本地存 token 的话（高级路径用过）不影响：双凭据 Bearer 优先。
      await loginRequest(email, password);
      // 换身份必清缓存（P1-2，2026-09-25 审查 §3）：登出→登录的竞态窗口里
      // 上一账号的 ["auth","me"] 等投影（staleTime 5min）可能存活，被
      // RequireAuth 探测/用户菜单直接吃掉 → 身份壳串号（Admin 导航/团队
      // 切换器仍为前一账号）。导航前整树清缓存，登录后的 Me/列表全部重探。
      queryClient.clear();
      setStatus("authed");
    },
    [],
  );

  const registerWithPassword = useCallback(async (input: RegisterInput) => {
    // Register 同样下发会话 cookie——注册即自动登录（S2 契约）。inviteToken
    // 非空时走服务端受邀注册通道（豁免注册窗 + 同事务消费邀请入队）。
    await registerRequest({
      email: input.email,
      password: input.password,
      ...(input.displayName ? { display_name: input.displayName } : {}),
      ...(input.inviteToken ? { invite_token: input.inviteToken } : {}),
    });
    // 同登录路径：注册即换身份，清缓存后再进入应用面（P1-2）。
    queryClient.clear();
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
      loginWithPassword,
      registerWithPassword,
      logout,
      logoutAll,
      clearSession,
    }),
    [status, loginWithPassword, registerWithPassword, logout, logoutAll, clearSession],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
