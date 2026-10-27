import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowRightIcon, CheckCircle2Icon, FingerprintIcon } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { toast } from "sonner";
import { z } from "zod";
import { useCommand } from "@lynx-go/uikit-core";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { describeApiError } from "@/kit/errors";
import { githubAppsResource } from "@/kit/githubapps";
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
// 批2：consume/complete 走 kit/githubapps.ts commands——回跳页有整页错误
// 态（ErrorState）在场，rejected/conflict/failure 都折算进页面呈现不走
// toast（结果盒在场，toast 单源的例外分支）；流态解码挪 kit
// decodeFlowStateProject。

// GitHub 以纯 query 追加回调参数,TanStack Router 缺省会 JSON.parse 值——
// 数字形态的 installation_id 落成 number。一律宽容收下再转 string。
const searchString = z.union([z.string(), z.number()]).transform(String);
const connectSearch = z.object({
  code: searchString.optional(),
  state: searchString.optional(),
  installation_id: searchString.optional(),
  setup_action: z.string().optional(),
});

// setup 回调不带 state(GitHub 实证:setup_url 只追加 installation_id 与
// setup_action)——签名态由 consume 成功时落入 localStorage,phase B 本地
// 恢复。签名态可读不可伪造,落浏览器无泄;complete 成功即清除。
const flowStateKey = "fleetly.gh-manifest-flow-state";

export const Route = createFileRoute("/_shell/github-apps/connect")({
  validateSearch: connectSearch,
  component: ConnectPage,
});

// 结果/失败两通道折算为 ErrorState 可呈现形态：kernel ApiError 非 Error
// 实例（describeApiError 先摊平），rejected/conflict 用 message 原句。
function commandPageError<P>(command: { failure: ApiError | null; result: CommandResult<P> | null }): Error | null {
  if (command.failure != null) return new Error(describeApiError(command.failure));
  if (command.result != null && command.result.status !== "ok") return new Error(command.result.message);
  return null;
}

function ConnectPage() {
  const { code, state, installation_id } = Route.useSearch();
  const navigate = useNavigate();
  const [installUrl, setInstallUrl] = useState<string | null>(null);
  const [appName, setAppName] = useState<string | null>(null);

  // ok 副作用：签名态落本地（phase B 恢复面）+ 安装页 URL/应用名进呈现；
  // 非 ok 分支不落任何态，页面错误态由 commandPageError 折算。
  const consume = useCommand(githubAppsResource.commands.manifestConsume, {
    onSettled: (result, failure) => {
      if (failure != null || result == null || result.status !== "ok") return;
      if (state != null && state !== "") {
        try {
          localStorage.setItem(flowStateKey, state);
        } catch {
          // 存不进也不挡流程——setup 回调同 tab 打开,常态都在
        }
      }
      setInstallUrl(result.payload.install_url ?? "");
      setAppName(result.payload.app_name ?? "");
    },
  });
  const consumeInput = { code: code ?? "", state: state ?? "" };

  // phase B 的流态:setup 回调缺 state 时取 consume 时存的本地副本。
  const resolveFlowState = (): string => {
    if (state != null && state !== "") return state;
    try {
      return localStorage.getItem(flowStateKey) ?? "";
    } catch {
      return "";
    }
  };

  // ok 副作用：清本地流态 + toast + 回对应清单页（project 行回 Registry
  // 第三 tab，global 行回管理页）；落行失效（双语境集合 + repos 子查询）
  // 由命令声明承载。
  const complete = useCommand(githubAppsResource.commands.manifestComplete, {
    onSettled: (result, failure) => {
      if (failure != null || result == null || result.status !== "ok") return;
      try {
        localStorage.removeItem(flowStateKey);
      } catch {
        // 清理尽力而为
      }
      toast.success(`GitHub App added — owner ${result.payload.app?.account_login ?? "?"} resolved from the installation`);
      const pid = result.payload.app?.project_id;
      if (pid != null && pid !== "") {
        void navigate({ to: "/p/$projectId/registry", params: { projectId: pid }, search: { tab: "github" } });
      } else {
        void navigate({ to: "/github-apps" });
      }
    },
  });
  const completeInput = { state: resolveFlowState(), installationId: installation_id ?? "" };

  // 命令不会自跑——落地即自动兑换/落行（ref 挡 StrictMode 双发；code 兑
  // 换在 GitHub 侧一次性，服务端对已耗 code 做流内幂等重放，刷新页面安
  // 全）。
  const firedRef = useRef(false);
  useEffect(() => {
    if (firedRef.current) return;
    if (installation_id != null && installation_id !== "") {
      firedRef.current = true;
      void complete.mutate(completeInput);
    } else if (code != null && code !== "" && state != null && state !== "") {
      firedRef.current = true;
      void consume.mutate(consumeInput);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 挂载即判段触发一次，参数由路由 search 固定
  }, []);

  const consumeError = commandPageError(consume);
  const completeError = commandPageError(complete);

  // phase B：安装完成回跳（setup_url 追加 installation_id；state 由本地恢复）。
  if (installation_id != null && installation_id !== "") {
    return (
      <ConnectFrame>
        {complete.pending ? (
          <p className="flex items-center gap-2 text-sm text-muted">
            <FingerprintIcon className="size-4" /> Verifying the installation and landing the row…
          </p>
        ) : completeError != null ? (
          <ErrorState error={completeError} onRetry={() => void complete.mutate(completeInput)} />
        ) : null}
      </ConnectFrame>
    );
  }

  // phase A：code 兑换。
  if (code != null && code !== "") {
    return (
      <ConnectFrame>
        {consume.pending ? (
          <p className="text-sm text-muted">Exchanging the creation code…</p>
        ) : consumeError != null ? (
          <ErrorState error={consumeError} onRetry={() => void consume.mutate(consumeInput)} />
        ) : (
          <div className="flex flex-col items-start gap-3">
            <p className="flex items-center gap-2 text-sm">
              <CheckCircle2Icon className="size-4 text-[var(--status-success)]" />
              App <span className="font-semibold">{appName}</span> created — the key was delivered to fleetly (sealed, never
              shown).
            </p>
            <p className="text-xs text-muted">
              Step 2 of 3 — install it on your account and pick the repositories it may read.
            </p>
            <Button onClick={() => installUrl != null && (window.location.href = installUrl)}>
              <FingerprintIcon data-icon-start-inline />
              Continue to GitHub — install the app
              <ArrowRightIcon />
            </Button>
            <p className="text-2xs text-muted">
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
