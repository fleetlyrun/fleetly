// 错误信封渲染：fleetly.shared.v1.ErrorResponse 的 Console 呈现——必须
// 展示 code + message + suggestion（票面纪律：不允许退化为裸 toast）。
// 两个消费面：
//   1. HTTP 错误（ApiError.envelope / StreamError.envelope）——完整七字段
//      信封；
//   2. 部署历史失败行——行投影只有 error_code/verdict/recovery，按同视觉
//      形态构造（verdict 作 message、recovery 作 suggestion，docs 按错误
//      码文档锚点规则拼出），与 HTTP 信封错误同一视觉语言。

import { AlertTriangle, ExternalLink } from "lucide-react";

import { cn } from "@/lib/utils";
import type { ErrorEnvelope } from "@/api/errors";

export function EnvelopeAlert({
  code,
  message,
  suggestion,
  docs,
  stage,
  deploymentId,
  className,
}: {
  code?: string;
  message?: string;
  suggestion?: string;
  docs?: string;
  stage?: string;
  deploymentId?: string;
  className?: string;
}) {
  const docUrl = docs || (code ? `https://docs.fleetly.dev/errors/${code}` : "");
  return (
    <div
      role="alert"
      data-testid="error-envelope"
      className={cn(
        "rounded-md border border-red-300 bg-red-50 p-3 text-sm text-red-900",
        className,
      )}
    >
      <div className="flex items-start gap-2">
        <AlertTriangle aria-hidden className="mt-0.5 h-4 w-4 shrink-0 text-red-600" />
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            {code ? (
              <code className="rounded bg-red-100 px-1.5 py-0.5 font-mono text-xs font-bold text-red-800">
                {code}
              </code>
            ) : (
              <span className="text-xs font-semibold uppercase tracking-wide text-red-700">
                error
              </span>
            )}
            {stage ? (
              <span className="text-xs text-red-700">stage: {stage}</span>
            ) : null}
            {deploymentId ? (
              <span className="font-mono text-xs text-red-700">
                {deploymentId}
              </span>
            ) : null}
          </div>
          {message ? <div className="break-words">{message}</div> : null}
          {suggestion ? (
            <div className="break-words text-red-800">
              <span className="font-semibold">Suggestion: </span>
              {suggestion}
            </div>
          ) : null}
          {docUrl ? (
            <a
              href={docUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-red-700 underline underline-offset-2"
            >
              docs <ExternalLink aria-hidden className="h-3 w-3" />
            </a>
          ) : null}
        </div>
      </div>
    </div>
  );
}

/** 从完整信封对象渲染（HTTP 错误路径）。 */
export function EnvelopeAlertFrom({
  envelope,
  className,
}: {
  envelope: ErrorEnvelope;
  className?: string;
}) {
  return (
    <EnvelopeAlert
      code={envelope.code}
      message={envelope.message}
      suggestion={envelope.suggestion}
      docs={envelope.docs}
      stage={envelope.stage}
      deploymentId={envelope.deployment_id}
      className={className}
    />
  );
}

/** 从部署行的失败投影构造同形态信封（历史行路径）。 */
export function DeploymentFailureAlert({
  errorCode,
  verdict,
  recovery,
  deploymentId,
  className,
}: {
  errorCode: string;
  verdict?: string;
  recovery?: string;
  deploymentId?: string;
  className?: string;
}) {
  return (
    <EnvelopeAlert
      code={errorCode || undefined}
      message={verdict || undefined}
      suggestion={recovery || undefined}
      deploymentId={deploymentId}
      className={className}
    />
  );
}
