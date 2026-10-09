import { createFileRoute } from "@tanstack/react-router";
import { useRevisions } from "@/lib/catalog";
import { ExecTerminal } from "@/features/apps-tabs/exec-terminal";

// App 详情 · Terminal tab（IA v3 T2）：exec 会话（ADR-0049），app 语境固化，
// process 来自最新 revision 的 process_strategies。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/terminal")({
  component: AppTerminalRoute,
});

function AppTerminalRoute() {
  const { appId } = Route.useParams();
  const revisions = useRevisions(appId);
  const processes = (revisions.data?.[0]?.process_strategies ?? [])
    .map((entry) => entry?.process ?? "")
    .filter((name) => name !== "");
  return (
    <div className="mx-auto max-w-7xl px-6 pb-8">
      <ExecTerminal appId={appId} processes={processes} />
      <p className="pb-3 text-[11.5px] leading-relaxed text-muted-foreground">
        Sessions are interactive TTYs into a running replica (platform picks the first running instance). Change freeze does not
        cover exec — it is a diagnostics face (ADR-0049).
      </p>
    </div>
  );
}
