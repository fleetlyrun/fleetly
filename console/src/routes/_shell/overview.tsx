import { createFileRoute } from "@tanstack/react-router";
import { BoxIcon } from "lucide-react";
import { EmptyState } from "@/components/domain/empty-state";
import { PageHeader } from "@/components/domain/page-header";

// 过渡占位（批 1）：舰队总览在批 2 落地（项目磁贴墙+平台健康条+近期事件），
// 此前空路径收敛到本页并给出可达动作。
export const Route = createFileRoute("/_shell/overview")({
  component: OverviewPlaceholder,
});

function OverviewPlaceholder() {
  const navigate = Route.useNavigate();
  return (
    <div className="mx-auto max-w-6xl px-6 py-8">
      <PageHeader title="Overview" description="Fleet health at a glance — dashboard ships in the next milestone" />
      <EmptyState
        icon={BoxIcon}
        title="Dashboard is coming right up"
        description="Projects, platform health and recent events land with milestone 2. Everything else is already live in the sidebar."
        actionLabel="Go to apps"
        onAction={() => void navigate({ to: "/apps" })}
      />
    </div>
  );
}
