import { createFileRoute } from "@tanstack/react-router";
import { QuickstartPage } from "@/features/quickstart/quickstart-page";

// Quickstart 路由接线：向导本体住 features/quickstart（routes/ 文件被
// router 插件按路由变换——组件直测走 features 侧）。
export const Route = createFileRoute("/_shell/quickstart")({
  component: QuickstartPage,
});
