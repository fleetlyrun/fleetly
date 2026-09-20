// 主题切换：亮/暗两态（dokploy 同款语义）。偏好持久化 localStorage，
// 首帧防闪由 index.html 的内联脚本负责——本组件只读当前态并翻转。

import { Moon, Sun } from "lucide-react";
import { useState } from "react";

import { Button } from "@/components/ui/button";

export const THEME_STORAGE_KEY = "fleetly.console.theme";

function currentTheme(): "light" | "dark" {
  if (typeof document === "undefined") return "light";
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function ThemeToggle() {
  const [theme, setTheme] = useState<"light" | "dark">(currentTheme);

  function toggle() {
    const next = theme === "dark" ? "light" : "dark";
    document.documentElement.classList.toggle("dark", next === "dark");
    try {
      localStorage.setItem(THEME_STORAGE_KEY, next);
    } catch {
      // 存储不可用（隐私模式等）——仅本次会话生效。
    }
    setTheme(next);
  }

  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
      onClick={toggle}
    >
      {theme === "dark" ? (
        <Sun aria-hidden className="h-4 w-4" />
      ) : (
        <Moon aria-hidden className="h-4 w-4" />
      )}
    </Button>
  );
}
