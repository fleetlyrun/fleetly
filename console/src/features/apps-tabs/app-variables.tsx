import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import type { components } from "@/api/structure";
import { SlidersIcon, TrashIcon } from "lucide-react";
import { toast } from "sonner";
import { CopyButton } from "@/components/domain/copy-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type AppSpec = components["schemas"]["v1AppSpec"];

// per-process 暂存面（IA v3 §4.1 + 原型 screen-appdetail Variables 对齐）：
// env 与 secret refs 单平面表（VALUE 列内 secret · ref 名 + secret ref
// 徽标），多 process 用表内分组行承载；对话框暂存，Apply changes = 冻结 Spec 全量
// + 暂存改动组装 spec_file 走 Deploy（第四源，零 proto）。脏态表达 =
// Apply changes 由禁用变可用（原型口径，不另设 staged 描边）。
// ADR-0060 组织复裁（用户裁决 2026-10-11）：工具栏 process 筛选下拉 +
// 创建对话框内显式绑定 process——env 本就挂 process，创建即绑定；不做
// "应用到所有 process"的隐性扇出（同名变量后续单改会分叉，语义不诚实）。
interface ProcessEdits {
  env: Record<string, string>;
  secretRefs: string[];
}

export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [staged, setStaged] = useState<Record<string, ProcessEdits>>({});
  const [editTarget, setEditTarget] = useState<{ process: string; name: string } | null>(null);
  const [filter, setFilter] = useState("all");
  const [addOpen, setAddOpen] = useState(false);
  const [refOpen, setRefOpen] = useState(false);
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

  const frozen = spec.data;
  const processes = frozen?.processes ?? [];
  const frozenProcess = (name: string): ProcessEdits => {
    const process = processes.find((entry) => entry.name === name);
    return { env: { ...(process?.env ?? {}) }, secretRefs: [...(process?.secret_refs ?? [])] };
  };
  // hasStagedEdits：任一暂存面与其冻结基线有实际差异（同值编辑不算）——
  // Apply changes 的启用门（原型口径：按钮由禁用变可用即脏态表达）。
  const hasStagedEdits =
    frozen != null &&
    Object.entries(staged).some(([name, edits]) => {
      const base = frozenProcess(name);
      return JSON.stringify(edits.env) !== JSON.stringify(base.env) || JSON.stringify(edits.secretRefs) !== JSON.stringify(base.secretRefs);
    });
  // 筛选落点："all" = 全部 process；名字已不在冻结体（切了 spec）时诚实
  // 回落全部，避免永远筛到空集。
  const filterName = filter !== "all" && processes.some((process) => process.name === filter) ? filter : "";
  const visible = filterName === "" ? processes : processes.filter((process) => process.name === filterName);
  // addDefaultProcess 是创建对话框的默认绑定（当前筛选 > 第一个 process）。
  const addDefaultProcess = filterName !== "" ? filterName : (processes[0]?.name ?? "");

  const deploy = useMutation({
    mutationFn: async () => {
      if (frozen == null) throw new Error("no frozen spec");
      const editedProcesses = processes.map((process) => {
        const edits = staged[process.name ?? ""];
        if (edits == null) return process;
        return { ...process, env: edits.env, secret_refs: edits.secretRefs };
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
      toast("Variables staged as a new deployment");
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
    // W-3：未部署（404）= 空态面，不渲染查询内部错误。
    return (
      <div className="rounded-xl border bg-card p-6 text-center">
        <h3 className="text-sm font-semibold">No frozen spec yet</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          The app has no deployment — its spec freezes on the first deploy. Environment can be provided at deploy time.
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
          Runtime environment per process · secret sources managed in project{" "}
          <a href={`/p/${encodeURIComponent(projectId)}/variables`} className="text-info underline-offset-2 hover:underline">
            Variables
          </a>
        </p>
        <div className="flex items-center gap-1.5">
          <Label className="text-2xs text-muted-foreground">Process</Label>
          <Select value={filter} onValueChange={setFilter}>
            <SelectTrigger className="h-8 w-40 text-xs">
              <SelectValue placeholder="All processes" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All processes</SelectItem>
              {processes.map((process) => (
                <SelectItem key={process.name} value={process.name ?? ""} className="font-mono">
                  {process.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
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
        <Button size="sm" variant="outline" onClick={() => setAddOpen(true)}>
          + Add variable
        </Button>
        <Button size="sm" variant="outline" onClick={() => setRefOpen(true)}>
          + Add secret ref
        </Button>
        <Button size="sm" disabled={!hasStagedEdits || deploy.isPending} onClick={() => deploy.mutate()}>
          {deploy.isPending ? "Deploying…" : "Apply changes"}
        </Button>
      </div>

      {processes.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-muted-foreground">The frozen spec carries no processes.</p>
      ) : (
        <table className="w-full text-xs">
          <thead className="border-b bg-muted/40 text-left text-2xs font-semibold tracking-wide text-muted-foreground uppercase">
            <tr>
              <th className="px-4 py-2">Name</th>
              <th className="px-4 py-2">Value</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {visible.map((process) => {
              const name = process.name ?? "";
              const edits = staged[name] ?? frozenProcess(name);
              const envNames = Object.keys(edits.env).sort();
              const stagedHere = staged[name] != null;
              const showProcessRow = processes.length > 1;
              const rows: React.ReactNode[] = [];
              if (showProcessRow) {
                rows.push(
                  <tr key={`${name}-group`} className="border-b bg-muted/20">
                    <td colSpan={3} className="px-4 py-1.5 text-2xs font-semibold tracking-wide text-muted-foreground uppercase">
                      process {name} · {process.replicas ?? 1} replica{Number(process.replicas ?? 1) === 1 ? "" : "s"}
                      {stagedHere ? <span className="ml-2 text-info normal-case">staged edits</span> : null}
                    </td>
                  </tr>,
                );
              }
              if (envNames.length === 0 && edits.secretRefs.length === 0) {
                rows.push(
                  <tr key={`${name}-empty`} className="border-b last:border-b-0">
                    <td colSpan={3} className="px-4 py-3 text-center text-muted-foreground">
                      No variables.
                    </td>
                  </tr>,
                );
                return rows;
              }
              for (const envName of envNames) {
                rows.push(
                  <tr key={`${name}-${envName}`} className="border-b last:border-b-0">
                    <td className="px-4 py-2.5 font-mono font-semibold">{envName}</td>
                    <td className="px-4 py-2.5 font-mono text-muted-foreground">{edits.env[envName]}</td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right">
                      <IconAction label="Edit variable" onClick={() => setEditTarget({ process: name, name: envName })}>
                        <SlidersIcon className="size-3.5" />
                      </IconAction>
                      <IconAction
                        label="Remove variable"
                        danger
                        onClick={() => {
                          const next = { ...edits.env };
                          delete next[envName];
                          setStaged((prev) => ({ ...prev, [name]: { ...edits, env: next } }));
                        }}
                      >
                        <TrashIcon className="size-3.5" />
                      </IconAction>
                    </td>
                  </tr>,
                );
              }
              for (const ref of edits.secretRefs) {
                rows.push(
                  <tr key={`${name}-ref-${ref}`} className="border-b last:border-b-0">
                    <td className="px-4 py-2.5 font-mono font-semibold">{ref}</td>
                    <td className="px-4 py-2.5">
                      <span className="flex flex-wrap items-center gap-1.5 font-mono">
                        <span className="text-muted-foreground">secret ·</span>
                        <a
                          href={`/p/${encodeURIComponent(projectId)}/variables`}
                          className="text-info underline-offset-2 hover:underline"
                        >
                          {ref}
                        </a>
                        <span className="rounded-full border border-info/30 bg-info/10 px-1.5 py-0.5 text-2xs font-semibold text-info">
                          secret ref
                        </span>
                      </span>
                    </td>
                    <td className="whitespace-nowrap px-4 py-2.5 text-right">
                      <CopyButton value={ref} />
                      <IconAction
                        label="Remove secret ref"
                        danger
                        onClick={() =>
                          setStaged((prev) => ({
                            ...prev,
                            [name]: { ...edits, secretRefs: edits.secretRefs.filter((entry) => entry !== ref) },
                          }))
                        }
                      >
                        <TrashIcon className="size-3.5" />
                      </IconAction>
                    </td>
                  </tr>,
                );
              }
              return rows;
            })}
          </tbody>
        </table>
      )}

      {/* Add/Edit 变量对话框（keyed remount——重开不残留）；Add 形内选
          目标 process（默认 = 当前筛选，落住创建即绑定）。 */}
      <VariableDialog
        key={editTarget == null ? "closed" : `edit-${editTarget.process}-${editTarget.name}`}
        open={editTarget != null}
        processes={processes.map((process) => process.name ?? "")}
        processLocked={editTarget != null}
        initialProcess={editTarget?.process ?? ""}
        initialName={editTarget?.name ?? ""}
        initialValue={
          editTarget ? (staged[editTarget.process] ?? frozenProcess(editTarget.process)).env[editTarget.name] ?? "" : ""
        }
        onClose={() => setEditTarget(null)}
        onSubmit={(_process, key, value) => {
          if (editTarget == null) return;
          const base = staged[editTarget.process] ?? frozenProcess(editTarget.process);
          const next = { ...base.env, [key]: value };
          setStaged((prev) => ({ ...prev, [editTarget.process]: { ...base, env: next } }));
          setEditTarget(null);
        }}
      />
      <VariableDialog
        key={addOpen ? "add-open" : "add-closed"}
        open={addOpen}
        processes={processes.map((process) => process.name ?? "")}
        processLocked={false}
        initialProcess={addDefaultProcess}
        initialName=""
        initialValue=""
        onClose={() => setAddOpen(false)}
        onSubmit={(process, key, value) => {
          const base = staged[process] ?? frozenProcess(process);
          setStaged((prev) => ({ ...prev, [process]: { ...base, env: { ...base.env, [key]: value } } }));
          setAddOpen(false);
        }}
      />
      <RefDialog
        key={refOpen ? "ref-open" : "ref-closed"}
        open={refOpen}
        processes={processes.map((process) => process.name ?? "")}
        initialProcess={addDefaultProcess}
        refsOf={(process) => (staged[process] ?? frozenProcess(process)).secretRefs}
        onClose={() => setRefOpen(false)}
        onSubmit={(process, ref) => {
          const base = staged[process] ?? frozenProcess(process);
          setStaged((prev) => ({ ...prev, [process]: { ...base, secretRefs: [...base.secretRefs, ref] } }));
          setRefOpen(false);
        }}
      />
    </div>
  );
}

// IconAction 是行内图标动作钮（原型 rowactions 形态；danger 语义红）。
function IconAction({
  label,
  danger,
  onClick,
  children,
}: {
  label: string;
  danger?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      className={`ml-1.5 inline-flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-muted ${
        danger ? "hover:text-destructive" : "hover:text-foreground"
      }`}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

// VariableDialog 是 Add/Edit 共用面（initialName 空 = 新增；Add 形带
// process 选择，Edit 形 process 与名字锁定）：名字与值都走暂存面，服务端
// 叶子校验是最终防线（部署受理时执法字符集）。
function VariableDialog({
  open,
  processes,
  processLocked,
  initialProcess,
  initialName,
  initialValue,
  onClose,
  onSubmit,
}: {
  open: boolean;
  processes: string[];
  processLocked: boolean;
  initialProcess: string;
  initialName: string;
  initialValue: string;
  onClose: () => void;
  onSubmit: (process: string, name: string, value: string) => void;
}) {
  // 基线由挂载参数初始化——调用方以 key 变更驱动重挂（Add 重开不残留）。
  const [process, setProcess] = useState(initialProcess);
  const [name, setName] = useState(initialName);
  const [value, setValue] = useState(initialValue);
  const valid = name.trim() !== "" && process !== "";
  return (
    <Dialog open={open} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{initialName === "" ? "Add variable" : `Edit ${initialName}`}</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            if (!valid) return;
            onSubmit(process, name.trim(), value);
          }}
        >
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Process</span>
            {processLocked ? (
              <Input value={process} readOnly className="font-mono text-xs" />
            ) : (
              <Select value={process} onValueChange={setProcess}>
                <SelectTrigger className="w-full font-mono text-xs">
                  <SelectValue placeholder={processes.length === 0 ? "no processes" : ""} />
                </SelectTrigger>
                <SelectContent>
                  {processes.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            <span className="text-2xs font-normal text-muted-foreground">The variable lands on this process only.</span>
          </Label>
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Name</span>
            <Input value={name} onChange={(event) => setName(event.target.value)} readOnly={initialName !== ""} autoFocus={initialName === ""} />
          </Label>
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Value</span>
            <Input value={value} onChange={(event) => setValue(event.target.value)} autoFocus={initialName !== ""} />
          </Label>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!valid}>
              Stage
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// RefDialog 是 secret ref 新增面（重复 ref 拒绝；值面在 secret 本体；
// process 内选——默认当前筛选，落住创建即绑定。原实现无触发点是死 UI，
// ADR-0060 同批修活）。
function RefDialog({
  open,
  processes,
  initialProcess,
  refsOf,
  onClose,
  onSubmit,
}: {
  open: boolean;
  processes: string[];
  initialProcess: string;
  refsOf: (process: string) => string[];
  onClose: () => void;
  onSubmit: (process: string, ref: string) => void;
}) {
  const [process, setProcess] = useState(initialProcess);
  const [ref, setRef] = useState("");
  const valid = ref.trim() !== "" && process !== "" && !refsOf(process).includes(ref.trim());
  return (
    <Dialog open={open} onOpenChange={(next) => (next ? undefined : onClose())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add secret ref</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            if (!valid) return;
            onSubmit(process, ref.trim());
            setRef("");
          }}
        >
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Process</span>
            <Select value={process} onValueChange={setProcess}>
              <SelectTrigger className="w-full font-mono text-xs">
                <SelectValue placeholder={processes.length === 0 ? "no processes" : ""} />
              </SelectTrigger>
              <SelectContent>
                {processes.map((name) => (
                  <SelectItem key={name} value={name}>
                    {name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Label>
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Secret name</span>
            <Input value={ref} onChange={(event) => setRef(event.target.value)} autoFocus />
            <span className="text-2xs font-normal text-muted-foreground">
              Reference form (e.g. database:orders) — the value lives in the project secret, never in the spec. Lands on process{" "}
              {process || "…"}.
            </span>
          </Label>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={!valid}>
              Stage
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
