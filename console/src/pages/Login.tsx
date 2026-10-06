import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "../api/client";
import { setToken } from "../lib/token";
import { ErrorNote, PrimaryButton, TextInput } from "../components/ui";

// 登录页（F3.1：Token 输入从页头栏升格）：提交即 GET /v1/whoami 验证
// （错误信封原样呈现——吊销/错钥不做本地猜测），通过后落 localStorage
// 并失效全部查询。本平台凭证是 Token（无用户名密码面），"登录"= 验证
// 并记住一枚 Token。
export function LoginPage() {
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
    <div className="flex min-h-screen items-center justify-center bg-slate-950 px-4">
      <form onSubmit={submit} className="flex w-full max-w-sm flex-col gap-4 rounded-lg border border-slate-800 bg-slate-900/60 p-6">
        <div>
          <h1 className="text-lg font-semibold text-slate-100">fleetly console</h1>
          <p className="mt-1 text-xs text-slate-500">
            Sign in with an API token (create one with <code className="text-slate-400">fleetly tokens create</code>).
          </p>
        </div>
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
    </div>
  );
}
