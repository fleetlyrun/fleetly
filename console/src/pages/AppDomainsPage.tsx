// 域名资源页（IMPL-T1-1）：per-domain {host, service, port, protocol(http|h2c),
// cert_mode(http01|wildcard)} 的 CRUD + verify 动作 + 证书状态。域名资源是
// 路由声明的唯一真值（写入即触发入口收敛）；compose fleetly.domains label
// 仅首部署 bootstrap 种子，state 有行后一律忽略（route.label_ignored 事件
// 披露）。写入门 = deploy（canDeploy）；平台管理员资源面只读的双门沿用
// （P0-3）。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe, Pencil, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";

import {
  createDomain,
  listDomains,
  removeDomain,
  updateDomain,
  verifyDomains,
} from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { DomainCertMode, DomainProtocol, DomainView } from "@/api/types";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
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
import { useIsPlatformAdmin, useTeamCapabilities } from "@/lib/context";
import { formatTime } from "@/lib/utils";

// DOMAIN_PATTERN 是 host 形态的前端体验门（服务端 idna 归一化与权威校验；
// 通配主机由服务端以 E_DOMAIN_UNSUPPORTED 拒绝——DNS-01 签发链沿 W5）。
const DOMAIN_PATTERN =
  /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])$/i;
const SERVICE_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

type DomainFormState = {
  domain: string;
  service: string;
  port: string;
  protocol: DomainProtocol;
  cert_mode: DomainCertMode;
};

function initialForm(row?: DomainView): DomainFormState {
  return {
    domain: row?.domain ?? "",
    service: row?.service ?? "",
    port: row?.port ?? "",
    protocol: (row?.protocol as DomainProtocol) || "http",
    cert_mode: (row?.cert_mode as DomainCertMode) || "http01",
  };
}

function formValid(f: DomainFormState, withDomain: boolean): boolean {
  const port = Number(f.port);
  return (
    (!withDomain || DOMAIN_PATTERN.test(f.domain)) &&
    SERVICE_PATTERN.test(f.service) &&
    Number.isInteger(port) &&
    port >= 1 &&
    port <= 65535
  );
}

// DomainDialog 是新增/编辑共用的表单对话框（新增可改 host；编辑时 host 是
// 资源身份，只读——改名 = 删除 + 重建）。
function DomainDialog({
  app,
  edit,
  onClose,
}: {
  app: string;
  edit?: DomainView;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [form, setForm] = useState<DomainFormState>(() => initialForm(edit));
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const editing = edit !== undefined;

  const mutation = useMutation({
    mutationFn: () => {
      const body = {
        service: form.service,
        port: form.port,
        protocol: form.protocol,
        cert_mode: form.cert_mode,
      };
      return editing
        ? updateDomain(app, edit.domain ?? "", body)
        : createDomain(app, { domain: form.domain, ...body });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["domains", app] });
      onClose();
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (formValid(form, !editing)) mutation.mutate();
  }

  return (
    <Dialog open onOpenChange={(v) => (v ? undefined : onClose())}>
      <DialogContent data-testid={editing ? "domain-edit-dialog" : "domain-add-dialog"}>
        <DialogHeader>
          <DialogTitle>{editing ? `Edit ${edit.domain}` : "Add domain"}</DialogTitle>
          <DialogDescription>
            {editing
              ? "The host is the resource identity; remove and re-add to rename it."
              : "The host is globally unique — the platform routes it to the chosen service port and converges the ingress right away."}
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-3" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <Label htmlFor="domain-host">Host</Label>
            <Input
              id="domain-host"
              data-testid="domain-host-input"
              className="font-mono text-xs"
              placeholder="api.example.com"
              value={form.domain}
              disabled={editing}
              onChange={(e) => setForm({ ...form, domain: e.target.value })}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="domain-service">Service</Label>
              <Input
                id="domain-service"
                data-testid="domain-service-input"
                className="font-mono text-xs"
                placeholder="web"
                value={form.service}
                onChange={(e) => setForm({ ...form, service: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="domain-port">Port</Label>
              <Input
                id="domain-port"
                data-testid="domain-port-input"
                className="font-mono text-xs"
                inputMode="numeric"
                placeholder="8080"
                value={form.port}
                onChange={(e) => setForm({ ...form, port: e.target.value })}
              />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>Protocol</Label>
              <Select
                value={form.protocol}
                onValueChange={(v) => setForm({ ...form, protocol: v as DomainProtocol })}
              >
                <SelectTrigger data-testid="domain-protocol-select" aria-label="Protocol">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http">http</SelectItem>
                  <SelectItem value="h2c">h2c (gRPC / HTTP2 cleartext)</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>Certificate</Label>
              <Select
                value={form.cert_mode}
                onValueChange={(v) => setForm({ ...form, cert_mode: v as DomainCertMode })}
              >
                <SelectTrigger data-testid="domain-cert-mode-select" aria-label="Certificate mode">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http01">http01 (per-domain ACME)</SelectItem>
                  <SelectItem value="wildcard">wildcard (DNS-01, lands with W5)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          {error ? (
            <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
          ) : null}
          <DialogFooter>
            <Button
              type="submit"
              size="sm"
              data-testid="domain-submit"
              disabled={!formValid(form, !editing) || mutation.isPending}
            >
              {mutation.isPending ? "Saving…" : editing ? "Save" : "Add domain"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// DomainRemoveDialog 是删除确认（routes withdraw immediately；证书 SAN 集
// 随下次签发收敛——如实说明）。
function DomainRemoveDialog({
  app,
  row,
  onClose,
}: {
  app: string;
  row: DomainView;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const mutation = useMutation({
    mutationFn: () => removeDomain(app, row.domain ?? ""),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["domains", app] });
      onClose();
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });
  return (
    <Dialog open onOpenChange={(v) => (v ? undefined : onClose())}>
      <DialogContent data-testid="domain-remove-dialog">
        <DialogHeader>
          <DialogTitle>Remove {row.domain}</DialogTitle>
          <DialogDescription>
            The route is withdrawn right away. The host becomes free for another
            app; the app certificate's SAN set converges on the next issuance.
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
        ) : null}
        <DialogFooter>
          <Button
            variant="destructive"
            data-testid="domain-remove-submit"
            disabled={mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {mutation.isPending ? "Removing…" : "Remove domain"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DomainRow({ app, row, canWrite }: { app: string; row: DomainView; canWrite: boolean }) {
  const [editOpen, setEditOpen] = useState(false);
  const [removeOpen, setRemoveOpen] = useState(false);
  return (
    <TableRow data-testid="domain-row" data-domain={row.domain}>
      <TableCell className="font-mono text-xs">{row.domain}</TableCell>
      <TableCell>{row.service}</TableCell>
      <TableCell className="font-mono text-xs">
        {row.port || <span className="text-muted-foreground">not reconciled</span>}
      </TableCell>
      <TableCell className="text-xs" data-testid="domain-row-protocol">
        {row.protocol || "http"}
      </TableCell>
      <TableCell className="text-xs" data-testid="domain-row-cert-mode">
        {row.cert_mode || "http01"}
      </TableCell>
      <TableCell className="text-xs">
        {row.cert_sha256 ? (
          <span className="space-y-0.5">
            <div className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
              <ShieldCheck aria-hidden className="h-3.5 w-3.5" />
              issued · expires {formatTime(row.cert_not_after)}
            </div>
            <code className="text-[10px] text-muted-foreground">
              sha256:{row.cert_sha256.slice(0, 16)}…
            </code>
          </span>
        ) : (
          <span className="text-muted-foreground">no certificate</span>
        )}
      </TableCell>
      <TableCell className="text-right">
        {canWrite ? (
          <span className="flex items-center justify-end gap-1">
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              aria-label={`Edit domain ${row.domain}`}
              data-testid="domain-edit-button"
              onClick={() => setEditOpen(true)}
            >
              <Pencil aria-hidden className="h-3.5 w-3.5" />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 text-red-600 dark:text-red-400"
              aria-label={`Remove domain ${row.domain}`}
              data-testid="domain-remove-button"
              onClick={() => setRemoveOpen(true)}
            >
              <Trash2 aria-hidden className="h-3.5 w-3.5" />
            </Button>
          </span>
        ) : null}
      </TableCell>
      {editOpen ? <DomainDialog app={app} edit={row} onClose={() => setEditOpen(false)} /> : null}
      {removeOpen ? (
        <DomainRemoveDialog app={app} row={row} onClose={() => setRemoveOpen(false)} />
      ) : null}
    </TableRow>
  );
}

export function AppDomainsPage() {
  const { name = "" } = useParams();
  const [verifyError, setVerifyError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  // 角色门（前端体验门，§3.2）：域名资源写 = deploy+（服务端 scope 硬门
  // 不变）；平台管理员资源面恒只读（P0-3 双门）——写面消失时以说明行明示
  // 原因，不做静默消失。
  const { canDeploy } = useTeamCapabilities();
  const isPlatformAdmin = useIsPlatformAdmin();

  const query = useQuery({
    queryKey: ["domains", name],
    queryFn: () => listDomains(name),
  });
  const verifyMutation = useMutation({
    mutationFn: () => verifyDomains(name),
    onError: (err) => setVerifyError(errorEnvelopeFrom(err)),
  });

  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <EnvelopeAlert
        code={envelope.code}
        message={envelope.message}
        suggestion={envelope.suggestion}
        docs={envelope.docs}
      />
    );
  }

  const domains = query.data?.domains ?? [];
  const checks = verifyMutation.data?.checks ?? [];

  return (
    <div className="space-y-4">
      <Card data-testid="domains-page">
        <CardHeader className="flex-row items-center justify-between space-y-0 border-b pb-3">
          <div>
            <CardTitle className="flex items-center gap-2 text-sm font-semibold">
              <Globe aria-hidden className="h-4 w-4 text-muted-foreground" />
              Domains
              <span className="font-normal text-muted-foreground">({domains.length})</span>
            </CardTitle>
            <CardDescription className="mt-1">
              Route domain resources — the ingress renders Traefik from these
              rows. compose <code>fleetly.domains</code> labels seed the first
              deploy only; once rows exist they are ignored (a{" "}
              <code>route.label_ignored</code> event is raised).
            </CardDescription>
          </div>
          <div className="flex items-center gap-2">
            {canDeploy ? (
              <Button
                size="sm"
                data-testid="domain-add-button"
                onClick={() => setAddOpen(true)}
              >
                <Plus aria-hidden className="h-3.5 w-3.5" />
                Add domain
              </Button>
            ) : null}
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7"
              aria-label="Refresh domains"
              onClick={() => void query.refetch()}
            >
              <RefreshCw aria-hidden className="h-3.5 w-3.5" />
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setVerifyError(null);
                verifyMutation.mutate();
              }}
              disabled={verifyMutation.isPending}
            >
              <ShieldCheck aria-hidden className="h-3.5 w-3.5" />
              Verify
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {/* Verify 的性质说明（2026-09-25 走查）：VerifyAppDomains 服务端
              登记 read scope（scope.go DomainsService）+ handler 内读层角色
              门（internal/api/read.go requireAppAccess）——探测不写任何平台
              状态，对全部项目角色（含 viewer）可用是服务端事实，按钮保留
              全角色；此行只把动作性质说清。 */}
          <p
            className="mb-3 text-xs text-muted-foreground"
            data-testid="domains-verify-note"
          >
            Verify runs a read-only local probe (DNS + HTTP reachability) — it
            changes nothing and is available to every project role.
          </p>
          {!canDeploy && isPlatformAdmin ? (
            <section className="mb-3" data-testid="domains-write-note">
              <p className="text-sm text-muted-foreground">
                Platform administrators have read-only access to resources
                (separation of duties). Manage domains from the CLI with a
                machine token (<code>fleetly domains add</code>), or ask a team
                owner for a member role.
              </p>
            </section>
          ) : null}
          {verifyError ? (
            <div className="mb-3">
              <EnvelopeAlert
                code={verifyError.code}
                message={verifyError.message}
                suggestion={verifyError.suggestion}
                docs={verifyError.docs}
              />
            </div>
          ) : null}
          {domains.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No domains declared for this app.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domain</TableHead>
                  <TableHead>Service</TableHead>
                  <TableHead>Port</TableHead>
                  <TableHead>Protocol</TableHead>
                  <TableHead>Cert mode</TableHead>
                  <TableHead>Certificate</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {domains.map((d) => (
                  <DomainRow key={`${d.service}:${d.domain}`} app={name} row={d} canWrite={canDeploy} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {verifyMutation.data ? (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-sm font-medium text-muted-foreground">
              Verification result (local probe — judgement is yours)
            </CardTitle>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domain</TableHead>
                  <TableHead>DNS</TableHead>
                  <TableHead>:80</TableHead>
                  <TableHead>:443</TableHead>
                  <TableHead>Served certificate</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {checks.map((c) => (
                  <TableRow key={c.domain}>
                    <TableCell className="font-mono text-xs">{c.domain}</TableCell>
                    <TableCell className="text-xs">
                      {c.resolved ? (c.ips ?? []).join(", ") : (
                        <span className="text-red-600 dark:text-red-400">{c.error || "unresolved"}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-xs">{c.http_80 || "—"}</TableCell>
                    <TableCell className="text-xs">{c.https_443 || "—"}</TableCell>
                    <TableCell className="text-xs">
                      {c.cert_subject || "—"}
                      {c.cert_not_after
                        ? ` · expires ${formatTime(c.cert_not_after)}`
                        : ""}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}

      {addOpen ? <DomainDialog app={name} onClose={() => setAddOpen(false)} /> : null}
    </div>
  );
}
