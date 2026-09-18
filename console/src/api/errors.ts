// 错误信封（fleetly.shared.v1.ErrorResponse 的 JSON 投影，snake_case 七
// 字段）与类型化 ApiError。Console 渲染面必须展示 code + message +
// suggestion（EnvelopeAlert），不允许退化为无上下文的裸报错。

export interface ErrorEnvelope {
  /** 稳定错误码（如 E_COMPOSE_INVALID）；退化信封形态下为空串/缺失 */
  code?: string;
  message?: string;
  /** 失败所处发布阶段（resolve / build / deploy / serve） */
  phase?: string;
  deployment_id?: string;
  /** 可执行的修复建议 */
  suggestion?: string;
  context?: Record<string, string>;
  docs?: string;
}

export class ApiError extends Error {
  readonly status: number;
  readonly envelope: ErrorEnvelope;

  constructor(status: number, envelope: ErrorEnvelope) {
    super(envelope.message ?? `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.envelope = envelope;
  }

  get code(): string {
    return this.envelope.code ?? "";
  }

  get suggestion(): string {
    return this.envelope.suggestion ?? "";
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

export function errorEnvelopeFrom(err: unknown): ErrorEnvelope {
  if (isApiError(err)) return err.envelope;
  if (err instanceof Error) return { message: err.message };
  return { message: String(err) };
}
