import { createFileRoute } from "@tanstack/react-router";
import { ManagedProvidersView } from "@/features/fleet/providers-view";

// Components（IA v3 T6，ADR-0058 定名 + ADR-0059 复裁；ADR-0060 路由对齐页名 /components）。
export const Route = createFileRoute("/_shell/components")({
  component: ProvidersRoute,
});

function ProvidersRoute() {
  return <ManagedProvidersView />;
}
