import { createFileRoute } from "@tanstack/react-router";
import { RegistryView } from "@/features/registry/registry-view";

// Registry v1（IA v3 T5）：按 app 的当前运行内容（revision digest）。
export const Route = createFileRoute("/_shell/p/$projectId/registry")({
  component: RegistryRoute,
});

function RegistryRoute() {
  const { projectId } = Route.useParams();
  return <RegistryView projectId={projectId} />;
}
