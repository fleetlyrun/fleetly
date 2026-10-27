import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useCommand } from "@lynx-go/uikit-core";
import type { CommandDescriptor } from "@lynx-go/uikit-core/kernel";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import { PlusIcon, SlidersIcon, TrashIcon } from "lucide-react";
import { toast } from "sonner";
import { CopyButton } from "@/components/domain/copy-button";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { describeApiError, runCommandMapped } from "@/kit/errors";
import { fetchFrozenSpec, useSpecDeploy, type AppSpec } from "@/lib/spec-deploy";

type VariableRow = { id?: string; name: string; value: string; updated_at?: string };

// Variables tab（ADR-0072 重做）：App 级变量是平台一等聚合——store 编辑器
// + Apply（写 store + 重发冻结 Spec：三级合成顶层在冻结咽喉取新值，故
// 变量轮换 = 改 store + 一次部署；git hook 应用同样适用——spec_file 按
// 运行时 overlay 受理，材料半边不动）。生效 env 是只读投影（冻结体合成
// 终态）；per-process 的源声明 env 与 secret_refs 属 Spec 高级面（Spec tab
// 编辑器 / Deploy 表单），不在此重复。
//
// 批 2 写路径命令化：Apply 动线（PUT 变更键 → DELETE 移除键 → 回读冻结
// 体 → 重发 Spec）是一条原子命令——模块级 descriptor 工厂闭包 appId 与
// 部署步（useSpecDeploy 句柄注入，lib 共享件不动）；未变键过滤在提交边
// （staged/base diff 是 UI 状态语义）。存量 store 键
// ["resources","app-variables",appId] 经 legacyKeys 桥（读面尚未 kit 化，
// 无 kit 键可失效）；结果两分经 runCommandMapped（400→rejected 保留
// apperr code、409→conflict、其余 throw）；toast 单源 onSettled。

export interface ApplyAppVariablesInput {
  /** 变更键全量行（空名与未变键已在提交边过滤） */
  puts: ReadonlyArray<{ name: string; value: string }>;
  /** 移除键名 */
  deletes: ReadonlyArray<string>;
}

// applyAppVariablesDescriptor：变量落库 + 重部署一条动线（deploy 是组件
// 侧 hook 句柄——闭包注入保持 useSpecDeploy 的失效/导航单源）。
export function applyAppVariablesDescriptor(
  appId: string,
  deploy: (spec: AppSpec) => Promise<unknown>,
): CommandDescriptor<ApplyAppVariablesInput, null> {
  return {
    run: async (input) =>
      runCommandMapped(async () => {
        for (const row of input.puts) {
          await apiSend("/v1/app-variables", "PUT", { app_id: appId, name: row.name, value: row.value });
        }
        for (const name of input.deletes) {
          await apiSend(`/v1/app-variables/${encodeURIComponent(appId)}/${encodeURIComponent(name)}`, "DELETE");
        }
        // 变量生效需一次部署（冻结咽喉合成）；无冻结体的 App 在首部署取新值。
        const frozen = await fetchFrozenSpec(appId);
        if (frozen) await deploy(frozen);
        return null;
      }),
    // 存量 store 键桥（旧 useMutation invalidate 等值）
    legacyKeys: () => [["resources", "app-variables", appId]],
  };
}

export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
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

  const apply = useCommand(applyAppVariablesDescriptor(appId, (frozen) => deploy.mutateAsync(frozen)), {
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setStaged(null);
        toast.success("Variables saved — a new deployment rolls the new values");
      } else if (result) {
        toast.error(result.message);
      } else if (failure) {
        toast.error(describeApiError(failure));
      }
    },
  });

  // 提交边 diff：未变键不重写、移除键 DELETE、空名行丢弃（原动线语义）。
  function applyStaged() {
    const baseline = new Map(base.map((row) => [row.name, row]));
    const puts = (staged ?? []).filter(
      (row) => row.name !== "" && baseline.get(row.name)?.value !== row.value,
    );
    const deletes = base
      .filter((row) => !(staged ?? []).some((kept) => kept.name === row.name))
      .map((row) => row.name);
    apply.mutate({ puts, deletes });
  }

  return (
    <div className="flex flex-col gap-5">
      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="flex items-center gap-1.5 text-sm">
            <SlidersIcon className="size-3.5 text-muted" />
            App variables
          </CardTitle>
          <span className="ml-auto text-2xs text-muted">
            top merge layer · app variable &gt; source env &gt; project shared · the next deploy bakes the values
          </span>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {store.isError ? (
            <p className="py-4 text-center text-xs text-muted">
              {store.error instanceof ApiError ? store.error.message : String(store.error)}
            </p>
          ) : rows.length === 0 ? (
            <p className="py-2 text-center text-xs text-muted">
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
                <Button size="sm" variant="ghost" onClick={() => setStaged(null)} disabled={apply.pending}>
                  Discard
                </Button>
              ) : null}
              <Button size="sm" disabled={!hasChanges || apply.pending} onClick={applyStaged}>
                {apply.pending ? "Applying…" : "Apply & deploy"}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center">
          <CardTitle className="text-sm">Effective environment</CardTitle>
          <span className="ml-auto text-2xs text-muted">
            read-only · frozen-spec synthesis (the Spec tab shows the full projection)
          </span>
        </CardHeader>
        <CardContent>
          {spec.data == null ? (
            <p className="py-4 text-center text-xs text-muted">
              No frozen spec yet — variables bake in at the first deploy.
            </p>
          ) : (
            <div className="flex flex-col gap-3">
              {(spec.data.processes ?? []).map((process) => (
                <div key={process.name ?? ""} className="rounded-lg border p-3">
                  <div className="flex items-baseline gap-2">
                    <span className="text-sm font-medium">{process.name ?? ""}</span>
                    <span className="text-2xs text-muted">
                      {Object.keys(process.env ?? {}).length} keys
                      {(process.secret_refs ?? []).length > 0
                        ? ` · ${(process.secret_refs ?? []).length} secret refs`
                        : ""}
                    </span>
                  </div>
                  <div className="mt-1 grid gap-x-4 gap-y-0.5 sm:grid-cols-2">
                    {Object.entries(process.env ?? {}).map(([key, value]) => (
                      <div key={key} className="flex min-w-0 items-baseline gap-2 font-mono text-2xs">
                        <span className="shrink-0 text-muted">{key}</span>
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
