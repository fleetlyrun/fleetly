import { createFileRoute } from "@tanstack/react-router";
import { ManagedProvidersView } from "@/features/fleet/providers-view";

// Managed Providers（IA v3 T6，ADR-0058）：受管 Provider 实例的排障面。
export const Route = createFileRoute("/_shell/providers")({
  component: ProvidersRoute,
});

function ProvidersRoute() {
  return <ManagedProvidersView />;
}
