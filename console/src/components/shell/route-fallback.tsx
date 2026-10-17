import { LoaderCircleIcon } from "lucide-react";

// 壳级路由 fallback（W-3）：TanStack 文件式路由按路由切片懒加载
// （React.lazy），慢链窗口内默认 fallback 是空——内容区整块全黑（Tasks
// 首访实录，秒级自愈但观感是壳坏了）。壳 Outlet 包 Suspense 落此兜底。
// 图标显式导入（ui/spinner 的 IconPlaceholder 注册表未收 Loader2，引它
// 生产渲染为空）；role/aria 在 wrapper（读屏可达，测试不依赖图标件）。
export function RouteFallback() {
  return (
    <div
      role="status"
      aria-label="Loading"
      className="flex min-h-[50vh] items-center justify-center p-10 text-muted-foreground"
    >
      <LoaderCircleIcon className="size-5 animate-spin" />
    </div>
  );
}
