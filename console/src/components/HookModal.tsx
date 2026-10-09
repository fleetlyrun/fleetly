import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { apiFetch, ApiError } from "../api/client";
import type { components as deliverySchemas } from "../api/delivery";
import { Modal, ErrorNote, LoadingNote, Field, TextInput, PrimaryButton, DangerRowButton, MutationBanner, useApiMutation } from "./ui";

type GitHook = deliverySchemas["schemas"]["v1GitHook"];

// HookModal 是 per-App git hook 面板（C2 治理批）：get/set/rotate 三动词
// 对齐 CLI（hooks get/set/rotate）。secret 是一次性明文（flthook_ 前缀，
// 兼 GitHub webhook secret）——只在铸造/轮换响应出现一次，配置面只有
// token_prefix 泄露识别锚。webhook URL = <console 同源>/v1/hooks/<secret>。
export function HookModal({ appId, appName, open, onClose }: { appId: string; appName: string; open: boolean; onClose: () => void }) {
  const [oneTimeSecret, setOneTimeSecret] = useState<string | null>(null);
  const hook = useQuery({
    queryKey: ["hook", appId],
    enabled: open,
    retry: false,
    queryFn: async (): Promise<GitHook> => {
      const res = await apiFetch<{ hook?: GitHook }>(`/v1/apps/${encodeURIComponent(appId)}/hook`);
      if (!res.hook) throw new ApiError(404, "E_NOT_FOUND", "no hook configured");
      return res.hook;
    },
  });
  const notFound = hook.isError && hook.error instanceof ApiError && (hook.error.code === "E_NOT_FOUND" || hook.error.status === 404);
  const rotate = useApiMutation<{ hook?: GitHook; secret?: string }>({
    path: () => `/v1/apps/${encodeURIComponent(appId)}/hook/rotate`,
    method: "POST",
    body: () => ({}),
  });

  if (oneTimeSecret != null) {
    return (
      <Modal title="Webhook secret (shown once)" open={open} onClose={() => { setOneTimeSecret(null); onClose(); }}>
        <p className="text-xs text-slate-400">
          Point the repository webhook at this URL (the secret doubles as the GitHub signing secret). It is shown once — store it now.
        </p>
        <pre className="overflow-x-auto rounded-md border border-slate-800 bg-slate-950 px-3 py-2 font-mono text-xs text-emerald-300">
          {`${window.location.origin}/v1/hooks/${oneTimeSecret}`}
        </pre>
        <PrimaryButton onClick={() => { setOneTimeSecret(null); onClose(); }}>Done</PrimaryButton>
      </Modal>
    );
  }

  return (
    <Modal title={`Git hook — ${appName}`} open={open} onClose={onClose}>
      {hook.isPending ? <LoadingNote label="loading hook…" /> : null}
      {hook.isError && !notFound ? <ErrorNote error={hook.error} /> : null}
      {hook.data != null ? (
        <div className="flex flex-col gap-3">
          <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1.5 text-xs">
            <dt className="text-slate-500">repo</dt>
            <dd className="break-all font-mono text-slate-300">{hook.data.repo}</dd>
            <dt className="text-slate-500">branch</dt>
            <dd className="font-mono text-slate-300">{hook.data.branch || "(all branches)"}</dd>
            <dt className="text-slate-500">dockerfile</dt>
            <dd className="font-mono text-slate-300">{hook.data.dockerfile || "Dockerfile"}</dd>
            <dt className="text-slate-500">watch paths</dt>
            <dd className="font-mono text-slate-300">{(hook.data.watch_paths ?? []).join(" ") || "(all changes)"}</dd>
            <dt className="text-slate-500">token prefix</dt>
            <dd className="font-mono text-slate-300">{hook.data.token_prefix}…</dd>
            <dt className="text-slate-500">updated</dt>
            <dd className="text-slate-500">{hook.data.updated_at}</dd>
          </dl>
          <p className="text-xs text-slate-500">
            Pushes to the matching branch rebuild and deploy this app. The URL token doubles as the webhook signing secret; rotate it if it leaked.
          </p>
          <MutationBanner pending={rotate.isPending} error={rotate.error} success={null} />
          <div className="flex justify-end gap-2">
            <DangerRowButton
              confirm={`Rotate the hook token for ${appName}? The old webhook URL stops working immediately.`}
              disabled={rotate.isPending}
              onClick={() => rotate.mutate(undefined, { onSuccess: (data) => { if (data.secret) setOneTimeSecret(data.secret); } })}
            >
              rotate token…
            </DangerRowButton>
            <PrimaryButton onClick={onClose}>Close</PrimaryButton>
          </div>
        </div>
      ) : null}
      {notFound ? <SetHookForm appId={appId} onMinted={(secret) => setOneTimeSecret(secret)} onCancel={onClose} /> : null}
    </Modal>
  );
}

// SetHookForm 是首配表单（服务端 Set 幂等：已配置再 Set 只改配置不换
// Token——本表单只在"无 hook"态出现，轮换走 rotate）。
function SetHookForm({ appId, onMinted, onCancel }: { appId: string; onMinted: (secret: string) => void; onCancel: () => void }) {
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("");
  const [dockerfile, setDockerfile] = useState("");
  const [watchPaths, setWatchPaths] = useState("");
  const set = useApiMutation<{ hook?: GitHook; secret?: string }>({
    path: `/v1/apps/${encodeURIComponent(appId)}/hook`,
    method: "POST",
    body: () => ({
      repo,
      branch: branch === "" ? undefined : branch,
      dockerfile: dockerfile === "" ? undefined : dockerfile,
      watch_paths: watchPaths === "" ? undefined : watchPaths.split(/[\s,]+/).filter((part) => part !== ""),
    }),
  });
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault();
        set.mutate(undefined, { onSuccess: (data) => { if (data.secret) onMinted(data.secret); } });
      }}
    >
      <p className="text-xs text-slate-500">No hook yet — configure one to rebuild and deploy on push.</p>
      <Field label="Repository URL">
        <TextInput value={repo} onChange={(event) => setRepo(event.target.value)} placeholder="https://github.com/org/repo" autoFocus required />
      </Field>
      <Field label="Branch filter" hint="empty = all branches (tags never trigger)">
        <TextInput value={branch} onChange={(event) => setBranch(event.target.value)} placeholder="main" />
      </Field>
      <Field label="Dockerfile path" hint="defaults to Dockerfile">
        <TextInput value={dockerfile} onChange={(event) => setDockerfile(event.target.value)} placeholder="Dockerfile" />
      </Field>
      <Field label="Watch paths" hint="space-separated prefixes; empty = every change triggers">
        <TextInput value={watchPaths} onChange={(event) => setWatchPaths(event.target.value)} placeholder="cmd/ internal/" spellCheck={false} />
      </Field>
      <MutationBanner pending={set.isPending} error={set.error} success={null} />
      <div className="flex justify-end gap-2">
        <DangerRowButton confirm="Discard the hook form?" onClick={onCancel}>cancel</DangerRowButton>
        <PrimaryButton disabled={set.isPending || repo === ""}>Configure hook</PrimaryButton>
      </div>
    </form>
  );
}
