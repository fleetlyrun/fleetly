import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { apiSend } from "../api/client";
import { setToken } from "../lib/token";
import { describeError } from "../lib/api-errors";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

// 登录页（C6 密码会话 → 2026-10-11 用户裁决收窄单形态）：用户名+密码
// → 服务端铸平台 token（secret 只在本响应出现一次）落 localStorage 并
// 失效全部查询。原 API token 粘贴形态退役（登录面不再收编手工凭证；
// token 生命周期治理仍在 Settings API tokens 面）。错误分状态诚实呈现
// （describeError 单源）。W1' 纪律：本页恰一 form 且无 form 祖先。
export function LoginPage() {
  return (
    <div className="grid min-h-svh place-items-center bg-bg px-6 py-10">
      <div className="w-full max-w-sm">
        {/* 登录页同源 Dokploy 页卡：外 rail + 内层背景卡；认证页不套
            Console shell，但 token/圆角/内距与主界面同一套。 */}
        <div className="rounded-xl bg-sidebar p-2.5">
          <div className="rounded-xl border border-line bg-background p-7">
            <div className="mb-6 flex flex-col items-center gap-2.5">
              <img src="/logo.png" alt="" aria-hidden className="size-12 rounded-xl" />
              <h1 className="font-heading text-2xl font-semibold tracking-tight">fleetly console</h1>
              <p className="text-center text-sm text-muted">Sign in with your user name and password.</p>
            </div>
            <PasswordForm />
          </div>
        </div>
        <p className="mt-4 text-center text-2xs text-muted">fleetly control plane · console v2</p>
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
      <div className="flex flex-col gap-1.5">
        <Label className="text-xs font-medium">User name</Label>
        <Input className="h-9" value={name} onChange={(event) => setName(event.target.value)} placeholder="user name" autoFocus spellCheck={false} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label className="text-xs font-medium">Password</Label>
        <Input className="h-9" type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder="password" />
      </div>
      {error != null ? (
        <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 text-xs">
          <span className="font-semibold text-[var(--status-danger)]">{describeError(error).title}</span>
          <span className="ml-1.5 text-[var(--status-danger)]/80">
            The credentials were rejected — the name may be wrong, the user has no password, or the password is mistyped.
          </span>
        </div>
      ) : null}
      <Button className="mt-1 h-9 w-full" disabled={checking || name.trim() === "" || password === ""}>
        {checking ? "Checking…" : "Sign in"}
      </Button>
    </form>
  );
}
