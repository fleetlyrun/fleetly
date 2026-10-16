import { Outlet, createRootRoute } from "@tanstack/react-router";
import { CompassIcon } from "lucide-react";
import { EmptyState } from "@/components/domain/empty-state";

// 根路由：仅 Outlet；notFound 给可行动的诚实形态（壳外手输 typo 不落
// 白屏——ADR-0060:19 旧路径不做兼容重定向，未知地址一律本页兜底）。
export const Route = createRootRoute({
  component: () => <Outlet />,
  notFoundComponent: () => (
    <div className="grid min-h-svh place-items-center">
      <EmptyState
        icon={CompassIcon}
        title="Page not found"
        description="This address does not match anything in the console — addresses from the previous version are no longer remapped. Head back to the overview."
        actionLabel="Back to overview"
        onAction={() => window.location.assign("/overview")}
      />
    </div>
  ),
});
