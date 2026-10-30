import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useCommand, useCollectionData, useRecord } from "@lynx-go/uikit-core";
import type { CommandDescriptor, CommandResult } from "@lynx-go/uikit-core/kernel";
import { ApiError, apiSend } from "@/api/client";
import { appSpecSheetResource, appVariablesStoreResource } from "@/kit/app-observe";
import { PlusIcon, SlidersIcon, TrashIcon } from "lucide-react";
import { toast } from "sonner";
import { CopyButton } from "@/components/domain/copy-button";
import { ConfirmSkipDialog } from "@/components/domain/confirm-skip-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { runCommandMapped } from "@/kit/errors";
import { deploymentsResource, type DeployOutcome } from "@/kit/deployments";
import { specFileDeployState } from "@/lib/deployPayload";
import { fetchFrozenSpec, type AppSpec } from "@/lib/spec-deploy";

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
// 部署步（deploy 命令句柄注入）；未变键过滤在提交边（staged/base diff
// 是 UI 状态语义）。失效面 kit selector（app-variables 集合）+ 命令内
// deploy 步自带的部署两形态；结果两分经 runCommandMapped（400/428→
// rejected 保留 apperr code、409→conflict、其余 throw）。
//
// B1 W4 遗留直发面收口（2026-10-31）：内嵌 deploy 步原经 lib useSpecDeploy
// 裸 useMutation 直发（强制确认下恒收 428）——改走 deployments.deploy
// 命令（写必经 useCommand；skip 通道 = ConfirmSkipDialog 双击形态）。
// 双击语义：一击已把变量写入 store、仅部署被拒（REQUIRED）；二击全量
// 重放（PUT 幂等、DELETE 对已删键容忍 404 = 目标态已达成）带 skip=true
// 完成部署（proof=skipped 进审计）。预览化（PreviewDeploy 铸票复用
// DeploySheet 面板）留 B2a。

export interface ApplyAppVariablesInput {
  /** 变更键全量行（空名与未变键已在提交边过滤） */
  puts: ReadonlyArray<{ name: string; value: string }>;
  /** 移除键名 */
  deletes: ReadonlyArray<string>;
  /** 双击确认二击形态：deploy 步带 confirmation.skip=true（ConfirmSkipDialog） */
  skip?: boolean;
}

// applyAppVariablesDescriptor：变量落库 + 重部署一条动线（deploy 是组件
// 侧命令句柄——闭包注入保持失效/导航单源；返回结果两分供双击态分诊）。
// 结构：写步（PUT/DELETE/回读冻结体）经 runCommandMapped 保持既有两分
// 口径；部署步的结果两分**原样透传**（非 ok 变体不携带 payload，与 null
// 载荷并集结构相容——REJECTED 交 ConfirmSkipDialog 分诊双击态）。
export function applyAppVariablesDescriptor(
  projectId: string,
  appId: string,
  deploy: (spec: AppSpec, skip: boolean) => Promise<CommandResult<DeployOutcome>>,
): CommandDescriptor<ApplyAppVariablesInput, null> {
  // M2 同源：失效身份（store 读键 scope {projectId, appId}）从闭包派生。
  const scopeOf = () => ({ projectId, appId });
  return {
    run: async (input): Promise<CommandResult<null>> => {
      const writes = await runCommandMapped<AppSpec | null>(async () => {
        for (const row of input.puts) {
          await apiSend("/v1/app-variables", "PUT", { app_id: appId, name: row.name, value: row.value });
        }
        for (const name of input.deletes) {
          try {
            await apiSend(`/v1/app-variables/${encodeURIComponent(appId)}/${encodeURIComponent(name)}`, "DELETE");
          } catch (err) {
            // 双击二击的全量重放：键已被一击删除（目标态已达成）→ 404 容忍
            // 为幂等成功；其余错误照常出界。
            if (err instanceof ApiError && err.status === 404) continue;
            throw err;
          }
        }
        return await fetchFrozenSpec(appId);
      });
      if (writes.status !== "ok") return writes;
      // 变量生效需一次部署（冻结咽喉合成）；无冻结体的 App 在首部署取新值。
      const frozen = writes.payload;
      if (frozen == null) return { status: "ok", payload: null };
      const deployResult = await deploy(frozen, input.skip === true);
      if (deployResult.status !== "ok") return deployResult;
      return { status: "ok", payload: null };
    },
    // 失效面：kit selector（app-variables 集合）——存量 store 键桥零读者
    // 退役（F1 回流批审计）。
    scopeOf,
    invalidates: [{ resource: "app-variables" }],
  };
}

export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const navigate = useNavigate();

  // store/冻结 Spec 伴读（M1 useCollectionData/useRecord 直消费）：60s 拍子同旧。
  const store = useCollectionData(appVariablesStoreResource, {
    scope: { projectId, appId },
    window: { size: 200 },
    enabled: appId !== "",
  });
  const spec = useRecord(appSpecSheetResource, {
    id: "current",
    scope: { projectId, appId },
    query: { refetchInterval: 60_000, enabled: appId !== "" },
  });

  // 暂存面：行集 = store 基线 + 本地增删改；Apply 一次性落库 + 部署。
  const [staged, setStaged] = useState<VariableRow[] | null>(null);
  const rows = staged ?? store.rows;
  const base = store.rows;
  const hasChanges =
    staged != null &&
    (staged.length !== base.length ||
      staged.some((row, i) => row.name !== base[i]?.name || row.value !== base[i]?.value));

  // deploy 步 = deployments.deploy 命令（spec_file 形态经 lib/deployPayload
  // 的表单态工厂）：受理成功落部署详情的导航锚（apply 命令内嵌调用）。
  const deploy = useCommand(deploymentsResource.commands.deploy, {
    scope: { projectId },
    onSettled: (result) => {
      if (result?.status !== "ok") return;
      const id = result.payload.deployment?.id;
      if (id) {
        void navigate({
          to: "/p/$projectId/apps/$appId/deployments/$deploymentId",
          params: { projectId, appId, deploymentId: id },
        });
      }
    },
  });

  // 提交边 diff：未变键不重写、移除键 DELETE、空名行丢弃（原动线语义）。
  const stagedDiff = () => {
    const baseline = new Map(base.map((row) => [row.name, row]));
    const puts = (staged ?? []).filter(
      (row) => row.name !== "" && baseline.get(row.name)?.value !== row.value,
    );
    const deletes = base
      .filter((row) => !(staged ?? []).some((kept) => kept.name === row.name))
      .map((row) => row.name);
    return { puts, deletes };
  };

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
          {store.status === "error" ? (
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
                <Button size="sm" variant="ghost" onClick={() => setStaged(null)}>
                  Discard
                </Button>
              ) : null}
              {/* B1 W4：Apply & deploy 接 ConfirmSkipDialog 双击 skip 形态
                  （首击写 store + 无票部署被拒 → 「已请求确认」态 → 二击
                  skip 完成部署；写步幂等可重放）。 */}
              <ConfirmSkipDialog
                label="Apply & deploy"
                disabled={!hasChanges}
                title="Apply variables and deploy?"
                description={
                  "Writes the staged variables to the app store, then re-deploys the frozen spec — variable values only take effect when a deployment freezes them (top merge layer: app variable > source env > project shared)."
                }
                confirmLabel="Apply & deploy"
                confirmAgainLabel="Confirm apply"
                pendingLabel="Applying…"
                askedSubject="Deploying these variable changes"
                askedNote="the values are saved already and bake in at the next deploy, which this path submits without a preview"
                descriptor={applyAppVariablesDescriptor(projectId, appId, (frozen, skip) =>
                  deploy.mutateAsync({ ...specFileDeployState(appId, JSON.stringify(frozen)), skip: skip || undefined }),
                )}
                buildInput={(skip) => ({ ...stagedDiff(), skip: skip || undefined })}
                onOk={() => {
                  setStaged(null);
                  toast.success("Variables saved — a new deployment rolls the new values");
                }}
              />
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
          {(() => {
            // kit 侧 spec 保持 unknown（资源面不带业务形状）——AppSpec 断言
            // 归消费方（lib/spec-deploy 是 AppSpec 词汇单源）。
            const frozen = spec.data?.spec as AppSpec | null;
            if (frozen == null) {
              return (
                <p className="py-4 text-center text-xs text-muted">
                  No frozen spec yet — variables bake in at the first deploy.
                </p>
              );
            }
            return (
              <div className="flex flex-col gap-3">
                {(frozen.processes ?? []).map((process) => (
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
            );
          })()}
        </CardContent>
      </Card>
    </div>
  );
}
