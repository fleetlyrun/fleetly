// 邀请链接页（/auth/invite?token=…，设计 §3.1/§7 两分支）：
// - 已登录：直接 POST /v1/auth/invite:accept 消费一次性邀请（成功展示入队
//   结果；无效/过期/已消费的 409 信封如实展示——EnvelopeAlert 全字段，不
//   美化不吞码）。accept 是消费性动作，effect 以 ref 防重（StrictMode 双
//   触发不得二次 POST）。
// - 未登录：提示先登录/注册——「登录后 accept」由登录页的 from 暂存回跳
//   本页实现（登录成功 navigate(from) → 本页 authed 分支自动 accept）。
//   受邀注册不受注册窗管辖（P1-1 服务端契约：携带 invite_token 的注册豁免
//   窗口判定）——「Create an account」恒显（2026-09-25 审查顺手项，此前被
//   registrationOpen 门控：关窗时受邀新用户在 Console 无路可走）。
// 注意：邀请的产生面（TeamsService.CreateInvite）是 W2——W1 期间无合法
// token 可签发，本页对任意 token 的 409 均如实报错（本票验收口径）。

import { CheckCircle2, Loader2, UserPlus } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";

import { acceptInvite } from "@/api/endpoints";
import type { AcceptInviteResponse } from "@/api/types";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import { useAuth } from "@/auth";
import { AuthCard } from "@/components/auth-card";
import { EnvelopeAlertFrom } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";

type InvitePhase = "idle" | "busy" | "ok" | "error";

export function InvitePage() {
  const { authed } = useAuth();
  const [searchParams] = useSearchParams();
  const location = useLocation();
  const token = searchParams.get("token") ?? "";

  const [phase, setPhase] = useState<InvitePhase>("idle");
  const [result, setResult] = useState<AcceptInviteResponse | null>(null);
  const [failure, setFailure] = useState<ErrorEnvelope | null>(null);
  // 一次性消费防重：accept 每次挂载至多触发一次。
  const attempted = useRef(false);

  useEffect(() => {
    if (!authed || !token || attempted.current) return;
    attempted.current = true;
    setPhase("busy");
    acceptInvite(token)
      .then((res) => {
        setResult(res);
        setPhase("ok");
      })
      .catch((err: unknown) => {
        setFailure(errorEnvelopeFrom(err));
        setPhase("error");
      });
  }, [authed, token]);

  // 登录页回跳链路：携带完整邀请 URL（含 token 查询参数）作 from。
  const inviteUrl = `${location.pathname}${location.search}`;

  return (
    <AuthCard
      description={
        <>
          Team invitations in fleetly are one-time links. This page applies an
          invitation to your account.
        </>
      }
    >
      {!token ? (
        <div className="space-y-4" data-testid="invite-missing-token">
          <p className="text-sm text-muted-foreground">
            This invite link is missing its token. Ask your team owner for a
            fresh link.
          </p>
          <Button asChild variant="outline" className="w-full">
            <Link to="/">Go to fleetly</Link>
          </Button>
        </div>
      ) : !authed ? (
        <div className="space-y-4" data-testid="invite-anon">
          <div className="flex items-start gap-2.5">
            <UserPlus aria-hidden className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              You have been invited to join a team. Sign in first — the
              invitation will be applied right after.
            </p>
          </div>
          <Button asChild className="w-full" data-testid="invite-signin">
            <Link to="/login" state={{ from: inviteUrl }}>
              Sign in to accept
            </Link>
          </Button>
          {/* 受邀注册恒显（顺手项）：不查注册窗、不受其管辖——登录页感知
              from 里的邀请 token 后首屏即受邀注册表单（P1-1 机制）。 */}
          <p className="text-center text-xs text-muted-foreground">
            New here?{" "}
            <Link
              to="/login"
              state={{ from: inviteUrl }}
              data-testid="invite-register"
              className="font-medium underline underline-offset-2"
            >
              Create an account
            </Link>{" "}
            from the sign-in page.
          </p>
        </div>
      ) : phase === "ok" && result ? (
        <div className="space-y-4" data-testid="invite-ok">
          <div className="flex items-start gap-2.5">
            <CheckCircle2 aria-hidden className="mt-0.5 h-5 w-5 shrink-0 text-emerald-500" />
            <p className="text-sm">
              You joined{" "}
              <span className="font-semibold">
                {result.team_name || result.team_slug}
              </span>{" "}
              as <span className="font-semibold">{result.role || "member"}</span>.
            </p>
          </div>
          <Button asChild className="w-full">
            <Link to="/apps">Go to applications</Link>
          </Button>
        </div>
      ) : phase === "error" ? (
        <div className="space-y-4" data-testid="invite-error">
          <EnvelopeAlertFrom envelope={failure ?? {}} />
          <Button asChild variant="outline" className="w-full">
            <Link to="/">Go to fleetly</Link>
          </Button>
        </div>
      ) : (
        <div
          className="flex items-center gap-2 text-sm text-muted-foreground"
          data-testid="invite-busy"
        >
          <Loader2 aria-hidden className="h-4 w-4 animate-spin" />
          Accepting invitation…
        </div>
      )}
    </AuthCard>
  );
}
