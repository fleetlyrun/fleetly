import { createFileRoute } from "@tanstack/react-router";
import { useCollection } from "@lynx-go/uikit-core";
import { ExecTerminal } from "@/features/apps-tabs/exec-terminal";
import { revisionsResource, revisionsLookup } from "@/kit/revisions";

// App 详情 · Terminal tab（IA v3 T2）：exec 会话（ADR-0049），app 语境固化，
// process 来自最新 revision 的 process_strategies。
export const Route = createFileRoute("/_shell/p/$projectId/apps/$appId/terminal")({
  component: AppTerminalRoute,
});

function AppTerminalRoute() {
  // 批4（2026-10-27）：revisions 读面迁 kit useCollection（大页 lookup 不
  // 截断；15s 轮询同旧拍）；exec 观测面仍走 catalog（无 kit 资源）。
  const { projectId, appId } = Route.useParams();
  const revisions = useCollection(revisionsResource, {
    from: Route.id,
    search: revisionsLookup,
    scope: { projectId, appId },
    query: { refetchInterval: 15_000 },
  });
  const processes = (revisions.rows[0]?.process_strategies ?? [])
    .map((entry) => entry?.process ?? "")
    .filter((name) => name !== "");
  return (
    <>
      <ExecTerminal appId={appId} processes={processes} />
      <p className="text-2xs leading-relaxed text-muted">
        Sessions are interactive TTYs into a running replica — anchor a specific one via Instance (auto = platform picks the
        first running). Change freeze does not cover exec — it is a diagnostics face (ADR-0049).
      </p>
    </>
  );
}
