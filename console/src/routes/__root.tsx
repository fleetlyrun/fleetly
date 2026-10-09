import { Outlet, createRootRoute } from "@tanstack/react-router";
import { CompassIcon } from "lucide-react";
import { EmptyState } from "@/components/domain/empty-state";

// 根路由：仅 Outlet；notFound 给可行动的诚实形态（壳外手输 typo 不落
// 白屏——v2 路由族收窄后旧路径由迁移 shim 与本页兜底）。
export const Route = createRootRoute({
  component: () => <Outlet />,
  notFoundComponent: () => (
    <div className="grid min-h-svh place-items-center">
      <EmptyState
        icon={CompassIcon}
        title="Page not found"
        description="This address does not match anything in the console — deep links from the previous version are remapped automatically, this one was not recognized."
        actionLabel="Back to overview"
        onAction={() => window.location.assign("/overview")}
      />
    </div>
  ),
});
