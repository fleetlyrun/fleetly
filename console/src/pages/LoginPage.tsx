// 登录页（v0.3 RBAC W1，设计 §7）：主形态 = 邮箱+口令（POST
// /v1/auth/login，成功经 Set-Cookie 下发会话）；注册入口由 GET
// /v1/auth/registration 驱动（open=true 或无用户窗口才出现），注册成功即
// 自动登录（Register 同事务下发 cookie）。**API token 登录面已移除**
// （2026-09-25 用户裁决：Console 只走会话 cookie；CLI/CI 的 token 体系
// 不受影响——PAT 在「用户菜单 → API tokens」页创建管理）。
//
// 时序纪律（M9-1 沿用）：loginWithPassword 成功（服务端已下发 cookie）才
// 翻转 authed；错口令的 401 是表单业务结果（optionalAuth 豁免 Gate 级
// 401 横幅），唯一展示面是本页信封。成功进入后 navigate(from ?? "/")——
// 来源页（深链/邀请链接）恢复由 anon 门卫的 from 暂存承担。

import { useQuery } from "@tanstack/react-query";
import { KeyRound, Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { getRegistrationState } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { useAuth } from "@/auth";
import { AuthCard } from "@/components/auth-card";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type AuthMode = "signin" | "register";

interface PageError {
  message: string;
  envelope: ReturnType<typeof errorEnvelopeFrom>;
}

export function LoginPage({
  authError,
  onAuthErrorSeen,
}: {
  authError?: string;
  onAuthErrorSeen?: () => void;
} = {}) {
  const { loginWithPassword, registerWithPassword } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [mode, setMode] = useState<AuthMode>("signin");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<PageError | null>(null);

  const from = (location.state as { from?: string } | null)?.from;

  // 注册入口开关（设计 §2.1/§7）：open=true 或 has_users=false（无用户
  // 窗口）才出现；查询失败按关闭处理（不闪入口，保守口径）。
  const registrationQuery = useQuery({
    queryKey: ["auth", "registration"],
    queryFn: getRegistrationState,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  });
  const registrationOpen = registrationQuery.data
    ? registrationQuery.data.open === true ||
      registrationQuery.data.has_users === false
    : false;

  function switchMode(next: AuthMode) {
    setMode(next);
    setError(null);
  }

  async function submitPassword(e: FormEvent) {
    e.preventDefault();
    onAuthErrorSeen?.();
    if (busy) return;
    setError(null);
    if (!email.trim() || !password) return;

    if (mode === "register") {
      // 前端校验先于请求（口令≥8、两次一致——与 proto buf.validate 对齐）。
      if (password.length < 8) {
        setError({
          message: "Password must be at least 8 characters.",
          envelope: { message: "Password must be at least 8 characters." },
        });
        return;
      }
      if (password !== confirm) {
        setError({
          message: "Passwords do not match.",
          envelope: { message: "Passwords do not match." },
        });
        return;
      }
    }

    setBusy(true);
    try {
      if (mode === "signin") {
        await loginWithPassword(email.trim(), password);
      } else {
        await registerWithPassword({
          email: email.trim(),
          password,
          displayName: displayName.trim() || undefined,
        });
      }
      navigate(from ?? "/", { replace: true });
    } catch (err) {
      setError({
        message: `${mode === "signin" ? "Sign-in" : "Registration"} failed: ${
          err instanceof Error ? err.message : String(err)
        }`,
        envelope: errorEnvelopeFrom(err),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthCard
      description={
        mode === "signin" ? (
          <>Sign in with your email and password.</>
        ) : (
          <>
            Create the first account of this fleet, or join while registration
            is open. You are signed in automatically.
          </>
        )
      }
    >
      {mode === "signin" ? (
        <form className="space-y-4" data-testid="login-form" onSubmit={submitPassword}>
          <div className="space-y-2">
            <Label htmlFor="login-email">Email</Label>
            <Input
              id="login-email"
              name="email"
              type="email"
              data-testid="login-email"
              placeholder="you@example.com"
              autoComplete="email"
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="login-password">Password</Label>
            <Input
              id="login-password"
              name="password"
              type="password"
              data-testid="login-password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          {authError ? (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-sm text-amber-800 dark:text-amber-300"
            >
              <KeyRound aria-hidden className="mt-0.5 h-4 w-4 shrink-0" />
              {authError}
            </div>
          ) : null}
          {error ? (
            <EnvelopeAlert
              code={error.envelope.code}
              message={error.message}
              suggestion={
                error.envelope.suggestion ??
                "Check your email and password, then try again."
              }
            />
          ) : null}
          <Button
            type="submit"
            className="w-full"
            data-testid="login-submit"
            disabled={!email.trim() || !password || busy}
          >
            {busy ? <Loader2 aria-hidden className="h-4 w-4 animate-spin" /> : null}
            Sign in
          </Button>
          {registrationOpen ? (
            <Button
              type="button"
              variant="ghost"
              className="w-full"
              data-testid="register-toggle"
              onClick={() => switchMode("register")}
            >
              Need an account? Create one
            </Button>
          ) : null}
        </form>
      ) : (
        <form className="space-y-4" data-testid="register-form" onSubmit={submitPassword}>
          <div className="space-y-2">
            <Label htmlFor="register-email">Email</Label>
            <Input
              id="register-email"
              name="email"
              type="email"
              data-testid="register-email"
              placeholder="you@example.com"
              autoComplete="email"
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="register-display-name">
              Display name <span className="text-muted-foreground">(optional)</span>
            </Label>
            <Input
              id="register-display-name"
              name="display_name"
              type="text"
              data-testid="register-display-name"
              autoComplete="name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="register-password">Password</Label>
            <Input
              id="register-password"
              name="password"
              type="password"
              data-testid="register-password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">At least 8 characters.</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="register-confirm">Confirm password</Label>
            <Input
              id="register-confirm"
              name="confirm"
              type="password"
              data-testid="register-confirm"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          </div>
          {error ? (
            <EnvelopeAlert
              code={error.envelope.code}
              message={error.message}
              suggestion={error.envelope.suggestion}
            />
          ) : null}
          <Button
            type="submit"
            className="w-full"
            data-testid="register-submit"
            disabled={!email.trim() || !password || busy}
          >
            {busy ? <Loader2 aria-hidden className="h-4 w-4 animate-spin" /> : null}
            Create account
          </Button>
          <Button
            type="button"
            variant="ghost"
            className="w-full"
            data-testid="login-toggle"
            onClick={() => switchMode("signin")}
          >
            Already have an account? Sign in
          </Button>
        </form>
      )}
    </AuthCard>
  );
}
