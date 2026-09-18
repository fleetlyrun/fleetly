// 登录页：token 粘贴表单（v0.1 单操作员口径，无 OAuth）。提交后以
// GET /v1/apps 验证凭据（read scope 即可），401 → 渲染错误信封
// （code + message + suggestion），成功 → 进入来源页或应用列表。

import { AlertCircle, Loader2 } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { listApps } from "@/api/endpoints";
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
      // 已持有有效 token（如从 401 回退）直达列表。
      navigate("/apps", { replace: true });
    }
  }, [authed, error, navigate]);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    onAuthErrorSeen?.();
    if (!token.trim() || busy) return;
    setBusy(true);
    setError(null);
    login(token);
    try {
      // 凭据校验 = 用 read 面最便宜的列表请求打一发真实鉴权。
      await listApps();
      navigate(from ?? "/apps", { replace: true });
    } catch (err) {
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
          <CardTitle className="text-xl">fleetly console</CardTitle>
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
                className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900"
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
