import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import type { components } from "@/api/structure";
import { SlidersIcon, TrashIcon } from "lucide-react";
import { toast } from "sonner";
import { CopyButton } from "@/components/domain/copy-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type AppSpec = components["schemas"]["v1AppSpec"];

// per-process 暂存面（IA v3 §4.1 + 原型 screen-appdetail Variables 对齐）：
// env 与 secret refs 单平面表（VALUE 列内 secret · ref 名 + secret ref
// 徽标），多 process 用表内分组行承载；对话框暂存，Apply changes = 冻结 Spec 全量
// + 暂存改动组装 spec_file 走 Deploy（第四源，零 proto）。脏态表达 =
// Apply changes 由禁用变可用（原型口径，不另设 staged 描边）。
interface ProcessEdits {
  env: Record<string, string>;
  secretRefs: string[];
}

export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const queryClient = useQueryClient();
  const [staged, setStaged] = useState<Record<string, ProcessEdits>>({});
  const [editTarget, setEditTarget] = useState<{ process: string; name: string } | null>(null);
  const [addTarget, setAddTarget] = useState<string | null>(null);
  const [refTarget, setRefTarget] = useState<string | null>(null);
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
      if (id) window.location.assign(`../deployments/${encodeURIComponent(id)}`);
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
        <h3 className="text-[13px] font-semibold">No frozen spec yet</h3>
        <p className="mt-1 text-xs text-muted-foreground">
          The app has no deployment — its spec freezes on the first deploy. Environment can be provided at deploy time.
        </p>
        <Button size="sm" className="mt-3" onClick={() => window.location.assign("?deploy=1")}>
          Deploy now
        </Button>
      </div>
    );
  }

  return (
    <div className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
        <p className="mr-auto text-xs text-muted-foreground">
          Runtime environment for all processes · secret sources managed in project{" "}
          <a href={`/p/${encodeURIComponent(projectId)}/configuration`} className="text-info underline-offset-2 hover:underline">
            Variables
          </a>
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
        <Button size="sm" variant="outline" onClick={() => setAddTarget(processes[0]?.name ?? "web")}>
          + Add variable
        </Button>
        <Button size="sm" disabled={!hasStagedEdits || deploy.isPending} onClick={() => deploy.mutate()}>
          {deploy.isPending ? "Deploying…" : "Apply changes"}
        </Button>
      </div>

      {processes.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-muted-foreground">The frozen spec carries no processes.</p>
      ) : (
        <table className="w-full text-xs">
          <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
            <tr>
              <th className="px-4 py-2">Name</th>
              <th className="px-4 py-2">Value</th>
              <th className="px-4 py-2" />
            </tr>
          </thead>
          <tbody>
            {processes.map((process) => {
              const name = process.name ?? "";
              const edits = staged[name] ?? frozenProcess(name);
              const envNames = Object.keys(edits.env).sort();
              const stagedHere = staged[name] != null;
              const showProcessRow = processes.length > 1;
              const rows: React.ReactNode[] = [];
              if (showProcessRow) {
                rows.push(
                  <tr key={`${name}-group`} className="border-b bg-muted/20">
                    <td colSpan={3} className="px-4 py-1.5 text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
                      process {name} ×{process.replicas ?? 1}
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
                          href={`/p/${encodeURIComponent(projectId)}/configuration`}
                          className="text-info underline-offset-2 hover:underline"
                        >
                          {ref}
                        </a>
                        <span className="rounded-full border border-info/30 bg-info/10 px-1.5 py-0.5 text-[10px] font-semibold text-info">
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

      {/* Add/Edit 变量对话框（keyed remount——重开不残留） */}
      <VariableDialog
        key={editTarget == null ? "closed" : `edit-${editTarget.process}-${editTarget.name}`}
        open={editTarget != null}
        initialName={editTarget?.name ?? ""}
        initialValue={
          editTarget ? (staged[editTarget.process] ?? frozenProcess(editTarget.process)).env[editTarget.name] ?? "" : ""
        }
        onClose={() => setEditTarget(null)}
        onSubmit={(key, value) => {
          if (editTarget == null) return;
          const base = staged[editTarget.process] ?? frozenProcess(editTarget.process);
          const next = { ...base.env, [key]: value };
          setStaged((prev) => ({ ...prev, [editTarget.process]: { ...base, env: next } }));
          setEditTarget(null);
        }}
      />
      <VariableDialog
        key={addTarget == null ? "add-closed" : `add-${addTarget}`}
        open={addTarget != null}
        initialName=""
        initialValue=""
        onClose={() => setAddTarget(null)}
        onSubmit={(key, value) => {
          if (addTarget == null) return;
          const base = staged[addTarget] ?? frozenProcess(addTarget);
          setStaged((prev) => ({ ...prev, [addTarget]: { ...base, env: { ...base.env, [key]: value } } }));
          setAddTarget(null);
        }}
      />
      <RefDialog
        key={refTarget == null ? "ref-closed" : "ref-open"}
        open={refTarget != null}
        processName={refTarget ?? processes[0]?.name ?? "web"}
        existing={refTarget != null ? (staged[refTarget] ?? frozenProcess(refTarget)).secretRefs : []}
        onClose={() => setRefTarget(null)}
        onSubmit={(ref) => {
          if (refTarget == null) return;
          const base = staged[refTarget] ?? frozenProcess(refTarget);
          setStaged((prev) => ({ ...prev, [refTarget]: { ...base, secretRefs: [...base.secretRefs, ref] } }));
          setRefTarget(null);
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

// VariableDialog 是 Add/Edit 共用面（initialName 空 = 新增）：名字与值都
// 走暂存面，服务端叶子校验是最终防线（部署受理时执法字符集）。
function VariableDialog({
  open,
  initialName,
  initialValue,
  onClose,
  onSubmit,
}: {
  open: boolean;
  initialName: string;
  initialValue: string;
  onClose: () => void;
  onSubmit: (name: string, value: string) => void;
}) {
  // 基线由挂载参数初始化——调用方以 key 变更驱动重挂（Add 重开不残留）。
  const [name, setName] = useState(initialName);
  const [value, setValue] = useState(initialValue);
  const valid = name.trim() !== "";
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
            onSubmit(name.trim(), value);
          }}
        >
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

// RefDialog 是 secret ref 新增面（重复 ref 拒绝；值面在 secret 本体）。
function RefDialog({
  open,
  processName,
  existing,
  onClose,
  onSubmit,
}: {
  open: boolean;
  processName: string;
  existing: string[];
  onClose: () => void;
  onSubmit: (ref: string) => void;
}) {
  const [ref, setRef] = useState("");
  const valid = ref.trim() !== "" && !existing.includes(ref.trim());
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
            onSubmit(ref.trim());
            setRef("");
          }}
        >
          <Label className="flex flex-col gap-1.5">
            <span className="text-xs font-semibold">Secret name</span>
            <Input value={ref} onChange={(event) => setRef(event.target.value)} autoFocus />
            <span className="text-[11px] font-normal text-muted-foreground">
              Reference form (e.g. database:orders) — the value lives in the project secret, never in the spec. Lands on process{" "}
              {processName}.
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
