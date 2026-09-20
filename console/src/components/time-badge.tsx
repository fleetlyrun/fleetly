// 顶栏时钟：操作台常备件（日志时间戳/证书到期均为服务器 UTC 口径，本地
// 时钟 + UTC 偏移并列，操作者对表用）。

import { useEffect, useState } from "react";

export function TimeBadge() {
  const [now, setNow] = useState(() => new Date());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const clock = now.toLocaleTimeString("en-GB", { hour12: false });
  const offsetMin = -now.getTimezoneOffset();
  const sign = offsetMin >= 0 ? "+" : "-";
  const abs = Math.abs(offsetMin);
  const utcLabel = `UTC${sign}${String(Math.floor(abs / 60)).padStart(2, "0")}:${String(abs % 60).padStart(2, "0")}`;

  return (
    <div className="hidden items-center gap-2 rounded-md border bg-muted/40 px-2.5 py-1.5 font-mono text-xs lg:flex">
      <span className="tabular-nums">{clock}</span>
      <span className="text-muted-foreground">{utcLabel}</span>
    </div>
  );
}
