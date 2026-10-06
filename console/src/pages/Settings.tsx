import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch } from "../api/client";
import { useRoles, useTokens, useWhoami } from "../lib/catalog";
import {
  DangerRowButton,
  EmptyNote,
  ErrorNote,
  Field,
  LoadingNote,
  Modal,
  MutationBanner,
  PageShell,
  PrimaryButton,
  RowButton,
  Select,
  TableHead,
  TableWrap,
  TextInput,
  formatTime,
  useApiMutation,
} from "../components/ui";

// 设置页（F3.1）：身份（whoami）/ Token 铸造与吊销（Console 自身的凭证
// 生命周期）/ 平台治理（变更冻结 + 平台备份触发——治理刹车是全局写面
// 的开关，放在设置页就近呈现）。

export function SettingsPage() {
  return (
    <PageShell title="Settings" hint="identity, API tokens and platform governance">
      <div className="flex flex-col gap-6">
        <IdentityCard />
        <TokensCard />
        <GovernanceCard />
      </div>
    </PageShell>
  );
}

function IdentityCard() {
  const whoami = useWhoami(true);
  return (
    <section className="rounded-lg border border-slate-800 px-4 py-3">
      <h2 className="text-sm font-semibold text-slate-200">identity</h2>
      {whoami.isPending ? (
        <LoadingNote label="Checking token…" />
      ) : whoami.isError ? (
        <ErrorNote error={whoami.error} hint="GET /v1/whoami failed." />
      ) : (
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs text-slate-400">
          <dt>token</dt>
          <dd className="text-slate-200">{whoami.data.tokenName || "—"}</dd>
          <dt>role</dt>
          <dd>{whoami.data.roleName || "—"}</dd>
        </dl>
      )}
    </section>
  );
}

function TokensCard() {
  const tokens = useTokens();
  const roles = useRoles();
  const [createOpen, setCreateOpen] = useState(false);
  const [name, setName] = useState("");
  const [roleId, setRoleId] = useState("");
  const [minted, setMinted] = useState<string | null>(null);
  const create = useApiMutation<{ token?: { id?: string }; secret?: string }>({
    path: "/v1/tokens",
    method: "POST",
    body: () => ({ name, role_id: roleId || roles.data?.[0]?.id }),
    invalidate: [["settings", "tokens"]],
  });
  return (
    <section className="rounded-lg border border-slate-800 px-4 py-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-slate-200">API tokens</h2>
        <RowButton
          onClick={() => {
            setName("");
            setMinted(null);
            setCreateOpen(true);
          }}
        >
          New token…
        </RowButton>
      </div>
      <Modal title="New API token" open={createOpen} onClose={() => setCreateOpen(false)}>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            setMinted(null);
            create.mutate(undefined, {
              onSuccess: (response) => {
                setMinted(response.secret ?? "secret not returned");
                create.reset();
              },
            });
          }}
        >
          <Field label="Name">
            <TextInput value={name} onChange={(event) => setName(event.target.value)} autoFocus />
          </Field>
          <Field label="Role">
            <Select value={roleId || roles.data?.[0]?.id || ""} onChange={(event) => setRoleId(event.target.value)}>
              {(roles.data ?? []).map((role) => (
                <option key={role.id} value={role.id}>
                  {role.name}
                </option>
              ))}
            </Select>
          </Field>
          {minted != null ? (
            <div className="rounded-md border border-amber-900/60 bg-amber-950/40 px-3 py-2 text-xs text-amber-300">
              <div className="font-medium">Secret shown once — copy it now:</div>
              <div className="mt-1 break-all font-mono">{minted}</div>
            </div>
          ) : null}
          <MutationBanner pending={create.isPending} error={create.isError ? create.error : null} success={null} />
          <div className="flex justify-end gap-2">
            {minted != null ? (
              <PrimaryButton onClick={() => setCreateOpen(false)}>Done</PrimaryButton>
            ) : (
              <PrimaryButton disabled={create.isPending || name === ""}>Create</PrimaryButton>
            )}
          </div>
        </form>
      </Modal>
      {tokens.isPending ? (
        <LoadingNote label="Loading tokens…" />
      ) : tokens.isError ? (
        <ErrorNote error={tokens.error} hint="GET /v1/tokens failed — token administration needs elevated scope." />
      ) : (tokens.data ?? []).length === 0 ? (
        <EmptyNote label="No tokens visible to this role." />
      ) : (
        <TableWrap>
          <table className="w-full text-sm">
            <TableHead columns={["Name", "Role", "Created", "Last used", ""]} />
            <tbody>
              {(tokens.data ?? []).map((token) => (
                <TokenRow key={token.id} token={token} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function TokenRow({ token }: { token: { id?: string; name?: string; role_id?: string; created_at?: string; last_used_at?: string; revoked_at?: string } }) {
  const revoke = useApiMutation({
    path: `/v1/tokens/${encodeURIComponent(token.id ?? "")}/revoke`,
    method: "POST",
    invalidate: [["settings", "tokens"]],
  });
  return (
    <tr className={`border-b border-slate-800/60 hover:bg-slate-900/40 ${token.revoked_at ? "opacity-50" : ""}`}>
      <td className="px-3 py-2 font-medium text-slate-200">
        {token.name}
        {token.revoked_at ? <span className="ml-2 text-xs text-slate-500">revoked</span> : null}
      </td>
      <td className="px-3 py-2 font-mono text-xs text-slate-500" title={token.role_id}>
        {token.role_id?.slice(0, 10)}…
      </td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(token.created_at)}</td>
      <td className="px-3 py-2 text-xs text-slate-500">{formatTime(token.last_used_at)}</td>
      <td className="px-3 py-2 text-right">
        {!token.revoked_at ? (
          <DangerRowButton
            confirm={`Revoke token ${token.name}? Clients using it fail immediately (this includes the console if it is the current token).`}
            disabled={revoke.isPending}
            onClick={() => void revoke.mutate()}
          >
            revoke
          </DangerRowButton>
        ) : null}
        {revoke.isError ? <ErrorNote error={revoke.error} /> : null}
      </td>
    </tr>
  );
}

function GovernanceCard() {
  const freezes = useQuery({
    queryKey: ["settings", "freezes"],
    queryFn: async () => {
      const res = await apiFetch<{ freezes?: Array<{ id?: string; team_id?: string; reason?: string; created_at?: string; lifted_at?: string } | undefined> }>("/v1/governance/freezes");
      return (res.freezes ?? []).flatMap((row) => (row != null ? [row] : []));
    },
    refetchInterval: 30_000,
  });
  const [reason, setReason] = useState("");
  const freeze = useApiMutation({
    path: "/v1/governance/freezes",
    method: "POST",
    body: () => ({ reason }),
    invalidate: [["settings", "freezes"]],
  });
  const backup = useApiMutation<{ backup?: { id?: string } }>({
    path: "/v1/platform/backups",
    method: "POST",
    body: () => ({}),
  });
  const active = (freezes.data ?? []).filter((row) => !row.lifted_at);
  return (
    <section className="rounded-lg border border-slate-800 px-4 py-3">
      <h2 className="text-sm font-semibold text-slate-200">platform governance</h2>
      <div className="mt-3 flex flex-col gap-4">
        {active.length > 0 ? (
          <div className="rounded-md border border-amber-900/60 bg-amber-950/30 px-3 py-2">
            <div className="text-xs font-medium text-amber-300">change freeze active</div>
            <ul className="mt-1 flex flex-col gap-1">
              {active.map((row) => (
                <li key={row.id} className="flex items-center justify-between gap-3 text-xs text-amber-200/80">
                  <span>
                    {row.reason} · {formatTime(row.created_at)}
                    <span className="ml-2 font-mono text-amber-200/50" title={row.id}>
                      {row.id?.slice(0, 10)}…
                    </span>
                  </span>
                  <LiftButton freezeId={row.id ?? ""} reason={row.reason ?? ""} />
                </li>
              ))}
            </ul>
          </div>
        ) : null}
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            freeze.mutate(undefined, { onSuccess: () => setReason("") });
          }}
        >
          <Field label="Set change freeze" hint="platform-wide (all teams); change verbs are refused until lifted">
            <TextInput value={reason} onChange={(event) => setReason(event.target.value)} placeholder="maintenance window" />
          </Field>
          <PrimaryButton disabled={freeze.isPending || reason === ""}>set freeze</PrimaryButton>
          <MutationBanner pending={freeze.isPending} error={freeze.isError ? freeze.error : null} success={null} />
        </form>
        <div className="flex flex-wrap items-center gap-3 border-t border-slate-800 pt-3">
          <RowButton disabled={backup.isPending} onClick={() => void backup.mutate()}>
            {backup.isPending ? "Backing up…" : "Trigger platform backup"}
          </RowButton>
          <span className="text-xs text-slate-500">synchronous full platform snapshot (SQLite + secrets + object store)</span>
          <MutationBanner
            pending={backup.isPending}
            error={backup.isError ? backup.error : null}
            success={backup.isSuccess ? `platform backup ${backup.data?.backup?.id ?? ""} finished` : null}
          />
        </div>
      </div>
    </section>
  );
}

function LiftButton({ freezeId, reason }: { freezeId: string; reason: string }) {
  const lift = useApiMutation({
    path: `/v1/governance/freezes/${encodeURIComponent(freezeId)}/lift`,
    method: "POST",
    invalidate: [["settings", "freezes"]],
  });
  return (
    <DangerRowButton confirm={`Lift the change freeze "${reason}"?`} disabled={lift.isPending} onClick={() => void lift.mutate()}>
      lift
    </DangerRowButton>
  );
}
