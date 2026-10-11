import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import type { components } from "@/api/structure";
import { MinusIcon, PlusIcon } from "lucide-react";
import { toast } from "sonner";
import { useRoutes } from "@/lib/catalog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type AppSpec = components["schemas"]["v1AppSpec"];

// Processes 页（ADR-0060，用户裁决：App Detail 应有进程列表 + 管理面）：
// 冻结 Spec 全进程一览（Replicas/Strategy/Ports/Routes 归因）+ 实例数
// 暂存编辑——Apply changes 与 App Variables 同一暂存面（冻结 Spec 全量 +
// 改动组装 spec_file 走 Deploy，零 proto）。实例数语义 = spec replicas
// （0 = 挂起合法，"must not be negative" 是唯一下限——spec.go:316）。
export function AppProcessesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [staged, setStaged] = useState<Record<string, number>>({});
  const spec = useQuery({
    queryKey: ["resources", "app-spec", projectId, appId],
    queryFn: async (): Promise<AppSpec | null> => {
      try {
        const res = await apiFetch<{ spec?: AppSpec }>(`/v1/apps/${encodeURIComponent(appId)}/spec`);
        return res?.spec ?? null;
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null; // 未部署 = 无冻结 Spec（空态面，W-3）
        throw error;
      }
    },
    refetchInterval: 60_000,
  });
  const routes = useRoutes(projectId);
  const routesByProcess = new Map<string, number>();
  for (const route of routes.data ?? []) {
    if (route.app_id !== appId) continue;
    const key = route.process ?? "web";
    routesByProcess.set(key, (routesByProcess.get(key) ?? 0) + 1);
  }

  const frozen = spec.data;
  const processes = frozen?.processes ?? [];
  const replicasOf = (name: string): number => {
    if (staged[name] != null) return staged[name];
    const process = processes.find((entry) => entry.name === name);
    return Number(process?.replicas ?? 1);
  };
  const hasStagedEdits =
    frozen != null &&
    Object.entries(staged).some(([name, replicas]) => {
      const process = processes.find((entry) => entry.name === name);
      return replicas !== Number(process?.replicas ?? 1);
    });

  const deploy = useMutation({
    mutationFn: async () => {
      if (frozen == null) throw new Error("no frozen spec");
      const editedProcesses = processes.map((process) => {
        const replicas = staged[process.name ?? ""];
        if (replicas == null) return process;
        return { ...process, replicas: String(replicas) };
      });
      const edited: AppSpec = { ...frozen, processes: editedProcesses };
      return apiSend<{ deployment?: { id?: string } }>("/v1/deployments", "POST", {
        app_id: appId,
        spec_file: JSON.stringify(edited),
      });
    },
    onSuccess: (resp) => {
      setStaged({});
      void queryClient.invalidateQueries({ queryKey: ["resources", "app-spec", projectId, appId] });
      toast("Replicas staged as a new deployment");
      const id = resp?.deployment?.id;
      // 受理成功落部署详情：typed 路由 SPA 导航。相对 URL（../deployments/<id>）
      // 在 tab 路由下会解析成 /p/<pid>/apps/deployments/<id>（.. 吃掉 appId
      // 段）——404 页，禁回退。
      if (id) {
        void navigate({
          to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
          params: { projectId, appId, deploymentId: id },
        });
      }
    },
    onError: (cause) => toast.error(cause instanceof Error ? cause.message : String(cause)),
  });

  if (spec.isPending) {
    return <p className="py-10 text-center text-xs text-muted-foreground">Loading spec…</p>;
  }
  if (spec.isError) {
    return <p className="py-10 text-center text-xs text-destructive">{spec.error instanceof Error ? spec.error.message : String(spec.error)}</p>;
  }
  if (frozen == null) {
    return (
      <div className="rounded-xl border bg-card p-6 text-center">
        <h3 className="text-[13px] font-semibold">No frozen spec yet</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          The app has no deployment — its spec freezes on the first deploy, and its processes show up here.
        </p>
        {/* Deploy now 落部署 tab（Deploy Sheet 所在面）；旧 ?deploy=1 全仓无
            消费方，整页 reload 是死按钮。 */}
        <Button
          size="sm"
          className="mt-3"
          onClick={() => void navigate({ to: "/p/$projectId/apps/$appId/deployments", params: { projectId, appId } })}
        >
          Deploy now
        </Button>
      </div>
    );
  }

  return (
    <div className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
        <p className="mr-auto text-xs text-muted-foreground">
          {processes.length} process{processes.length === 1 ? "" : "es"} in the frozen spec · scaling stages a new deployment
        </p>
        {hasStagedEdits ? (
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setStaged({});
              toast("Staged edits discarded");
            }}
          >
            Discard
          </Button>
        ) : null}
        <Button size="sm" disabled={!hasStagedEdits || deploy.isPending} onClick={() => deploy.mutate()}>
          {deploy.isPending ? "Deploying…" : "Apply changes"}
        </Button>
      </div>

      {processes.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-muted-foreground">The frozen spec carries no processes.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Process</TableHead>
              <TableHead className="w-36">Replicas</TableHead>
              <TableHead>Strategy</TableHead>
              <TableHead>Ports</TableHead>
              <TableHead>Routes</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {processes.map((process) => {
              const name = process.name ?? "";
              const replicas = replicasOf(name);
              const stagedHere = staged[name] != null && staged[name] !== Number(process.replicas ?? 1);
              return (
                <TableRow key={name}>
                  <TableCell className="font-mono font-semibold">{name}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <Button
                        type="button"
                        variant="outline"
                        size="icon-sm"
                        aria-label={`Decrease replicas of ${name}`}
                        disabled={replicas <= 0}
                        onClick={() => setStaged((prev) => ({ ...prev, [name]: Math.max(0, replicas - 1) }))}
                      >
                        <MinusIcon />
                      </Button>
                      <Input
                        value={String(replicas)}
                        onChange={(event) => {
                          const next = event.target.value.replace(/[^0-9]/g, "");
                          setStaged((prev) => ({ ...prev, [name]: Math.min(999, Number(next === "" ? "0" : next)) }));
                        }}
                        inputMode="numeric"
                        aria-label={`Replicas of ${name}`}
                        className="h-7 w-14 text-center font-mono text-xs"
                      />
                      <Button
                        type="button"
                        variant="outline"
                        size="icon-sm"
                        aria-label={`Increase replicas of ${name}`}
                        onClick={() => setStaged((prev) => ({ ...prev, [name]: Math.min(999, replicas + 1) }))}
                      >
                        <PlusIcon />
                      </Button>
                      {stagedHere ? <span className="ml-1 text-[10.5px] font-semibold text-info">staged</span> : null}
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{strategyLabel(process.strategy)}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{process.ports?.length ?? 0}</TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{routesByProcess.get(name) ?? 0}</TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
      <p className="border-t px-4 py-2 text-[11px] text-muted-foreground">
        Replicas 0 suspends the process. Strategy comes from the frozen spec — change it at deploy time.
      </p>
    </div>
  );
}

// strategyLabel 把 DeployStrategy 归一为显示词（UNSPECIFIED = rolling，
// proto 语义：投影/引擎按 rolling 消费缺省零值）。
function strategyLabel(strategy: components["schemas"]["v1DeployStrategy"] | undefined): string {
  if (strategy === "DEPLOY_STRATEGY_BLUE_GREEN") return "blue-green";
  return "rolling";
}
