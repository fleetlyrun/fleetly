import { useSyncExternalStore } from "react";

// 项目上下文单源（UI v2 信息架构的锚）：切换器选中态全局共享 +
// localStorage 记忆（fleetly_console_project）。批 2 起项目 ID 进 URL
// （/p/$projectId/...），本 store 承担"上次选中"与"侧栏语境"两个职责。
const STORAGE_KEY = "fleetly_console_project";

let current = window.localStorage.getItem(STORAGE_KEY) ?? "";
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

export function getProjectId(): string {
  return current;
}

export function setProjectId(next: string) {
  current = next;
  if (next === "") window.localStorage.removeItem(STORAGE_KEY);
  else window.localStorage.setItem(STORAGE_KEY, next);
  emit();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useProjectId(): [string, (next: string) => void] {
  const value = useSyncExternalStore(subscribe, getProjectId);
  return [value, setProjectId];
}
