// 登录页（v0.3 RBAC W1，设计 §7）：主形态 = 邮箱+口令（POST
// /v1/auth/login，成功经 Set-Cookie 下发会话）；注册入口由 GET
// /v1/auth/registration 驱动（open=true 或无用户窗口才出现），注册成功即
// 自动登录（Register 同事务下发 cookie）。「API token 登录」收进折叠高级
// 区（运维/CLI 直连路径，v0.1 口径保留：localStorage 存 token + Bearer）。
//
// 时序纪律（M9-1，token 路径沿用）：先校验后持久化——校验期间 token 只进
// API 层的 localStorage（listApps 需要 Bearer），auth 状态不翻转（Gate 不
// 卸载本页）；校验通过才 loginWithToken() 翻转 + 进入，失败则清除凭据并
// 停留展示错误信封。口令路径同纪律：loginWithPassword 成功（服务端已下发
// cookie）才翻转 authed；错口令的 401 是表单业务结果（optionalAuth 豁免
// Gate 级 401 横幅），唯一展示面是本页信封。成功进入后 navigate(from ??
// "/")——来源页（深链/邀请链接）恢复由 anon 门卫的 from 暂存承担。

import { useQuery } from "@tanstack/react-query";
import { ChevronDown, KeyRound, Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { getRegistrationState, listApps } from "@/api/endpoints";
import { clearToken, setToken } from "@/api/client";
import { errorEnvelopeFrom } from "@/api/errors";
import { useAuth } from "@/auth";
import { AuthCard } from "@/components/auth-card";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

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
  const { loginWithToken, loginWithPassword, registerWithPassword } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [mode, setMode] = useState<AuthMode>("signin");
  const [showToken, setShowToken] = useState(false);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [confirm, setConfirm] = useState("");
  const [token, setTokenInput] = useState("");
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

  async function submitToken(e: FormEvent) {
    e.preventDefault();
    onAuthErrorSeen?.();
    if (!token.trim() || busy) return;
    const trimmed = token.trim();
    setBusy(true);
    setError(null);
    // 仅写 API 层凭据（校验请求要带 Bearer）；authed 不翻——Gate 仍渲染
    // 本页，失败信封可达（M9-1）。
    setToken(trimmed);
    try {
      // 凭据校验 = 用 read 面最便宜的列表请求打一发真实鉴权。
      await listApps();
      // 校验通过才真正持久化登录态。
      loginWithToken(trimmed);
      navigate(from ?? "/", { replace: true });
    } catch (err) {
      // 失败：清除未通过校验的凭据（401 路径 handleUnauthorized 已清，
      // 这里兜底网络/5xx 形态），并压掉 Gate 级 401 横幅——本页的信封
      // （含 suggestion）是唯一展示面。
      clearToken();
      onAuthErrorSeen?.();
      setError({
        message: `Sign-in failed: ${err instanceof Error ? err.message : String(err)}`,
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
          <>
            Sign in with your email and password. For CLI and automation, use
            the <span className="font-medium">API token</span> option below.
          </>
        ) : (
          <>
            Create the first account of this fleet, or join while registration
            is open. You are signed in automatically.
          </>
        )
      }
    >
      {mode === "signin" ? (
        <>
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
          {/* token 折叠区与登录表单平级（form 嵌套非法——HTML 解析器会吞
              内层 form 标签，token 提交会误触口令表单）。 */}
          <TokenSection
            show={showToken}
            onToggle={() => setShowToken((v) => !v)}
            token={token}
            onTokenChange={setTokenInput}
            busy={busy}
            onSubmit={submitToken}
          />
        </>
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

/** 折叠高级区：API token 粘贴登录（v0.1 运维直连路径保留）。 */
function TokenSection({
  show,
  onToggle,
  token,
  onTokenChange,
  busy,
  onSubmit,
}: {
  show: boolean;
  onToggle: () => void;
  token: string;
  onTokenChange: (v: string) => void;
  busy: boolean;
  onSubmit: (e: FormEvent) => void;
}) {
  return (
    <div className="border-t pt-3">
      <Button
        type="button"
        variant="ghost"
        size="sm"
        className="w-full justify-between text-muted-foreground"
        data-testid="login-token-toggle"
        aria-expanded={show}
        onClick={onToggle}
      >
        <span className="flex items-center gap-2">
          <KeyRound aria-hidden className="h-4 w-4" />
          API token (advanced)
        </span>
        <ChevronDown
          aria-hidden
          className={cn("h-4 w-4 transition-transform", show && "rotate-180")}
        />
      </Button>
      {show ? (
        <form className="mt-2 space-y-3" onSubmit={onSubmit}>
          <div className="space-y-2">
            <Label htmlFor="token">API token</Label>
            <Input
              id="token"
              name="token"
              type="password"
              placeholder="flt_…"
              autoComplete="off"
              data-testid="login-token"
              value={token}
              onChange={(e) => onTokenChange(e.target.value)}
            />
          </div>
          <Button
            type="submit"
            variant="outline"
            className="w-full"
            data-testid="login-token-submit"
            disabled={!token.trim() || busy}
          >
            Sign in with token
          </Button>
          <p className="text-xs text-muted-foreground">
            Paste a token created with{" "}
            <code className="rounded bg-muted px-1 py-0.5">
              fleetly tokens create --scopes admin
            </code>
            .
          </p>
        </form>
      ) : null}
    </div>
  );
}
