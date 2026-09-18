// 登录态：v0.1 单操作员口径——token 粘贴表单 + localStorage 持久。
// 所有请求经 api client 自动带 Bearer；401 统一回登录页（App 级监听）。

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { clearToken, getToken, setToken } from "@/api/client";

interface AuthContextValue {
  /** localStorage 有 token 即视为已登录（有效性由首个请求校验）。 */
  authed: boolean;
  login: (token: string) => void;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

function readToken(): string {
  return getToken();
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setTokenState] = useState<string>(readToken);

  const login = useCallback((next: string) => {
    const trimmed = next.trim();
    setToken(trimmed);
    setTokenState(trimmed);
  }, []);

  const logout = useCallback(() => {
    clearToken();
    setTokenState("");
  }, []);

  const value = useMemo(
    () => ({ authed: token !== "", login, logout }),
    [token, login, logout],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
