import { createFileRoute } from "@tanstack/react-router";
import { ManagedProvidersView } from "@/features/fleet/providers-view";

// Components（IA v3 T6，ADR-0058 定名 + ADR-0059 复裁；route 保持 /providers）。
export const Route = createFileRoute("/_shell/providers")({
  component: ProvidersRoute,
});

function ProvidersRoute() {
  return <ManagedProvidersView />;
}
