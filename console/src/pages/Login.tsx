import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch, apiSend } from "../api/client";
import { setToken } from "../lib/token";
import { ErrorNote, PrimaryButton, TextInput } from "../components/ui";

// 登录页（C6 密码会话第一期双形态）：Password（用户名+密码 → 服务端铸
// 平台 token，secret 只在本响应出现一次）| API token（F3.1 原形态，粘
// 贴粘贴）。两形态都经 /v1/whoami 验证后落 localStorage 并失效全部查询；
// 错误信封原样呈现（吊销/错钥/错密不做本地猜测）。
export function LoginPage() {
  const [mode, setMode] = useState<"password" | "token">("password");
  return (
    <div className="flex min-h-screen items-center justify-center bg-slate-950 px-4">
      <div className="flex w-full max-w-sm flex-col gap-4 rounded-lg border border-slate-800 bg-slate-900/60 p-6">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">fleetly console</h1>
          <p className="mt-1 text-xs text-slate-500">
            {mode === "password"
              ? "Sign in with your user name and password."
              : "Sign in with an API token (create one with fleetly tokens create)."}
          </p>
        </div>
        <div className="flex gap-1">
          {(["password", "token"] as const).map((candidate) => (
            <button
              key={candidate}
              type="button"
              onClick={() => setMode(candidate)}
              className={
                candidate === mode
                  ? "rounded-md bg-slate-800 px-3 py-1.5 text-xs font-medium text-slate-100"
                  : "rounded-md px-3 py-1.5 text-xs text-slate-400 hover:bg-slate-900 hover:text-slate-200"
              }
            >
              {candidate === "password" ? "Password" : "API token"}
            </button>
          ))}
        </div>
        {mode === "password" ? <PasswordForm /> : <TokenForm />}
      </div>
    </div>
  );
}

function PasswordForm() {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [checking, setChecking] = useState(false);
  const queryClient = useQueryClient();

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (name.trim() === "" || password === "") return;
    setChecking(true);
    setError(null);
    try {
      const res = await apiSend<{ secret?: string }>("/v1/auth/login", "POST", {
        name: name.trim(),
        password,
      });
      if (!res.secret) throw new Error("login response carried no secret");
      setToken(res.secret);
      void queryClient.invalidateQueries();
    } catch (cause) {
      setError(cause);
    } finally {
      setChecking(false);
    }
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={submit}>
      <TextInput value={name} onChange={(event) => setName(event.target.value)} placeholder="user name" autoFocus spellCheck={false} />
      <TextInput type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="password" />
      {error != null ? <ErrorNote error={error} hint="The credentials were rejected — the name may be wrong, the user has no password, or the password is mistyped." /> : null}
      <PrimaryButton disabled={checking || name.trim() === "" || password === ""}>{checking ? "Checking…" : "Sign in"}</PrimaryButton>
    </form>
  );
}

function TokenForm() {
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [checking, setChecking] = useState(false);
  const queryClient = useQueryClient();

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (draft.trim() === "") return;
    setChecking(true);
    setError(null);
    try {
      await apiFetch("/v1/whoami", { headers: { Authorization: `Bearer ${draft.trim()}` } });
      setToken(draft);
      void queryClient.invalidateQueries();
    } catch (cause) {
      setError(cause);
    } finally {
      setChecking(false);
    }
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={submit}>
      <TextInput
        type="password"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
        placeholder="paste a fleetly token"
        spellCheck={false}
        autoFocus
      />
      {error != null ? <ErrorNote error={error} hint="The token was rejected — it may be revoked or mistyped." /> : null}
      <PrimaryButton disabled={checking || draft.trim() === ""}>{checking ? "Checking…" : "Sign in"}</PrimaryButton>
    </form>
  );
}
