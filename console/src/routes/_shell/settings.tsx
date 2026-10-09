import { createFileRoute } from "@tanstack/react-router";
import { SettingsPage } from "@/pages/Settings";

// 过渡路由（UI v2 批 1）：旧页面挂新壳，批 2-5 由 /p/$projectId 语境树
// 下的新页替换后本文件消亡。
export const Route = createFileRoute("/_shell/settings")({
  component: SettingsPage,
});
