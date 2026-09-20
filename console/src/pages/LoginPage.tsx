// 登录页：token 粘贴表单（v0.1 单操作员口径，无 OAuth）。提交后以
// GET /v1/apps 验证凭据（read scope 即可），401 → 渲染错误信封
// （code + message + suggestion），成功 → 进入来源页或应用列表。
// 时序纪律（M9-1）：先校验后持久化——校验期间 token 只进 API 层的
// localStorage（listApps 需要 Bearer），auth 状态不翻转（Gate 不卸载
// 本页）；校验通过才 login() 持久化 + 进入，失败则清除凭据并停留展示
// 错误信封（修复前 authed 先翻 → LoginPage 被 Gate 卸载，catch 的
// setError 永不可达，且无效 token 已持久化）。

import { AlertCircle, Loader2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { listApps } from "@/api/endpoints";
import { clearToken, setToken } from "@/api/client";
import { errorEnvelopeFrom } from "@/api/errors";
import { useAuth } from "@/auth";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function LoginPage({
  authError,
  onAuthErrorSeen,
}: {
  authError?: string;
  onAuthErrorSeen?: () => void;
} = {}) {
  const { login, authed } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [token, setTokenInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<{ message: string; envelope: ReturnType<typeof errorEnvelopeFrom> } | null>(null);

  const from = (location.state as { from?: string } | null)?.from;

  useEffect(() => {
    if (authed && !error) {
      // 已持有有效 token（如从 401 回退）直达首页。
      navigate("/", { replace: true });
    }
  }, [authed, error, navigate]);

  async function onSubmit(e: FormEvent) {
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
      // 校验通过才真正持久化登录态（login 幂等地再写一次 token 并翻
      // authed；同一批次内 Gate 切换到应用路由，本页随之卸载）。
      login(trimmed);
      navigate(from ?? "/", { replace: true });
    } catch (err) {
      // 失败：清除未通过校验的凭据（401 路径 handleUnauthorized 已清，
      // 这里兜底网络/5xx 形态），并压掉 Gate 级 401 横幅——本页的信封
      // （含 suggestion）是唯一展示面。
      clearToken();
      onAuthErrorSeen?.();
      setError({
        message: err instanceof Error ? err.message : String(err),
        envelope: errorEnvelopeFrom(err),
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/30 p-6">
      <Card className="w-full max-w-md">
        <CardHeader>
          <div className="mb-1 flex items-center gap-2.5">
            <span className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
              f
            </span>
            <div className="leading-tight">
              <CardTitle className="text-base">fleetly</CardTitle>
              <span className="text-[10px] uppercase tracking-widest text-muted-foreground">
                console
              </span>
            </div>
          </div>
          <CardDescription>
            Paste an API token to sign in. Create one with{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              fleetly tokens create --scopes admin
            </code>
            .
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={onSubmit}>
            <div className="space-y-2">
              <Label htmlFor="token">API token</Label>
              <Input
                id="token"
                name="token"
                type="password"
                placeholder="flt_…"
                autoComplete="off"
                autoFocus
                value={token}
                onChange={(e) => setTokenInput(e.target.value)}
              />
            </div>
            {authError ? (
              <div
                role="alert"
                className="flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-sm text-amber-800 dark:text-amber-300"
              >
                <AlertCircle aria-hidden className="mt-0.5 h-4 w-4 shrink-0" />
                {authError}
              </div>
            ) : null}
            {error ? (
              <EnvelopeAlert
                code={error.envelope.code}
                message={`Sign-in failed: ${error.message}`}
                suggestion={
                  error.envelope.suggestion ??
                  "Check that the token is valid and has at least read scope."
                }
              />
            ) : null}
            <Button type="submit" className="w-full" disabled={!token.trim() || busy}>
              {busy ? <Loader2 aria-hidden className="h-4 w-4 animate-spin" /> : null}
              Sign in
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
