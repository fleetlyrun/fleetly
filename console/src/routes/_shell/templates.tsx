import { createFileRoute } from "@tanstack/react-router";
import { useRouter } from "@tanstack/react-router";
import { TemplatesPage } from "@/pages/Templates";

// 过渡路由（UI v2 批 1）：模板库旧页挂新壳；navigate 桥翻译旧相对路径。
export const Route = createFileRoute("/_shell/templates")({
  component: TemplatesBridge,
});

function TemplatesBridge() {
  const router = useRouter();
  return (
    <TemplatesPage
      navigate={(path) => {
        router.history.push(path.startsWith("deployments/") ? `/deployments/${path.slice("deployments/".length)}` : `/${path}`);
      }}
    />
  );
}
