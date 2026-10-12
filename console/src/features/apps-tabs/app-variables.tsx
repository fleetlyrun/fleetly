import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import { PlusIcon, SlidersIcon, TrashIcon } from "lucide-react";
import { toast } from "sonner";
import { CopyButton } from "@/components/domain/copy-button";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { fetchFrozenSpec, useSpecDeploy } from "@/lib/spec-deploy";

type VariableRow = { id?: string; name: string; value: string; updated_at?: string };

// Variables tab（ADR-0072 重做）：App 级变量是平台一等聚合——store 编辑器
// + Apply（写 store + 重发冻结 Spec：三级合成顶层在冻结咽喉取新值，故
// 变量轮换 = 改 store + 一次部署；git hook 应用同样适用——spec_file 按
// 运行时 overlay 受理，材料半边不动）。生效 env 是只读投影（冻结体合成
// 终态）；per-process 的源声明 env 与 secret_refs 属 Spec 高级面（Spec tab
// 编辑器 / Deploy 表单），不在此重复。
export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const store = useQuery({
    queryKey: ["resources", "app-variables", appId],
    queryFn: async () => {
      const res = await apiFetch<{ variables?: VariableRow[] }>(
        `/v1/app-variables?app_id=${encodeURIComponent(appId)}&limit=200`,
      );
      return (res?.variables ?? []).slice();
    },
  });
  const spec = useQuery({
    queryKey: ["resources", "app-spec", projectId, appId],
    queryFn: () => fetchFrozenSpec(appId),
    refetchInterval: 60_000,
  });

  // 暂存面：行集 = store 基线 + 本地增删改；Apply 一次性落库 + 部署。
  const [staged, setStaged] = useState<VariableRow[] | null>(null);
  const rows = staged ?? (store.data ?? []);
  const base = store.data ?? [];
  const hasChanges =
    staged != null &&
    (staged.length !== base.length ||
      staged.some((row, i) => row.name !== base[i]?.name || row.value !== base[i]?.value));

  const deploy = useSpecDeploy(appId, (id) => {
    void navigate({
      to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
      params: { projectId, appId, deploymentId: id },
    });
  });

  const apply = useMutation({
    mutationFn: async () => {
      const baseline = new Map(base.map((row) => [row.name, row]));
      for (const row of staged ?? []) {
        if (row.name === "" ) continue;
        if (baseline.get(row.name)?.value === row.value) continue; // 未变键不重写
        await apiSend("/v1/app-variables", "PUT", { app_id: appId, name: row.name, value: row.value });
      }
      for (const row of base) {
        if (!(staged ?? []).some((kept) => kept.name === row.name)) {
          await apiSend(`/v1/app-variables/${encodeURIComponent(appId)}/${encodeURIComponent(row.name)}`, "DELETE");
        }
      }
      // 变量生效需一次部署（冻结咽喉合成）；无冻结体的 App 在首部署取新值。
      const frozen = await fetchFrozenSpec(appId);
      if (frozen) await deploy.mutateAsync(frozen);
    },
    onSuccess: async () => {
      setStaged(null);
      await queryClient.invalidateQueries({ queryKey: ["resources", "app-variables", appId] });
      toast("Variables saved — a new deployment rolls the new values");
    },
    onError: (cause) => toast.error(cause instanceof Error ? cause.message : String(cause)),
  });

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <SlidersIcon className="size-3.5 text-muted-foreground" />
            App variables
          </CardTitle>
          <span className="ml-auto text-2xs text-muted-foreground">
            top merge layer · app variable &gt; source env &gt; project shared · the next deploy bakes the values
          </span>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {store.isError ? (
            <p className="py-4 text-center text-xs text-muted-foreground">
              {store.error instanceof ApiError ? store.error.message : String(store.error)}
            </p>
          ) : rows.length === 0 ? (
            <p className="py-2 text-center text-xs text-muted-foreground">
              No app variables — add one; values land in every process environment at the next deploy.
            </p>
          ) : (
            <div className="flex flex-col gap-2">
              {rows.map((row, index) => {
                const isExisting = base.some((baselineRow) => baselineRow.name === row.name);
                return (
                  <div key={`${row.name}-${index}`} className="flex items-center gap-2">
                    <Input
                      className="h-8 w-48 font-mono text-xs"
                      value={row.name}
                      disabled={isExisting}
                      placeholder="NAME"
                      aria-label="Variable name"
                      onChange={(event) =>
                        setStaged(rows.map((r, i) => (i === index ? { ...r, name: event.target.value } : r)))
                      }
                    />
                    <Input
                      className="h-8 flex-1 font-mono text-xs"
                      value={row.value}
                      placeholder="value"
                      aria-label="Variable value"
                      onChange={(event) =>
                        setStaged(rows.map((r, i) => (i === index ? { ...r, value: event.target.value } : r)))
                      }
                    />
                    <Button
                      size="icon-sm"
                      variant="outline"
                      aria-label={`Remove ${row.name || "variable"}`}
                      onClick={() => setStaged(rows.filter((_, i) => i !== index))}
                    >
                      <TrashIcon />
                    </Button>
                  </div>
                );
              })}
            </div>
          )}
          <div className="flex items-center justify-between">
            <Button size="sm" variant="outline" onClick={() => setStaged([...rows, { name: "", value: "" }])}>
              <PlusIcon data-icon-start-inline />
              Add variable
            </Button>
            <div className="flex items-center gap-2">
              {staged != null ? (
                <Button size="sm" variant="ghost" onClick={() => setStaged(null)} disabled={apply.isPending}>
                  Discard
                </Button>
              ) : null}
              <Button size="sm" disabled={!hasChanges || apply.isPending} onClick={() => void apply.mutate()}>
                {apply.isPending ? "Applying…" : "Apply & deploy"}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="text-sm">Effective environment</CardTitle>
          <span className="ml-auto text-2xs text-muted-foreground">
            read-only · frozen-spec synthesis (the Spec tab shows the full projection)
          </span>
        </CardHeader>
        <CardContent>
          {spec.data == null ? (
            <p className="py-4 text-center text-xs text-muted-foreground">
              No frozen spec yet — variables bake in at the first deploy.
            </p>
          ) : (
            <div className="flex flex-col gap-3">
              {(spec.data.processes ?? []).map((process) => (
                <div key={process.name ?? ""} className="rounded-lg border p-3">
                  <div className="flex items-baseline gap-2">
                    <span className="text-sm font-medium">{process.name ?? ""}</span>
                    <span className="text-2xs text-muted-foreground">
                      {Object.keys(process.env ?? {}).length} keys
                      {(process.secret_refs ?? []).length > 0
                        ? ` · ${(process.secret_refs ?? []).length} secret refs`
                        : ""}
                    </span>
                  </div>
                  <div className="mt-1 grid gap-x-4 gap-y-0.5 sm:grid-cols-2">
                    {Object.entries(process.env ?? {}).map(([key, value]) => (
                      <div key={key} className="flex min-w-0 items-baseline gap-2 font-mono text-2xs">
                        <span className="shrink-0 text-muted-foreground">{key}</span>
                        <span className="min-w-0 flex-1 truncate" title={value}>
                          {value}
                        </span>
                        <CopyButton value={value} />
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
