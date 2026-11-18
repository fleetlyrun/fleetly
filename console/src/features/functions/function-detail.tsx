import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useCommand, useCollection } from "@lynxland/uikit-core";
import type { ApiError, CommandResult } from "@lynxland/uikit-core/kernel";
import { CopyButton } from "@/components/domain/copy-button";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { DetailHero, DetailHeroIcon } from "@/components/domain/detail-hero";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge } from "@/components/domain/status-badge";
import { describeApiError } from "@/kit/errors";
import {
  functionRevisionsResource,
  functionRevisionsLookup,
  functionsLookup,
  functionsResource,
  type FunctionRow,
} from "@/kit/functions";
import { functionStatusTone } from "./panels";
import { DeleteFunctionButton } from "./panels";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Zap } from "lucide-react";

// Function 详情（ADR-0102 任务⑥；设计 v3 §11）：hero（状态/exposure 徽章 +
// 动作族）+ URL 段（private 恒在/public 仅暴露时——fn 名永久三律的可见面）
// + 容量窗 + Revisions 表（保留窗，ADR-0101）。动作族：Deploy（全量替换
// 对话框）/Scale（步进器对话框）/Exposure 翻转（确认框披露可达域变化——
// 本页最有价值的安全文案）/Rollback（缺省回上一枚）/Delete（panels 共件）。

function commandErrorText(result: CommandResult<unknown> | null, failure: ApiError | null): string | null {
  if (result != null && result.status !== "ok") {
    return result.status === "rejected" && result.code ? `[${result.code}] ${result.message}` : result.message;
  }
  if (failure != null) return describeApiError(failure);
  return null;
}

function ActionError({ result, failure }: { result: CommandResult<unknown> | null; failure: ApiError | null }) {
  const text = commandErrorText(result, failure);
  if (text == null) return null;
  return <div className="text-2xs text-[var(--status-danger)]">{text}</div>;
}

// DeployDialog：全量替换（源+形状半边——容量/接入不动，三平面分立的表单
// 可见面：无 replicas/exposure 字段）。
function DeployDialog({
  projectId,
  fn,
  open,
  onOpenChange,
}: {
  projectId: string;
  fn: FunctionRow;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [form, setForm] = useState({ image: "", port: "8080", command: "", env: "", secretRefs: "" });
  const deploy = useCommand(functionsResource.commands.deploy, {
    scope: { projectId },
    onSettled: (result) => {
      if (result?.status === "ok") onOpenChange(false);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Deploy {fn.name}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="dep-image">Image (full replace)</Label>
            <Input
              id="dep-image"
              placeholder="ghcr.io/acme/resize:2"
              value={form.image}
              onChange={(event) => setForm({ ...form, image: event.target.value })}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="dep-port">Port</Label>
              <Input
                id="dep-port"
                inputMode="numeric"
                value={form.port}
                onChange={(event) => setForm({ ...form, port: event.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="dep-command">Command (comma-separated)</Label>
              <Input
                id="dep-command"
                value={form.command}
                onChange={(event) => setForm({ ...form, command: event.target.value })}
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="dep-env">Env (KEY=VALUE per line — full replace)</Label>
            <Input
              id="dep-env"
              value={form.env}
              onChange={(event) => setForm({ ...form, env: event.target.value })}
            />
          </div>
          <ActionError result={deploy.result ?? null} failure={deploy.failure ?? null} />
        </div>
        <DialogFooter>
          <Button
            disabled={!form.image || deploy.pending}
            onClick={() =>
              void deploy.mutate({ id: fn.id ?? "", image: form.image, port: form.port, command: form.command, env: form.env })
            }
          >
            Deploy
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ScaleDialog：容量平面步进器（[min,max] 窗提示——出界拒的受理位文案面）。
function ScaleDialog({
  projectId,
  fn,
  open,
  onOpenChange,
}: {
  projectId: string;
  fn: FunctionRow;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [replicas, setReplicas] = useState(String(fn.replicas ?? 1));
  const scale = useCommand(functionsResource.commands.scale, {
    scope: { projectId },
    onSettled: (result) => {
      if (result?.status === "ok") onOpenChange(false);
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Scale {fn.name}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="scale-n">Replicas (window {fn.min_replicas}..{fn.max_replicas})</Label>
            <Input id="scale-n" inputMode="numeric" value={replicas} onChange={(event) => setReplicas(event.target.value)} />
          </div>
          <ActionError result={scale.result ?? null} failure={scale.failure ?? null} />
        </div>
        <DialogFooter>
          <Button disabled={scale.pending} onClick={() => void scale.mutate({ id: fn.id ?? "", replicas })}>
            Scale
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function FunctionDetailPage({ projectId, fnId, routeId }: { projectId: string; fnId: string; routeId: string }) {
  const functions = useCollection(functionsResource, {
    from: routeId,
    search: functionsLookup,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const fn = functions.rows.find((row) => row.id === fnId);
  const revisions = useCollection(functionRevisionsResource, {
    from: routeId,
    search: functionRevisionsLookup,
    scope: { projectId, functionId: fnId },
    query: { refetchInterval: 15_000 },
  });

  const [deployOpen, setDeployOpen] = useState(false);
  const [scaleOpen, setScaleOpen] = useState(false);
  const [exposureOpen, setExposureOpen] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);

  const flipTarget = fn?.exposure === "public" ? "private" : "public";
  const exposure = useCommand(functionsResource.commands.exposure, { scope: { projectId } });
  const rollback = useCommand(functionsResource.commands.rollback, { scope: { projectId } });

  if (!fn) {
    return (
      <PageFrame title="Function">
        {functions.error != null ? (
          <ErrorState error={new Error(describeApiError(functions.error))} onRetry={functions.refetch} />
        ) : (
          <p className="text-sm text-muted">
            Function not found — it may have been deleted.{" "}
            <Link className="underline" to="/p/$projectId/functions" params={{ projectId }}>
              Back to functions
            </Link>
          </p>
        )}
      </PageFrame>
    );
  }

  return (
    <PageFrame title={fn.name ?? fn.id ?? ""}>
      <DetailHero
        icon={<DetailHeroIcon icon={Zap} />}
        title={fn.name ?? fn.id ?? ""}
        badges={
          <>
            <StatusBadge tone={functionStatusTone(fn.status)} pulse={fn.status === "pending"}>
              {fn.status ?? "pending"}
            </StatusBadge>
            <StatusBadge tone={fn.exposure === "public" ? "info" : "neutral"}>
              {fn.exposure === "public" ? "public" : "private"}
            </StatusBadge>
            <StatusBadge tone="neutral">r{fn.current_revision_seq}</StatusBadge>
          </>
        }
        meta={
          <span className="flex items-center gap-2 font-mono text-2xs text-muted">
            {fn.id}
            <CopyButton value={fn.id ?? ""} label="Copy id" />
          </span>
        }
        actions={
          <>
            <Button size="sm" onClick={() => setDeployOpen(true)}>
              Deploy…
            </Button>
            <Button size="sm" variant="outline" onClick={() => setScaleOpen(true)}>
              Scale…
            </Button>
            <Button size="sm" variant="outline" onClick={() => setExposureOpen(true)}>
              Go {flipTarget}…
            </Button>
            <Button size="sm" variant="outline" onClick={() => setRollbackOpen(true)}>
              Rollback…
            </Button>
            <DeleteFunctionButton projectId={projectId} fn={fn} />
          </>
        }
      />

      <div className="grid gap-4">
        {/* URL 段：fn 名永久三律的可见面（private 恒在；public 仅暴露时）。 */}
        <section className="rounded-xl border border-line bg-background p-4">
          <h2 className="mb-3 text-sm font-medium">Endpoints</h2>
          <dl className="grid gap-2">
            <div className="flex items-center justify-between gap-3">
              <dt className="text-xs text-muted">In-network (project members only)</dt>
              <dd className="flex items-center gap-2 font-mono text-xs">
                {fn.private_url}
                <CopyButton value={fn.private_url ?? ""} label="Copy URL" />
              </dd>
            </div>
            <div className="flex items-center justify-between gap-3">
              <dt className="text-xs text-muted">Public</dt>
              <dd className="flex items-center gap-2 font-mono text-xs">
                {fn.public_url ? (
                  <>
                    {fn.public_url}
                    <CopyButton value={fn.public_url} label="Copy URL" />
                  </>
                ) : (
                  <span className="text-muted">not exposed</span>
                )}
              </dd>
            </div>
          </dl>
        </section>

        <section className="rounded-xl border border-line bg-background p-4">
          <h2 className="mb-3 text-sm font-medium">Capacity</h2>
          <p className="font-mono text-xs text-muted">
            {fn.replicas} replicas (window {fn.min_replicas}..{fn.max_replicas}) — scaling is a row write,
            zero redeploy.
          </p>
        </section>

        <section className="rounded-xl border border-line bg-background p-4">
          <h2 className="mb-3 text-sm font-medium">Revisions (retention window)</h2>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Seq</TableHead>
                <TableHead>Digest</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right">Current</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {[...revisions.rows]
                .sort((a, b) => Number(b.seq ?? 0) - Number(a.seq ?? 0))
                .map((rev) => (
                  <TableRow key={rev.seq}>
                    <TableCell className="font-mono text-xs">r{rev.seq}</TableCell>
                    <TableCell className="font-mono text-xs text-muted">{rev.digest}</TableCell>
                    <TableCell>
                      <RelativeTime value={rev.created_at} />
                    </TableCell>
                    <TableCell className="text-right">
                      {rev.seq === fn.current_revision_seq ? (
                        <StatusBadge tone="success">current</StatusBadge>
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
            </TableBody>
          </Table>
        </section>
      </div>

      <DeployDialog projectId={projectId} fn={fn} open={deployOpen} onOpenChange={setDeployOpen} />
      <ScaleDialog projectId={projectId} fn={fn} open={scaleOpen} onOpenChange={setScaleOpen} />

      {/* Exposure 翻转确认框：披露可达域变化——本页最有价值的安全文案
          （设计 v3 §11；三律②零载体动作）。 */}
      <ConfirmDialog
        open={exposureOpen}
        onOpenChange={setExposureOpen}
        title={`Make ${fn.name} ${flipTarget}?`}
        description={
          flipTarget === "public"
            ? "The function becomes reachable from the public internet at its managed route. Anyone who can guess the host can call it — authentication belongs to the function itself."
            : "The public route is withdrawn immediately. The function stays reachable for project network members at its permanent in-network URL."
        }
        confirmLabel={flipTarget === "public" ? "Make public" : "Make private"}
        risk={{
          tier: "wide",
          consequences:
            flipTarget === "public" ? (
              <ul className="list-disc pl-4 text-xs">
                <li>The managed route serves this function from the public internet.</li>
                <li>Anyone who resolves the host can call it — authentication belongs to the function itself.</li>
              </ul>
            ) : (
              <ul className="list-disc pl-4 text-xs">
                <li>The public route is withdrawn immediately (managed route face republishes).</li>
                <li>Project network members keep the permanent in-network URL.</li>
              </ul>
            ),
        }}
        onConfirm={() => exposure.mutateAsync({ id: fn.id ?? "", exposure: flipTarget })}
      />
      <ActionError result={exposure.result ?? null} failure={exposure.failure ?? null} />

      <ConfirmDialog
        open={rollbackOpen}
        onOpenChange={setRollbackOpen}
        title={`Roll back ${fn.name}?`}
        description={`Re-track to the previous frozen revision (currently r${fn.current_revision_seq}). The frozen row is replayed — no new revision is created; capacity and exposure stay untouched.`}
        confirmLabel="Roll back"
        risk={{ tier: "standard" }}
        onConfirm={() => rollback.mutateAsync({ id: fn.id ?? "" })}
      />
      <ActionError result={rollback.result ?? null} failure={rollback.failure ?? null} />
    </PageFrame>
  );
}
