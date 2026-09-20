// 重连退避单测（D4-③）：指数序列、上限、抖动边界、健康连接归零、
// 服务端连续正常关流降频与解除。fake timers 驱动 Date.now（STABLE_
// CONNECTION_MS 判定）；抖动注入 rng 钉中点（±0%）得确定序列。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ReconnectBackoff } from "@/api/backoff";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

/** rng 恒中点：抖动系数 = 1，序列确定可断言。 */
const mid = () => 0.5;

describe("ReconnectBackoff", () => {
  it("exponential backoff: starts at 1.5s, ×2, capped at 30s (consecutive error disconnects)", () => {
    const b = new ReconnectBackoff({}, mid);
    expect(b.nextDelayMs(false)).toBe(1500);
    expect(b.nextDelayMs(false)).toBe(3000);
    expect(b.nextDelayMs(false)).toBe(6000);
    expect(b.nextDelayMs(false)).toBe(12000);
    expect(b.nextDelayMs(false)).toBe(24000);
    expect(b.nextDelayMs(false)).toBe(30000); // 1.5×2^5=48s → 上限
    expect(b.nextDelayMs(false)).toBe(30000);
  });

  it("jitter ±20%: delay lands in [0.8d, 1.2d]", () => {
    const low = new ReconnectBackoff({ baseMs: 1000, maxMs: 30_000 }, () => 0);
    expect(low.nextDelayMs(false)).toBe(800);
    const high = new ReconnectBackoff({ baseMs: 1000 }, () => 0.99);
    const d = high.nextDelayMs(false);
    expect(d).toBeGreaterThanOrEqual(800);
    expect(d).toBeLessThanOrEqual(1200);
  });

  it("healthy connections do not accumulate backoff: disconnect after ≥30s open resets the sequence", () => {
    const b = new ReconnectBackoff({}, mid);
    expect(b.nextDelayMs(false)).toBe(1500);
    b.markOpen();
    vi.advanceTimersByTime(30_000);
    expect(b.nextDelayMs(false)).toBe(1500); // 归零后从 1.5s 重新起步
  });

  it("short-lived connections still accumulate: disconnect right after open does not reset", () => {
    const b = new ReconnectBackoff({}, mid);
    expect(b.nextDelayMs(false)).toBe(1500);
    b.markOpen(); // 立即断开（fake clock 未推进）
    expect(b.nextDelayMs(false)).toBe(3000);
  });

  it("5 consecutive server clean closes → throttle (backoff doubles, cap relaxed to 60s)", () => {
    const b = new ReconnectBackoff({}, mid);
    expect(b.nextDelayMs(true)).toBe(1500);
    expect(b.nextDelayMs(true)).toBe(3000);
    expect(b.nextDelayMs(true)).toBe(6000);
    expect(b.nextDelayMs(true)).toBe(12000);
    expect(b.throttled).toBe(false);
    expect(b.nextDelayMs(true)).toBe(48_000); // 第 5 次：raw 24s × 2
    expect(b.throttled).toBe(true);
    expect(b.nextDelayMs(true)).toBe(60_000); // raw 已夹 30s，×2 = 60s
    expect(b.nextDelayMs(true)).toBe(60_000);
  });

  it("error disconnect clears the throttle (true disconnects reconnect at normal cadence)", () => {
    const b = new ReconnectBackoff({}, mid);
    for (let i = 0; i < 5; i++) b.nextDelayMs(true);
    expect(b.throttled).toBe(true);
    b.markOpen(); // 未推进时钟 = 短命连接，attempts 保留
    expect(b.nextDelayMs(false)).toBe(30_000); // attempts=6 → raw 夹 30s，无翻倍
    expect(b.throttled).toBe(false);
  });
});
