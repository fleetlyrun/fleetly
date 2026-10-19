import { useEffect } from "react";
import { useNavigate } from "@tanstack/react-router";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { useWhoami } from "@/lib/catalog";
import { navSections } from "@/lib/nav";

// ⌘K 命令面板（UI v2 键盘契约）：全局导航 + 动作入口。⌘K/Ctrl-K 开合，
// Esc 关闭（Dialog 自带）；选择即导航。导航项与侧栏同源（Nodes 的平台
// 管理员显隐同判；R08-M1）。
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate();
  const whoami = useWhoami(true);
  const sections = navSections(undefined, whoami.data?.platformAdmin === true);

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        onOpenChange(!open);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onOpenChange]);

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <CommandInput placeholder="Type a page or action…" />
      <CommandList>
        <CommandEmpty>No results.</CommandEmpty>
        {sections.map((section) => (
          <CommandGroup key={section.label} heading={section.label}>
            {section.items.map((item) => (
              <CommandItem
                key={`${item.label}-${item.to}`}
                value={`${item.label} ${item.to}`}
                onSelect={() => {
                  onOpenChange(false);
                  void navigate({ to: item.to });
                }}
              >
                <item.icon />
                {item.label}
              </CommandItem>
            ))}
          </CommandGroup>
        ))}
        <CommandSeparator />
        <CommandGroup heading="Actions">
          <CommandItem
            value="new project app quickstart guided setup"
            onSelect={() => {
              onOpenChange(false);
              void navigate({ to: "/quickstart" });
            }}
          >
            New project / app…
          </CommandItem>
          <CommandItem
            value="new app from template catalog"
            onSelect={() => {
              onOpenChange(false);
              void navigate({ to: "/templates" });
            }}
          >
            New app from template…
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  );
}
