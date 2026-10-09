import { createFileRoute, redirect } from "@tanstack/react-router";

// 落地即总览（UI v2 信息架构）：空路径与未知路径（router notFound 之外的
// 手输 typo）都收敛到 /overview。
export const Route = createFileRoute("/")({
  beforeLoad: () => {
    throw redirect({ to: "/overview" });
  },
});
