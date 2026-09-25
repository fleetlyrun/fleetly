// 登录页（v0.3 RBAC W1，设计 §7）：主形态 = 邮箱+口令（POST
// /v1/auth/login，成功经 Set-Cookie 下发会话）；注册入口由 GET
// /v1/auth/registration 驱动（open=true 或无用户窗口才出现），注册成功即
// 自动登录（Register 同事务下发 cookie）。**API token 登录面已移除**
// （2026-09-25 用户裁决：Console 只走会话 cookie；CLI/CI 的 token 体系
// 不受影响——PAT 在「用户菜单 → API tokens」页创建管理）。
//
// 受邀注册（P1-1，2026-09-25 审查 docs/reports/2026-09-25-console-ui-review.md
// §3；W3-S4 服务端契约 = internal/state/register.go RegisterWrite.InviteToken）：
// from 暂存的是邀请链接（InvitePage 引导登录时把 /auth/invite?token=… 整条
// 塞进 from）时，携带 invite_token 的注册**豁免注册窗**（有效邀请即平台侧
// 准入决定）且同事务消费邀请入队——注册表单不受 registration 门控、且为
// 首屏主形态（登录为次链接），文案区分受邀形态。受邀注册已在注册事务内
// 消费邀请，成功后直达控制台首页（回跳邀请 URL 只会二次 accept → 409）。
//
// 时序纪律（M9-1 沿用）：loginWithPassword 成功（服务端已下发 cookie）才
// 翻转 authed；错口令的 401 是表单业务结果（optionalAuth 豁免全局 401
// 处置），唯一展示面是本页信封——错误态收敛到提交动作自身（P1-2 顺带，
// 全局 401 不再向本页注入告警条）。成功进入后 navigate(from ?? "/")——
// 来源页（深链/邀请链接）恢复由 anon 门卫的 from 暂存承担。

import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
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
import { consumeSignedOut } from "@/lib/signed-out";

type AuthMode = "signin" | "register";

/**
 * 从 from 暂存里解析受邀注册上下文（P1-1）：InvitePage 引导登录时把完整
 * 邀请 URL（/auth/invite?token=…）塞进路由 state.from——命中邀请路径且带
 * token 即受邀注册；其余 from（深链/无）返回空串（原注册门控语义不变）。
 */
function inviteTokenFromRedirect(from: string | undefined): string {
  if (!from || !from.startsWith("/")) return "";
  try {
    const url = new URL(from, "http://fleetly.internal");
    if (url.pathname !== "/auth/invite") return "";
    return url.searchParams.get("token") ?? "";
  } catch {
    return "";
  }
}

interface PageError {
  message: string;
  envelope: ReturnType<typeof errorEnvelopeFrom>;
}

export function LoginPage() {
  const { loginWithPassword, registerWithPassword } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  // 中性登出提示（前序遗留补口）：全局 401 弹回（会话失效）在 sessionStorage
  // 置过一次性标志 → 渲染「You have been signed out.」muted 条；主动登出
  // 不置位、不渲染。挂载即读即清（useState 惰性初始化——重渲染不重复消费）。
  const [signedOut] = useState(() => consumeSignedOut());
  // 受邀注册上下文（P1-1）：from 是邀请链接（InvitePage 引导登录时塞入）
  // 时取其 token——注册表单豁免 registration 门控并为首屏主形态。
  const inviteToken = inviteTokenFromRedirect(
    (location.state as { from?: string } | null)?.from,
  );

  const [mode, setMode] = useState<AuthMode>(inviteToken ? "register" : "signin");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<PageError | null>(null);

  const from = (location.state as { from?: string } | null)?.from;

  // 注册入口开关（设计 §2.1/§7）：open=true 或 has_users=false（无用户窗口）
  // 才出现；查询失败按关闭处理（不闪入口，保守口径）。受邀上下文豁免本门
  // （P1-1：携带 invite_token 的注册不受注册窗管辖——服务端同事务现查邀请
  // 豁免判定，internal/state/register.go）。
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
  const registerEntryVisible = registrationOpen || inviteToken !== "";

  function switchMode(next: AuthMode) {
    setMode(next);
    setError(null);
  }

  async function submitPassword(e: FormEvent) {
    e.preventDefault();
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
          // 受邀注册：invite_token 随注册载荷上送（服务端同事务消费邀请、
          // 豁免注册窗——W3-S4 契约）。
          inviteToken: inviteToken || undefined,
        });
      }
      // 受邀注册已在注册事务内消费邀请——直达控制台首页（回跳邀请 URL 只会
      // 二次 accept 同一已消费 token → 409 E_INVITE_INVALID）；其余路径照旧
      // 回跳 from（登录后的邀请页 accept、深链恢复）。
      navigate(inviteToken && mode === "register" ? "/" : (from ?? "/"), {
        replace: true,
      });
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
        ) : inviteToken ? (
          // 受邀注册形态文案（P1-1）：与开放窗口注册区分——邀请即准入。
          <>
            You&apos;ve been invited to join a team. Create your account to
            accept the invitation — you are signed in and joined
            automatically.
          </>
        ) : (
          <>
            Create the first account of this fleet, or join while registration
            is open. You are signed in automatically.
          </>
        )
      }
    >
      {/* 中性登出提示（非告警、非信封）：仅全局 401 弹回链路渲染；主动
          登出（用户菜单 Sign out）不置标志、无此条。 */}
      {signedOut ? (
        <p
          className="mb-4 rounded-md border bg-muted/40 px-3 py-2 text-sm text-muted-foreground"
          data-testid="signed-out-note"
        >
          You have been signed out.
        </p>
      ) : null}
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
          {/* 错误态收敛到提交动作自身（P1-2 顺带排查）：登录页只展示本页
              表单提交的结果（错口令 401 等）——全局 401 处置（会话失效回
              登录页）不再向本页注入「Session invalid」告警条，自愿登出或
              陈旧资源请求的竞态 401 不得在登录页残留误导性告警。 */}
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
          {registerEntryVisible ? (
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
            {inviteToken ? "Create account and accept invite" : "Create account"}
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
