// join 向导卡片（E1-8，multi-node §2.3/D-MN-13）：复制 join 命令与 token、
// 按节点 IP 生成的防火墙放行规则、DNS 步骤与完成判据。诚实契约：平台只
// 生成规则文本、绝不自动应用用户防火墙；base_domain 缺失时服务端以
// E_MULTI_NODE_REQUIRES_BASE_DOMAIN（409）拒绝，信封原样呈现。
// Rotate join token（backlog #4-③）：轮换 worker join token——服务端语义
//（internal/api/system.go RotateJoinToken）为旧 token 即刻失效，两步确认
// 后执行；已生成的指引自动重取以显示新 token。

import { useMutation } from "@tanstack/react-query";
import { Check, Copy, Network } from "lucide-react";
import { useState } from "react";

import { getJoinGuide, rotateJoinToken } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import { EnvelopeAlert, EnvelopeAlertFrom } from "@/components/envelope-alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // 剪贴板不可用（无 HTTPS/权限）：如实回退为未复制状态，命令文本
          // 本身已完整可见、可手动复制。
          setCopied(false);
        }
      }}
      aria-label={label}
    >
      {copied ? <Check aria-hidden className="h-3.5 w-3.5" /> : <Copy aria-hidden className="h-3.5 w-3.5" />}
      {copied ? "Copied" : "Copy"}
    </Button>
  );
}

export function JoinWizard() {
  const [workerIp, setWorkerIp] = useState("");
  const [rotateOpen, setRotateOpen] = useState(false);
  const [rotateError, setRotateError] = useState<ReturnType<typeof errorEnvelopeFrom> | null>(null);
  const guide = useMutation({
    mutationFn: () => getJoinGuide(workerIp.trim() || undefined),
  });
  const g = guide.data?.guide;
  const err = guide.error ? errorEnvelopeFrom(guide.error) : null;

  // 轮换（系统面动作——服务端 requirePlatformWriteFace 硬门，平台管理员可
  // 执行；沿 SystemPage 既有卡片形态不另设前端角色门，403 信封照实展示）。
  // role 固定 worker（指引只消费 worker token）。成功后已生成的指引立即
  // 重取（join 命令/token 显示新值）；未生成过则无可刷新，下次 Generate
  // 自然拿到新 token。
  const rotate = useMutation({
    mutationFn: () => rotateJoinToken("worker"),
    onSuccess: () => {
      setRotateError(null);
      setRotateOpen(false);
      if (guide.data) guide.mutate();
    },
    onError: (err) => setRotateError(errorEnvelopeFrom(err)),
  });

  return (
    <Card>
      <CardHeader className="flex-row items-center gap-2 space-y-0 border-b pb-3">
        <Network aria-hidden className="h-4 w-4 text-muted-foreground" />
        <CardTitle className="text-sm font-semibold">Add node</CardTitle>
        <CardDescription className="ml-auto text-xs">
          Join a worker node — Docker Engine only, zero fleetly installables.
        </CardDescription>
      </CardHeader>
      <CardContent className="pt-4">
        <div className="flex flex-wrap items-center gap-2" data-testid="join-wizard">
          <Input
            className="h-8 w-56 text-xs"
            placeholder="worker public IP (optional)"
            value={workerIp}
            onChange={(e) => setWorkerIp(e.target.value)}
            aria-label="Worker public IP"
          />
          <Button
            size="sm"
            disabled={guide.isPending}
            onClick={() => guide.mutate()}
          >
            {guide.isPending ? "Generating…" : "Generate join guide"}
          </Button>
          <Button
            variant="outline"
            size="sm"
            data-testid="join-token-rotate"
            disabled={rotate.isPending}
            onClick={() => {
              setRotateError(null);
              setRotateOpen(true);
            }}
          >
            Rotate join token
          </Button>
        </div>

        {err ? (
          <div className="mt-3">
            <EnvelopeAlertFrom envelope={err} />
          </div>
        ) : null}

        {/* 轮换两步确认（无需输名字——后果说明 + 显式确认即两步纪律形态；
            服务端语义：旧 token 即刻失效，已加入节点不受影响）。 */}
        {rotateOpen ? (
          <Dialog open onOpenChange={(v) => (v ? undefined : setRotateOpen(false))}>
            <DialogContent data-testid="join-token-rotate-dialog">
              <DialogHeader>
                <DialogTitle>Rotate join token</DialogTitle>
                <DialogDescription>
                  The worker join token is replaced immediately: any join
                  command or token copied earlier stops working, and no new
                  node can join with it. Nodes that already joined the swarm
                  are not affected. A generated join guide below is refreshed
                  automatically so you can re-copy the new command.
                </DialogDescription>
              </DialogHeader>
              {rotateError ? (
                <EnvelopeAlert
                  code={rotateError.code}
                  message={rotateError.message}
                  suggestion={rotateError.suggestion}
                />
              ) : null}
              <DialogFooter>
                <Button
                  variant="destructive"
                  data-testid="join-token-rotate-submit"
                  disabled={rotate.isPending}
                  onClick={() => rotate.mutate()}
                >
                  {rotate.isPending ? "Rotating…" : "Rotate token"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        ) : null}

        {g ? (
          <div className="mt-4 space-y-4 text-sm" data-testid="join-wizard-guide">
            <div>
              <div className="mb-1 text-xs font-medium text-muted-foreground">
                1. Join command (run on the worker)
              </div>
              <div className="flex items-start gap-2">
                <code
                  className="min-w-0 flex-1 break-all rounded-md border bg-muted/40 p-2 font-mono text-xs"
                  data-testid="join-wizard-command"
                >
                  {g.join_command}
                </code>
                <CopyButton value={g.join_command ?? ""} label="Copy join command" />
              </div>
            </div>

            <div>
              <div className="mb-1 text-xs font-medium text-muted-foreground">
                2. Worker join token (admin material — rotate after joining)
              </div>
              <div className="flex items-start gap-2">
                <code
                  className="min-w-0 flex-1 break-all rounded-md border bg-muted/40 p-2 font-mono text-xs"
                  data-testid="join-wizard-token"
                >
                  {g.worker_token}
                </code>
                <CopyButton value={g.worker_token ?? ""} label="Copy join token" />
              </div>
            </div>

            <div>
              <div className="mb-1 text-xs font-medium text-muted-foreground">
                3. Firewall rules — generated text only, the platform never applies them
              </div>
              <div className="space-y-1">
                {[...(g.manager_firewall_rules ?? []), ...(g.worker_firewall_rules ?? [])].map(
                  (r, i) => (
                    <div
                      key={i}
                      className="flex flex-wrap items-center gap-2 rounded-md border p-2 text-xs"
                    >
                      <Badge variant="outline" className="font-normal">
                        {r.side}
                      </Badge>
                      <code className="font-mono">{r.port}</code>
                      <span className="text-muted-foreground">{r.purpose}</span>
                      <code className="w-full break-all font-mono text-muted-foreground">
                        {r.rule}
                      </code>
                    </div>
                  ),
                )}
              </div>
            </div>

            <div>
              <div className="mb-1 text-xs font-medium text-muted-foreground">4. DNS steps</div>
              <ul className="list-disc space-y-1 pl-5 text-xs">
                {(g.dns_steps ?? []).map((s, i) => (
                  <li key={i}>{s}</li>
                ))}
              </ul>
            </div>

            <div>
              <div className="mb-1 text-xs font-medium text-muted-foreground">
                5. Completion checks (the wizard advances automatically)
              </div>
              <ul className="list-disc space-y-1 pl-5 text-xs">
                {(g.completion_checks ?? []).map((s, i) => (
                  <li key={i}>{s}</li>
                ))}
              </ul>
            </div>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
