// 创建应用对话框（2026-09-25 审查 P0-1「全站没有创建应用入口」）：项目详情
// 页 Applications 卡 CTA 的落点。Deploy RPC 是 upsert 语义（REST Deploy 带
// app=<新名> + project=<team/prj>，服务端 ensureApp 随首次部署建应用行，
// internal/api/deployments.go）——Console 的「创建应用」即一次带归属声明的
// deploy 入队，不新增端点。
//
// name 语义与服务端 A1 一致性校验对齐（deployments.go Deploy）：
//   - compose 顶层已有 `name:` 行 → 以 compose 为准（回填名字段并说明）；
//   - compose 缺 name → 把 `name: <值>` 前置注入后发送；
//   两者冲突以 compose 为准并回填——请求 app 与 compose name 错位会被服务端
//   以 E_COMPOSE_UNSUPPORTED 拒绝（可预测的 400），前置对齐避免它。
//
// 名字校验与 internal/compose/validate.go specNamePattern 一致：
// ^[a-z0-9][a-z0-9_-]*$（小写字母/数字开头，仅小写字母/数字/-/_）。保留字
// 校验已随 v0.3 命名三段化退役（app 名不再邻 fleetly- 前缀），服务端其余
// 违约（E_COMPOSE_*）如实走错误信封呈现。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";

import { deploy } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

/** 与 internal/compose/validate.go specNamePattern 逐字一致。 */
const APP_NAME_PATTERN = /^[a-z0-9][a-z0-9_-]*$/;

/**
 * 解析 compose 顶层 `name:` 行。按行首（无缩进）匹配——YAML 顶层键不出现在
 * 缩进层级，服务内的 name: 键带前导空白不会误中；行内注释剥离（YAML 的
 * 注释起点是「空白 + #」，`api#1` 这类无空白紧邻的 # 不是注释；引号内含
 * # 的形态不在本启发式的处理域——这是表单回填的便利启发式，权威解析在
 * 服务端受控子集校验，UI 只需不再把 `name: myapi # prod` 这类合法输入
 * fail-closed 卡在名字校验）。
 */
export function parseComposeName(text: string): string | null {
  for (const line of text.split(/\r?\n/)) {
    const m = /^name\s*:\s*(.*?)\s*$/.exec(line);
    if (m) {
      const v = m[1]
        .replace(/(^|\s)#.*$/, "")
        .trim()
        .replace(/^["']|["']$/g, "");
      return v || null;
    }
  }
  return null;
}

export function CreateAppDialog({
  open,
  onOpenChange,
  projectRef,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  /** 目标项目限定形 team/prj（DeployRequest.project 透传；对话框显式展示）。 */
  projectRef: string;
}) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [composeText, setComposeText] = useState("");
  const [error, setError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);

  // compose 顶层 name 实时解析：有则以 compose 为准（回填名字段），无则用
  // 名字段注入（提交时前置 `name: <值>` 行）。
  const composeName = parseComposeName(composeText);
  const effectiveName = composeName ?? name.trim();
  const nameOk = APP_NAME_PATTERN.test(effectiveName);

  const deployMutation = useMutation({
    mutationFn: () => {
      const finalCompose = composeName
        ? composeText
        : `name: ${name.trim()}\n${composeText}`;
      return deploy(effectiveName, finalCompose, { project: projectRef || undefined });
    },
    onSuccess: (resp) => {
      setError(null);
      setName("");
      setComposeText("");
      void queryClient.invalidateQueries({ queryKey: ["apps"] });
      onOpenChange(false);
      // DeployResponse 只带 deployment_id/app 名/status/warnings（生成类型
      // 投影，无应用平台 id 字段）——按裁决回退到 /apps 列表并携带排队
      // 说明（AppsPage 消费 location.state 渲染 queued 通知条）。
      navigate("/apps", {
        state: {
          deployQueued: {
            app: resp.app ?? effectiveName,
            deploymentId: resp.deployment_id ?? "",
          },
        },
      });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onComposeChange(text: string) {
    setComposeText(text);
    // compose 顶层 name 出现/变化即回填名字段（冲突以 compose 为准——与
    // 服务端 A1 一致性口径对齐，避免可预测的 400）。
    const parsed = parseComposeName(text);
    if (parsed) setName(parsed);
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (nameOk && composeText.trim()) deployMutation.mutate();
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="app-create-dialog">
        <DialogHeader>
          <DialogTitle>Deploy new application</DialogTitle>
          <DialogDescription>
            The app is created on first deploy: the compose YAML is queued into
            the target project and the deployment can be tracked right after
            (status queued).
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={onSubmit}>
          <div className="space-y-1.5">
            <Label>Target project</Label>
            <p
              data-testid="app-target-project"
              className="font-mono text-xs text-muted-foreground"
            >
              {projectRef || "default (your personal team)"}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="app-create-name">Application name</Label>
            <Input
              id="app-create-name"
              data-testid="app-create-name-input"
              className="font-mono text-xs"
              placeholder="my-api"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              lowercase letters, digits, - and _ (must start alphanumeric).
              {composeName
                ? " The compose file declares the top-level name — it wins and the field above follows it."
                : " If the compose file declares a top-level name, it wins."}
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="app-create-compose">Compose YAML</Label>
            <Textarea
              id="app-create-compose"
              data-testid="app-create-compose-input"
              aria-label="Compose YAML"
              className="min-h-[180px] font-mono text-xs"
              placeholder={"services:\n  web:\n    image: nginx:1.27-alpine\n    ports:\n      - 8080:80"}
              value={composeText}
              onChange={(e) => onComposeChange(e.target.value)}
            />
            {!composeName && name.trim() && composeText.trim() ? (
              <p className="text-xs text-muted-foreground" data-testid="app-compose-name-note">
                The compose file has no top-level name — <code>name: {name.trim()}</code> is
                prepended on submit.
              </p>
            ) : null}
          </div>
          {error ? (
            <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} docs={error.docs} />
          ) : null}
          <DialogFooter>
            <Button
              type="submit"
              data-testid="app-create-submit"
              disabled={!nameOk || !composeText.trim() || deployMutation.isPending}
            >
              {deployMutation.isPending ? (
                <Loader2 aria-hidden className="h-4 w-4 animate-spin" />
              ) : null}
              Deploy
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
