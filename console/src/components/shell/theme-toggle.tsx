import { MoonIcon, SunIcon } from "lucide-react";
import { useTheme } from "next-themes";
import { Button } from "@/components/ui/button";

// 主题开关（UI v2 双主题契约）：暗默认亮可切，next-themes class 形态 +
// fleetly_console_theme 记忆（index.html 防闪脚本同键）。
export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const dark = theme !== "light";
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label="Toggle theme"
      onClick={() => setTheme(dark ? "light" : "dark")}
    >
      {dark ? <SunIcon className="size-4" /> : <MoonIcon className="size-4" />}
    </Button>
  );
}
