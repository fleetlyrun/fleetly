// 部署页：部署动作（粘贴/上传 compose → POST Deploy → 跟踪到终态）、
// 部署历史（状态徽章含 blocked_waiting/observing 等中间态；失败行展示
// code + verdict + recovery 同信封形态）、回滚（选 revision → Rollback）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { useParams } from "react-router-dom";
import { CheckCircle2, FileUp, Loader2, Undo2 } from "lucide-react";

import {
  deploy,
  getDeployment,
  listDeployments,
  listRevisions,
  rollbackDeployment,
  cancelDeployment,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { ComposeWarning, DeploymentView } from "@/api/types";
import { DeploymentFailureAlert, EnvelopeAlert } from "@/components/envelope-alert";
import { StateBadge } from "@/components/state-badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { formatTime, timeAgo } from "@/lib/utils";

/** 终态集：之外的状态轮询跟踪。 */
const TERMINAL = new Set(["succeeded", "failed", "cancelled"]);

function DeployCard({ app }: { app: string }) {
  const queryClient = useQueryClient();
  const [composeText, setComposeText] = useState("");
  const [trackedId, setTrackedId] = useState("");
  // ComposeWarning 形状跟随生成类型（D4-②：手写 {field,warning} 与 proto
  // {kind,code,service,message} 漂移，已修）。
  const [warnings, setWarnings] = useState<ComposeWarning[]>([]);
  const fileRef = useRef<HTMLInputElement>(null);

  const deployMutation = useMutation({
    mutationFn: () => deploy(app, composeText),
    onSuccess: (resp) => {
      setTrackedId(resp.deployment_id ?? "");
      setWarnings(resp.warnings ?? []);
      setComposeText("");
      void queryClient.invalidateQueries({ queryKey: ["deployments", app] });
      void queryClient.invalidateQueries({ queryKey: ["apps"] });
      void queryClient.invalidateQueries({ queryKey: ["app", app] });
    },
  });

  // 部署跟踪：入队后轮询该部署直至终态（与 CLI wait 同语义，2s 周期）。
  const tracked = useQuery({
    queryKey: ["deployment", trackedId],
    queryFn: () => getDeployment(trackedId),
    enabled: trackedId !== "",
    refetchInterval: (q) =>
      q.state.data && TERMINAL.has(q.state.data.deployment.status ?? "")
        ? false
        : 2000,
  });
  const trackedDeployment = tracked.data?.deployment;
  const trackedError =
    deployMutation.isError ? errorEnvelopeFrom(deployMutation.error) : null;

  function onFile(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    void file.text().then(setComposeText);
    e.target.value = "";
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!composeText.trim()) return;
    deployMutation.mutate();
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium text-muted-foreground">
          Deploy compose
        </CardTitle>
        <CardDescription>
          Paste compose YAML or upload the file. The app is created on first
          deploy; changes roll out zero-downtime.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <form className="space-y-3" onSubmit={onSubmit}>
          <Textarea
            aria-label="Compose YAML"
            className="min-h-[180px] font-mono text-xs"
            placeholder={"services:\n  web:\n    image: nginx:1.27-alpine\n    ports:\n      - 8080:80"}
            value={composeText}
            onChange={(e) => setComposeText(e.target.value)}
          />
          <div className="flex items-center gap-2">
            <Button type="submit" disabled={!composeText.trim() || deployMutation.isPending}>
              {deployMutation.isPending ? (
                <Loader2 aria-hidden className="h-4 w-4 animate-spin" />
              ) : null}
              Deploy
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => fileRef.current?.click()}
            >
              <FileUp aria-hidden className="h-4 w-4" />
              Upload file
            </Button>
            <input
              ref={fileRef}
              type="file"
              accept=".yaml,.yml,.json,application/yaml,text/yaml"
              className="hidden"
              onChange={onFile}
            />
          </div>
        </form>

        {trackedError ? (
          <EnvelopeAlert
            code={trackedError.code}
            message={trackedError.message}
            suggestion={trackedError.suggestion}
            docs={trackedError.docs}
          />
        ) : null}

        {warnings.length > 0 ? (
          <div className="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900">
            <div className="font-semibold">Compose warnings</div>
            <ul className="mt-1 list-disc pl-4">
              {warnings.map((w, i) => (
                <li key={i}>
                  <code className="text-xs">{w.code || w.kind}</code>
                  {w.service ? ` (${w.service})` : ""}: {w.message}
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        {trackedId ? (
          <div className="rounded-md border p-3 text-sm" data-testid="deployment-tracker">
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground">Deployment</span>
              <code className="font-mono text-xs">{trackedId}</code>
              {trackedDeployment ? (
                <>
                  <StateBadge state={
                    trackedDeployment.phase === "blocked_waiting"
                      ? "blocked_waiting"
                      : trackedDeployment.status ?? ""
                  } />
                  {trackedDeployment.error_code ? (
                    <span className="font-mono text-xs text-red-700">
                      {trackedDeployment.error_code}
                    </span>
                  ) : null}
                </>
              ) : (
                <Loader2 aria-hidden className="h-4 w-4 animate-spin" />
              )}
            </div>
            {trackedDeployment?.verdict ? (
              <p className="mt-1 text-muted-foreground">{trackedDeployment.verdict}</p>
            ) : null}
            {trackedDeployment && TERMINAL.has(trackedDeployment.status ?? "") && trackedDeployment.status === "succeeded" ? (
              <p className="mt-1 flex items-center gap-1 text-emerald-700">
                <CheckCircle2 aria-hidden className="h-4 w-4" /> Deployed
              </p>
            ) : null}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function RollbackCard({ app }: { app: string }) {
  const queryClient = useQueryClient();
  const [revisionId, setRevisionId] = useState("");
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const [queuedId, setQueuedId] = useState("");

  const revisionsQuery = useQuery({
    queryKey: ["revisions", app],
    queryFn: () => listRevisions(app),
  });
  const rollbackMutation = useMutation({
    mutationFn: () =>
      rollbackDeployment(app, revisionId === "latest" ? undefined : revisionId),
    onSuccess: (resp) => {
      setQueuedId(resp.deployment_id ?? "");
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["deployments", app] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  const revisions = (revisionsQuery.data?.revisions ?? []).filter(
    (r) => r.status === "active",
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium text-muted-foreground">
          Rollback
        </CardTitle>
        <CardDescription>
          Snapshot replay (last 5 successful revisions). Empty target = revert
          one version.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-end gap-2">
          <div className="flex-1 space-y-2">
            <Label htmlFor="rollback-revision">Target revision</Label>
            <Select value={revisionId} onValueChange={setRevisionId}>
              <SelectTrigger id="rollback-revision">
                <SelectValue placeholder="Latest successful (revert one)" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="latest">Latest successful</SelectItem>
                {revisions.map((r) => (
                  <SelectItem key={r.id} value={r.id ?? ""}>
                    #{r.seq} · {(r.id ?? "").slice(0, 12)} · {timeAgo(r.created_at)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button
            variant="outline"
            onClick={() => rollbackMutation.mutate()}
            disabled={rollbackMutation.isPending}
          >
            {rollbackMutation.isPending ? (
              <Loader2 aria-hidden className="h-4 w-4 animate-spin" />
            ) : (
              <Undo2 aria-hidden className="h-4 w-4" />
            )}
            Rollback
          </Button>
        </div>
        {error ? (
          <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} docs={error.docs} />
        ) : null}
        {queuedId ? (
          <p className="text-sm text-muted-foreground">
            Rollback queued: <code className="font-mono text-xs">{queuedId}</code>
          </p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function DeploymentRow({
  d,
  app,
}: {
  d: DeploymentView;
  app: string;
}) {
  const queryClient = useQueryClient();
  const cancelMutation = useMutation({
    mutationFn: () => cancelDeployment(d.id ?? ""),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["deployments", app] });
    },
  });
  const cancellable = !TERMINAL.has(d.status ?? "");
  const showState =
    d.phase === "blocked_waiting" ? "blocked_waiting" : d.status ?? "";

  return (
    <TableRow data-testid="deployment-row" data-status={d.status}>
      <TableCell className="whitespace-nowrap">
        <div className="flex items-center gap-2">
          <StateBadge state={showState} />
          {d.kind === "rollback" ? (
            <span className="text-xs text-muted-foreground">(rollback)</span>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="font-mono text-xs">{d.id}</TableCell>
      <TableCell className="whitespace-nowrap text-xs">
        {formatTime(d.created_at)}
      </TableCell>
      <TableCell className="text-xs">
        {d.source_git_sha ? `${d.source_git_sha.slice(0, 7)}` : "—"}
      </TableCell>
      <TableCell className="max-w-[360px]">
        {d.status === "failed" && (d.error_code || d.verdict) ? (
          <DeploymentFailureAlert
            errorCode={d.error_code ?? ""}
            verdict={d.verdict ?? ""}
            recovery={d.recovery ?? ""}
            deploymentId={d.id ?? ""}
          />
        ) : (
          <span className="text-xs text-muted-foreground">
            {d.verdict || ""}
            {d.recovery ? ` · ${d.recovery}` : ""}
          </span>
        )}
      </TableCell>
      <TableCell>
        {cancellable ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => cancelMutation.mutate()}
            disabled={cancelMutation.isPending}
          >
            Cancel
          </Button>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

export function AppDeploymentsPage() {
  const { name = "" } = useParams();

  const historyQuery = useQuery({
    queryKey: ["deployments", name],
    queryFn: () => listDeployments(name, 20),
    refetchInterval: (q) => {
      // 存在非终态部署行时保持轮询（跟踪进行中的部署）。
      const rows = q.state.data?.deployments ?? [];
      return rows.some((d) => !TERMINAL.has(d.status ?? "")) ? 2000 : 10000;
    },
  });

  if (historyQuery.isError) {
    const envelope = errorEnvelopeFrom(historyQuery.error);
    return (
      <EnvelopeAlert
        code={envelope.code}
        message={envelope.message}
        suggestion={envelope.suggestion}
        docs={envelope.docs}
      />
    );
  }

  const deployments = historyQuery.data?.deployments ?? [];

  return (
    <div className="space-y-4">
      <DeployCard app={name} />
      <Card>
        <CardHeader>
          <CardTitle className="text-sm font-medium text-muted-foreground">
            Deployment history
          </CardTitle>
        </CardHeader>
        <CardContent>
          {deployments.length === 0 ? (
            <p className="text-sm text-muted-foreground">No deployments yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Status</TableHead>
                  <TableHead>ID</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>Git</TableHead>
                  <TableHead>Detail</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {deployments.map((d) => (
                  <DeploymentRow key={d.id} d={d} app={name} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      <RollbackCard app={name} />
    </div>
  );
}
