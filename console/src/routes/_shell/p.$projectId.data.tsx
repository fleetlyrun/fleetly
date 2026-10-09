import { createFileRoute, redirect } from "@tanstack/react-router";

// /data 已拆分（IA v3 T3/T4）：Databases → 一级页，Volumes/Uploads →
// Storage。旧入口 301 式收敛到 Storage（Databases 由导航一级直达）。
export const Route = createFileRoute("/_shell/p/$projectId/data")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/p/$projectId/storage", params });
  },
});
