import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch, apiSend } from "../api/client";
import { setToken } from "../lib/token";
import { describeError } from "../lib/api-errors";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

// 登录页（C6 密码会话双形态 → UI v2 批 5 reskin）：Password（用户名+密码
// → 服务端铸平台 token，secret 只在本响应出现一次）| API token（F3.1 原
// 形态）。两形态都经 /v1/whoami 验证后落 localStorage 并失效全部查询；
// 错误分状态诚实呈现（describeError 单源）。W1' 纪律：本页恰一 form 且
// 无 form 祖先（守卫测试锚定本文件）。
export function LoginPage() {
  const [mode, setMode] = useState<"password" | "token">("password");
  return (
    <div className="grid min-h-svh place-items-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="mb-5 flex flex-col items-center gap-2.5">
          <span
            aria-hidden
            className="size-11 rounded-2xl shadow-[0_0_24px_color-mix(in_oklch,var(--primary)_40%,transparent)]"
            style={{ backgroundImage: "var(--brand-gradient)" }}
          />
          <h1 className="font-heading text-lg font-bold tracking-tight">fleetly console</h1>
          <p className="text-xs text-muted-foreground">
            {mode === "password"
              ? "Sign in with your user name and password."
              : "Sign in with an API token (create one with fleetly tokens create)."}
          </p>
        </div>
        <div className="rounded-xl border bg-card p-6 shadow-[0_1px_2px_oklch(0_0_0/0.08)]">
          <div className="mb-4 flex gap-1 rounded-lg bg-muted p-1">
            {(["password", "token"] as const).map((candidate) => (
              <button
                key={candidate}
                type="button"
                onClick={() => setMode(candidate)}
                className={`flex-1 rounded-md px-3 py-1.5 text-xs font-semibold transition-colors ${
                  candidate === mode ? "bg-card shadow-sm" : "text-muted-foreground hover:text-foreground"
                }`}
              >
                {candidate === "password" ? "Password" : "API token"}
              </button>
            ))}
          </div>
          {mode === "password" ? <PasswordForm /> : <TokenForm />}
        </div>
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
    <form className="flex flex-col gap-3.5" onSubmit={submit}>
      <div className="flex flex-col gap-1.5">
        <Label className="text-xs">User name</Label>
        <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="user name" autoFocus spellCheck={false} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label className="text-xs">Password</Label>
        <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="password" />
      </div>
      {error != null ? (
        <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 text-xs">
          <span className="font-semibold text-[var(--status-danger)]">{describeError(error).title}</span>
          <span className="ml-1.5 text-[var(--status-danger)]/80">
            The credentials were rejected — the name may be wrong, the user has no password, or the password is mistyped.
          </span>
        </div>
      ) : null}
      <Button disabled={checking || name.trim() === "" || password === ""}>
        {checking ? "Checking…" : "Sign in"}
      </Button>
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
    <form className="flex flex-col gap-3.5" onSubmit={submit}>
      <div className="flex flex-col gap-1.5">
        <Label className="text-xs">API token</Label>
        <Input
          type="password"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          placeholder="paste a fleetly token"
          spellCheck={false}
          autoFocus
        />
      </div>
      {error != null ? (
        <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 text-xs">
          <span className="font-semibold text-[var(--status-danger)]">{describeError(error).title}</span>
          <span className="ml-1.5 text-[var(--status-danger)]/80">The token was rejected — it may be revoked or mistyped.</span>
        </div>
      ) : null}
      <Button disabled={checking || draft.trim() === ""}>{checking ? "Checking…" : "Sign in"}</Button>
    </form>
  );
}
