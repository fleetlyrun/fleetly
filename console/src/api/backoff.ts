// 重连退避（D4-③）：断流后的下一次重连延迟计算——指数退避（1.5s 起，
// ×2 增长，上限 30s，±20% 抖动）+ "服务端立即正常关流"降频。
//
// 背景：固定 1.5s 重连对两类形态不友好——网络长时间不可达时高频空转
// （指数退避 + 上限收敛）；服务端开流即正常关流（非错误）时形成准紧
// 循环（连续 N 次后退避翻倍降频并提示）。EventsPage / AppLogsPage 共用。
//
// 语义：
//   - nextDelayMs(clean)：记录一次断流并给出下次延迟。clean = 无错误的
//     正常结束（服务端关流）；错误断流清零"连续正常关流"计数（真断流
//     需要正常频率重连，不降频）。
//   - markOpen()：连接建立时打点；开流时长 ≥ STABLE_CONNECTION_MS 的断开
//     视为新事件（退避计数归零）——稀疏帧但健康的流不累积退避。
//   - throttled：连续 cleanCloseThreshold 次正常关流后降频档生效（退避
//     翻倍，上限同步放宽到 2×max），页面据此追加提示文案。
//   - rng 可注入：测试钉中点抖动（±0%）得确定序列。

/** 开流超过该时长后的断开不累积退避（连接本身是健康的）。 */
const STABLE_CONNECTION_MS = 30_000;

export interface ReconnectBackoffOptions {
  /** 退避起点，缺省 1500ms。 */
  baseMs?: number;
  /** 指数退避上限，缺省 30s（降频档放宽到 2×）。 */
  maxMs?: number;
  /** 抖动幅度，缺省 ±20%。 */
  jitterRatio?: number;
  /** 连续正常关流判定阈值，缺省 5。 */
  cleanCloseThreshold?: number;
}

export class ReconnectBackoff {
  private readonly baseMs: number;
  private readonly maxMs: number;
  private readonly jitterRatio: number;
  private readonly cleanCloseThreshold: number;
  private readonly rng: () => number;

  private attempts = 0;
  private cleanStreak = 0;
  private throttled_ = false;
  private openedAt = 0;

  constructor(
    opts: ReconnectBackoffOptions = {},
    rng: () => number = Math.random,
  ) {
    this.baseMs = opts.baseMs ?? 1500;
    this.maxMs = opts.maxMs ?? 30_000;
    this.jitterRatio = opts.jitterRatio ?? 0.2;
    this.cleanCloseThreshold = opts.cleanCloseThreshold ?? 5;
    this.rng = rng;
  }

  /** 降频档是否生效（连续正常关流达到阈值）。 */
  get throttled(): boolean {
    return this.throttled_;
  }

  /** 连接建立打点（用于"健康连接不累积退避"判定）。 */
  markOpen(): void {
    this.openedAt = Date.now();
  }

  /**
   * 记录一次断流并返回下次重连延迟（ms）。
   * @param clean true = 非错误的正常结束（服务端关流）。
   */
  nextDelayMs(clean: boolean): number {
    if (
      this.openedAt > 0 &&
      Date.now() - this.openedAt >= STABLE_CONNECTION_MS
    ) {
      // 健康连接的断开：退避与降频状态全部归零。
      this.attempts = 0;
      this.cleanStreak = 0;
      this.throttled_ = false;
    }
    this.openedAt = 0;

    this.attempts += 1;
    if (clean) {
      this.cleanStreak += 1;
      if (this.cleanStreak >= this.cleanCloseThreshold) {
        this.throttled_ = true;
      }
    } else {
      this.cleanStreak = 0;
      this.throttled_ = false;
    }

    // 降频档：退避翻倍（上限同步放宽一档），再统一夹到档内上限。
    const cap = this.throttled_ ? this.maxMs * 2 : this.maxMs;
    let delay = Math.min(this.maxMs, this.baseMs * 2 ** (this.attempts - 1));
    if (this.throttled_) delay *= 2;
    delay = Math.min(cap, delay);

    // ±jitterRatio 抖动：rng=0.5（中点）时无偏移，序列可测。
    const jitter = 1 + (this.rng() * 2 - 1) * this.jitterRatio;
    return Math.round(delay * jitter);
  }
}
