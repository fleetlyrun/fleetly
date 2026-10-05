import { useSyncExternalStore } from "react";

// 凭证形态（ADR-0044 决策 2）：页头 Token 输入 + localStorage 持久化；
// 登录页是 F3.1 的事。模块级外部 store，供 fetch 面与输入框共享。
const STORAGE_KEY = "fleetly_console_token";

let current = window.localStorage.getItem(STORAGE_KEY) ?? "";
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

export function getToken(): string {
  return current;
}

export function setToken(next: string) {
  current = next.trim();
  if (current === "") window.localStorage.removeItem(STORAGE_KEY);
  else window.localStorage.setItem(STORAGE_KEY, current);
  emit();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// useToken 返回 [当前 Token, 设置函数]；设置即全量失效查询由调用方触发。
export function useToken(): [string, (next: string) => void] {
  const value = useSyncExternalStore(subscribe, getToken);
  return [value, setToken];
}
