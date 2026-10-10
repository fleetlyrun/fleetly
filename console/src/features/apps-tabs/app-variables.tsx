import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, apiFetch, apiSend } from "@/api/client";
import type { components } from "@/api/structure";
import { Link, useNavigate } from "@tanstack/react-router";
import { FileJsonIcon, PlusIcon } from "lucide-react";
import { toast } from "sonner";
import { EmptyState } from "@/components/domain/empty-state";
import { CopyButton } from "@/components/domain/copy-button";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type AppSpec = components["schemas"]["v1AppSpec"];
type ProcessSpec = NonNullable<AppSpec["processes"]>[number];

// per-process 暂存面（IA v3 §4.1 Variables = 高频编辑面）：env 与
// secret_refs 的对话框暂存——Apply = 冻结 Spec 全量 + 暂存改动组装
// spec_file 走 Deploy（第四源自 F3.5 起即全部 AppSpec 字段的 API 写面，
// 零新契约）。dirty 以"与冻结 Spec 有差异"判定（同值编辑不算脏）。
interface ProcessEdits {
  env: Record<string, string>;
  secretRefs: string[];
}

// App Variables tab（IA v3 二期②点亮 + ⑤b 暂存式编辑流）：冻结 Spec 读面
// + per-process env / secret_refs 表格化暂存编辑（Add/Edit/Remove 走对话
// 框，Apply changes 工具栏右侧、脏态才启用，应用 = 新部署滚动替换）。
// secret 值不进 Spec（引用即锚），Reveal once 语义在 Project Configuration。
export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [staged, setStaged] = useState<Record<string, ProcessEdits>>({});
  const spec = useQuery({
    queryKey: ["resources", "app-spec", projectId, appId],
    queryFn: async (): Promise<AppSpec | undefined> => {
      try {
        const res = await apiFetch<{ spec?: AppSpec }>(`/v1/apps/${encodeURIComponent(appId)}/spec`);
        return res?.spec;
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return undefined;
        throw error;
      }
    },
    refetchInterval: 60_000,
  });

  const frozen = spec.data;
  const frozenProcess = (name: string): ProcessEdits => {
    const process = frozen?.processes?.find((entry) => entry.name === name);
    return { env: { ...(process?.env ?? {}) }, secretRefs: [...(process?.secret_refs ?? [])] };
  };
  // dirty：任一暂存面与其冻结基线有实际差异（同值编辑不启 Apply）。
  const dirty =
    frozen != null &&
    Object.entries(staged).some(([name, edits]) => {
      const base = frozenProcess(name);
      return JSON.stringify(edits.env) !== JSON.stringify(base.env) || JSON.stringify(edits.secretRefs) !== JSON.stringify(base.secretRefs);
    });

  const apply = useMutation({
    mutationFn: async () => {
      if (frozen == null) throw new Error("no frozen spec");
      const processes = (frozen.processes ?? []).map((process): ProcessSpec => {
        const edits = staged[process.name ?? ""];
        if (edits == null) return process;
        return {
          ...process,
          env: edits.env,
          secret_refs: edits.secretRefs,
        };
      });
      const edited: AppSpec = { ...frozen, processes };
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
      <EmptyState
        icon={FileJsonIcon}
        title="No frozen spec yet"
        description="The app has no deployment — its spec freezes on the first deploy. Environment can be provided at deploy time."
        actionLabel="Deploy now"
        onAction={() => window.location.assign(`/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}?deploy=1`)}
      />
    );
  }
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-end gap-2">
        <span className="mr-auto text-[11.5px] text-muted-foreground">
          Edits stage locally — applying creates a new deployment (rolling replace).
        </span>
        {dirty ? (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setStaged({});
              toast("Staged edits discarded");
            }}
          >
            Discard
          </Button>
        ) : null}
        <Button size="sm" disabled={!dirty || apply.isPending} onClick={() => apply.mutate()}>
          {apply.isPending ? "Deploying…" : "Apply changes"}
        </Button>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {(frozen.processes ?? []).map((process) => (
          <ProcessVariablesCard
            key={process.name}
            projectId={projectId}
            process={process}
            edits={staged[process.name ?? ""] ?? frozenProcess(process.name ?? "")}
            staged={staged[process.name ?? ""] != null}
            onStage={(edits) => setStaged((prev) => ({ ...prev, [process.name ?? ""]: edits }))}
          />
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <SourceCard spec={frozen} />
        <BuildCard spec={frozen} />
      </div>

      <p className="text-[11.5px] text-muted-foreground">
        Values of secrets never enter the spec; only named references. Applying re-deploys the frozen spec with your staged
        variables — source and build stay untouched.
      </p>
    </div>
  );
}

function ProcessVariablesCard({
  projectId,
  process,
  edits,
  staged,
  onStage,
}: {
  projectId: string;
  process: ProcessSpec;
  edits: ProcessEdits;
  staged: boolean;
  onStage: (edits: ProcessEdits) => void;
}) {
  const [editName, setEditName] = useState<string | null>(null);
  const [addRefOpen, setAddRefOpen] = useState(false);
  const name = process.name ?? "";
  return (
    <section className={`rounded-xl border bg-card p-4 ${staged ? "border-info/40" : ""}`}>
      <div className="mb-3 flex items-baseline justify-between">
        <h3 className="text-[13px] font-semibold">
          process <span className="font-mono">{name}</span>
          {staged ? <span className="ml-2 rounded-full border border-info/30 bg-info/10 px-2 py-0.5 text-[10.5px] font-semibold text-info">staged</span> : null}
        </h3>
        <div className="flex items-center gap-2">
          <span className="text-[11.5px] text-muted-foreground">×{process.replicas ?? 1}</span>
          <Button size="sm" variant="outline" onClick={() => setEditName("")}>
            <PlusIcon data-icon-start-inline />
            Add variable
          </Button>
        </div>
      </div>
      <EnvTable
        env={edits.env}
        onEdit={(key) => setEditName(key)}
        onRemove={(key) => {
          const next = { ...edits.env };
          delete next[key];
          onStage({ ...edits, env: next });
        }}
      />
      <div className="mt-3 flex flex-col gap-1.5 text-xs">
        <div className="flex items-center justify-between">
          <span className="text-muted-foreground">secret refs</span>
          <Button size="sm" variant="ghost" onClick={() => setAddRefOpen(true)}>
            <PlusIcon data-icon-start-inline />
            Add ref
          </Button>
        </div>
        {edits.secretRefs.length === 0 ? (
          <span className="text-muted-foreground">—</span>
        ) : (
          <span className="flex flex-wrap gap-1.5">
            {edits.secretRefs.map((ref) => (
              <span key={ref} className="flex items-center gap-1 rounded-full border border-info/30 bg-info/10 px-2 py-0.5 font-mono text-[11px] text-info">
                <Link to="/p/$projectId/configuration" params={{ projectId }}>
                  {ref}
                </Link>
                <button
                  type="button"
                  aria-label={`Remove ${ref}`}
                  className="text-info/70 hover:text-destructive"
                  onClick={() => onStage({ ...edits, secretRefs: edits.secretRefs.filter((entry) => entry !== ref) })}
                >
                  ×
                </button>
              </span>
            ))}
          </span>
        )}
      </div>

      <VariableDialog
        key={editName == null ? "closed" : `var-${editName}`}
        open={editName != null}
        initialName={editName ?? ""}
        initialValue={editName ? (edits.env[editName] ?? "") : ""}
        onClose={() => setEditName(null)}
        onSubmit={(key, value) => {
          onStage({ ...edits, env: { ...edits.env, [key]: value } });
          setEditName(null);
        }}
      />
      <RefDialog
        key={addRefOpen ? "ref-open" : "ref-closed"}
        open={addRefOpen}
        existing={edits.secretRefs}
        onClose={() => setAddRefOpen(false)}
        onSubmit={(ref) => {
          onStage({ ...edits, secretRefs: [...edits.secretRefs, ref] });
          setAddRefOpen(false);
        }}
      />
    </section>
  );
}

function EnvTable({ env, onEdit, onRemove }: { env: Record<string, string>; onEdit: (name: string) => void; onRemove: (name: string) => void }) {
  const names = Object.keys(env).sort();
  return (
    <div className="overflow-hidden rounded-lg border">
      <table className="w-full text-xs">
        <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
          <tr>
            <th className="px-2.5 py-1.5">Variable</th>
            <th className="px-2.5 py-1.5">Value</th>
            <th className="px-2.5 py-1.5" />
          </tr>
        </thead>
        <tbody>
          {names.length === 0 ? (
            <tr>
              <td colSpan={3} className="px-2.5 py-3 text-center text-muted-foreground">
                No plain env vars.
              </td>
            </tr>
          ) : (
            names.map((name) => (
              <tr key={name} className="border-b last:border-b-0">
                <td className="px-2.5 py-1.5 font-mono font-semibold">{name}</td>
                <td className="px-2.5 py-1.5 font-mono text-muted-foreground">{env[name]}</td>
                <td className="px-2.5 py-1.5 text-right whitespace-nowrap">
                  <CopyButton value={env[name]} />
                  <RowAction label="Edit" onClick={() => onEdit(name)} />
                  <RowAction label="Remove" danger onClick={() => onRemove(name)} />
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

function RowAction({ label, danger, onClick }: { label: string; danger?: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      className={`ml-1.5 text-[11px] underline-offset-2 hover:underline ${danger ? "text-destructive" : "text-info"}`}
      onClick={onClick}
    >
      {label}
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
          <Field label="Name">
            <Input value={name} onChange={(event) => setName(event.target.value)} readOnly={initialName !== ""} autoFocus={initialName === ""} />
          </Field>
          <Field label="Value">
            <Input value={value} onChange={(event) => setValue(event.target.value)} autoFocus={initialName !== ""} />
          </Field>
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

function RefDialog({
  open,
  existing,
  onClose,
  onSubmit,
}: {
  open: boolean;
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
          <Field label="Secret name" hint="reference form — e.g. database:main or a plain project secret name; values live in the secret itself">
            <Input value={ref} onChange={(event) => setRef(event.target.value)} autoFocus />
          </Field>
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

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <Label className="flex flex-col gap-1.5">
      <span className="text-xs font-semibold">{label}</span>
      {children}
      {hint ? <span className="text-[11px] font-normal text-muted-foreground">{hint}</span> : null}
    </Label>
  );
}

function SourceCard({ spec }: { spec: AppSpec }) {
  const source = spec.source;
  const kind = source?.git ? "git" : source?.image ? "image" : source?.upload ? "upload" : "—";
  const detail = source?.git ? `${source.git.repo} @ ${source.git.ref ?? "-"}` : source?.image?.ref ?? (source?.upload?.id ? `upload ${source.upload.id}` : "—");
  return (
    <section className="rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-[13px] font-semibold">Source</h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
        <dt className="text-muted-foreground">kind</dt>
        <dd className="font-mono">{kind}</dd>
        <dt className="text-muted-foreground">origin</dt>
        <dd className="break-all font-mono">{detail}</dd>
      </dl>
    </section>
  );
}

function BuildCard({ spec }: { spec: AppSpec }) {
  const build = spec.build;
  return (
    <section className="rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-[13px] font-semibold">Build</h3>
      {build == null ? (
        <p className="text-xs text-muted-foreground">No build — image-deployed app.</p>
      ) : (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">builder</dt>
          <dd className="font-mono">{build.builder ?? "—"}</dd>
          <dt className="text-muted-foreground">dockerfile</dt>
          <dd className="font-mono">{build.dockerfile || "—"}</dd>
          <dt className="text-muted-foreground">cache from</dt>
          <dd className="font-mono">{build.cache_from?.length ? build.cache_from.join(", ") : "—"}</dd>
        </dl>
      )}
    </section>
  );
}
