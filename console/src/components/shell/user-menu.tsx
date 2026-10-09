import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { LogOutIcon } from "lucide-react";
import { useWhoami } from "@/lib/catalog";
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
  const whoami = useWhoami(token !== "");
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const name = whoami.data?.userName || whoami.data?.tokenName || "?";
  const role = whoami.data?.roleName ?? "";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" className="h-9 gap-2 px-2">
          <ProjectAvatar seed={name} label={name} className="rounded-full text-[10px]" />
          <span className="hidden flex-col items-start leading-tight sm:flex">
            <span className="text-xs font-semibold">{name}</span>
            <span className="text-[10px] text-muted-foreground">{role}</span>
          </span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        <DropdownMenuLabel className="font-normal">
          <span className="block text-xs font-semibold">{name}</span>
          {whoami.data?.tokenName ? (
            <span className="block text-[11px] text-muted-foreground">token: {whoami.data.tokenName}</span>
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
