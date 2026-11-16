import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { ChevronsUpDownIcon, LogOutIcon } from "lucide-react";
import { useRecord } from "@lynxland/uikit-core";
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
            justify-center 会让整宽钮的内容居中——走查反馈实咬）。与
            ProjectSwitcher 同语言：扁平行 + 悬停铺色 + 右侧展开符。 */}
        <Button
          variant="ghost"
          className="h-auto w-full justify-start gap-2 rounded-md px-2 py-1.5 hover:bg-panel-2"
        >
          <ProjectAvatar seed={name} label={name} className="size-7 rounded-full text-2xs" />
          <span className="flex min-w-0 flex-1 flex-col items-start leading-tight">
            <span className="truncate text-xs font-semibold">{name}</span>
            <span className="truncate text-2xs text-muted">{role}</span>
          </span>
          <ChevronsUpDownIcon className="size-3.5 shrink-0 text-muted" />
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
