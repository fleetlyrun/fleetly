import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { LogOutIcon } from "lucide-react";
import { useRecord } from "@lynx-go/uikit-core";
import { sessionResource } from "@/kit/settings";
import { useToken, setToken } from "@/lib/token";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

// 身份菜单（UI v2 外壳）：whoami 展示 + 登出（清 Token + 清查询缓存，
// 壳 beforeLoad 自然回落登录页）。
export function UserMenu() {
  const [token] = useToken();
  // 会话读（批4 kit 资源化）：token 缺席以 id=undefined 表达禁查门
  //（recordQueryOptions 对 undefined id 自动停查）。
  const whoami = useRecord(sessionResource, { id: token !== "" ? "self" : undefined, query: { staleTime: 30_000 } });
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const name = whoami.data?.userName || whoami.data?.tokenName || "?";
  const role = whoami.data?.roleName ?? "";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {/* 侧栏底部账号行：w-full + justify-start 左对齐（Button 基类
            justify-center 会让整宽钮的内容居中——走查反馈实咬）。 */}
        <Button variant="ghost" className="h-9 w-full justify-start gap-2 px-2">
          <ProjectAvatar seed={name} label={name} className="rounded-full text-2xs" />
          <span className="hidden min-w-0 flex-col items-start leading-tight sm:flex">
            <span className="truncate text-xs font-semibold">{name}</span>
            <span className="truncate text-2xs text-muted">{role}</span>
          </span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuLabel className="font-normal">
          <span className="block text-xs font-semibold">{name}</span>
          {whoami.data?.tokenName ? (
            <span className="block text-2xs text-muted">token: {whoami.data.tokenName}</span>
          ) : null}
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() => {
            setToken("");
            queryClient.clear();
            void navigate({ to: "/login" });
          }}
        >
          <LogOutIcon />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
