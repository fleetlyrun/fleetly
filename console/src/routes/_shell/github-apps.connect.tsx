import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowRightIcon, CheckCircle2Icon, FingerprintIcon } from "lucide-react";
import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import { z } from "zod";
import { apiSend } from "@/api/client";
import { ErrorState } from "@/components/domain/error-state";
import { PageFrame } from "@/components/domain/page-frame";
import { Button } from "@/components/ui/button";

// manifest 自动建流的 console 回调路由（ADR-0073 决策 8）：一个路由承载
// 两段回调——
//   phase A（redirect_url）：?code=<一次性>&state=<签名态> → consume 换凭
//     据，给安装页 URL（用户继续去 GitHub 勾仓库）；
//   phase B（setup_url）：?installation_id=<n>&setup_action=install&state=
//     <签名态>（GitHub 在 setup_url 上追加参数，start 时嵌入的 state 随
//     URL 存活）→ complete 落行（project_id 由 state 载荷解码——载荷可读
//     不可伪造，服务端复验）。
// 成功即回对应清单页（project 行回 Registry 第三 tab，global 行回本页）。

const connectSearch = z.object({
  code: z.string().optional(),
  state: z.string().optional(),
  installation_id: z.string().optional(),
  setup_action: z.string().optional(),
});

export const Route = createFileRoute("/_shell/github-apps/connect")({
  validateSearch: connectSearch,
  component: ConnectPage,
});

// stateProject 解码签名态的可读载荷（base64url(JSON)——服务端验签，本地
// 只读作用域用于回跳导航）。解码失败返回 null（走通用错误态）。
function stateProject(state: string): { projectId?: string } | null {
  try {
    const [body] = state.split(".");
    const json = JSON.parse(atob(body.replace(/-/g, "+").replace(/_/g, "/")));
    return { projectId: typeof json.p === "string" && json.p !== "" ? json.p : undefined };
  } catch {
    return null;
  }
}

function ConnectPage() {
  const { code, state, installation_id } = Route.useSearch();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [installUrl, setInstallUrl] = useState<string | null>(null);
  const [appName, setAppName] = useState<string | null>(null);

  const consume = useMutation({
    mutationFn: () =>
      apiSend<{ install_url?: string; app_name?: string }>("/v1/github-apps/manifest/consume", "POST", {
        code: code ?? "",
        state: state ?? "",
      }),
    onSuccess: (res) => {
      setInstallUrl(res.install_url ?? "");
      setAppName(res.app_name ?? "");
    },
  });

  const complete = useMutation({
    mutationFn: () => {
      const scope = state ? stateProject(state) : null;
      if (scope == null) throw new Error("the flow state could not be read — restart the flow");
      return apiSend<{ app?: { id?: string; project_id?: string; account_login?: string } }>(
        "/v1/github-apps/manifest/complete",
        "POST",
        { state: state ?? "", installation_id: Number(installation_id ?? 0), project_id: scope.projectId ?? "" },
      );
    },
    onSuccess: (res) => {
      toast.success(`GitHub App added — owner ${res.app?.account_login ?? "?"} resolved from the installation`);
      void queryClient.invalidateQueries({ queryKey: ["github-apps"] });
      const pid = res.app?.project_id;
      if (pid != null && pid !== "") {
        void navigate({ to: "/p/$projectId/registry", params: { projectId: pid }, search: { tab: "github" } });
      } else {
        void navigate({ to: "/github-apps" });
      }
    },
  });

  if (state == null || state === "") {
    return (
      <ConnectFrame>
        <ErrorState error={new Error("the callback carried no flow state — restart from the GitHub Apps page")} />
      </ConnectFrame>
    );
  }

  // phase B：安装完成回跳（setup_url 追加 installation_id）。
  if (installation_id != null && installation_id !== "") {
    return (
      <ConnectFrame>
        {complete.isPending ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <FingerprintIcon className="size-4" /> Verifying the installation and landing the row…
          </p>
        ) : complete.isError ? (
          <ErrorState error={complete.error} onRetry={() => void complete.mutate()} />
        ) : null}
      </ConnectFrame>
    );
  }

  // phase A：code 兑换。
  if (code != null && code !== "") {
    return (
      <ConnectFrame>
        {consume.isPending ? (
          <p className="text-sm text-muted-foreground">Exchanging the creation code…</p>
        ) : consume.isError ? (
          <ErrorState error={consume.error} onRetry={() => void consume.mutate()} />
        ) : (
          <div className="flex flex-col items-start gap-3">
            <p className="flex items-center gap-2 text-sm">
              <CheckCircle2Icon className="size-4 text-[var(--status-success)]" />
              App <span className="font-semibold">{appName}</span> created — the key was delivered to fleetly (sealed, never
              shown).
            </p>
            <p className="text-xs text-muted-foreground">
              Step 2 of 3 — install it on your account and pick the repositories it may read.
            </p>
            <Button onClick={() => installUrl != null && (window.location.href = installUrl)}>
              <FingerprintIcon data-icon-start-inline />
              Continue to GitHub — install the app
              <ArrowRightIcon />
            </Button>
            <p className="text-2xs text-muted-foreground">
              After installing, GitHub redirects right back here and the row lands automatically.
            </p>
          </div>
        )}
      </ConnectFrame>
    );
  }

  return (
    <ConnectFrame>
      <ErrorState error={new Error("unrecognised callback — restart from the GitHub Apps page")} />
    </ConnectFrame>
  );
}

function ConnectFrame({ children }: { children: ReactNode }) {
  return (
    <PageFrame
      title="GitHub App connection"
      description="Private-repo access material flow — the GitHub Apps list lives under Platform · Fleet (and Registry · GitHub Apps per project)"
    >
      {children}
    </PageFrame>
  );
}
