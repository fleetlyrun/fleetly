// 登出提示的一次性标志（2026-09-25 审查前序遗留：全局 401 弹回曾纯静默
// ——会话过期弹到登录页无任何解释）。两条链路在此分叉：
//   - 全局 401 处置（App.tsx unauthorizedListener → clearSession）：置位
//     ——登录页渲染中性提示「You have been signed out.」（muted，非告警）；
//   - 主动登出（用户菜单 Sign out → auth.logout）：不置位——用户自己发起
//     的登出无需告知。
// sessionStorage 生命周期 = 标签页会话（跨标签不串）；读后即清（一次性）。
const SIGNED_OUT_KEY = "fleetly.console.signed-out";

/** 全局 401 弹回时置位（App.tsx 的 unauthorizedListener 路径独占）。 */
export function markSignedOut() {
  try {
    sessionStorage.setItem(SIGNED_OUT_KEY, "1");
  } catch {
    // 存储不可用（隐私模式等）：缺提示可接受，不阻断登出流。
  }
}

/** 登录页挂载时读取并清除；置位过 = 渲染中性提示条。 */
export function consumeSignedOut(): boolean {
  try {
    if (sessionStorage.getItem(SIGNED_OUT_KEY) !== "1") return false;
    sessionStorage.removeItem(SIGNED_OUT_KEY);
    return true;
  } catch {
    return false;
  }
}
