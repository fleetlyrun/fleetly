import { cn } from "cn";

// PageTabs 是页面级模块 tab（对齐批 5 复裁：一页多模块用 tab 组织，形态
// 以 App Detail 的下划线式为基准——页头下 border-b 一行，active 紫色下
// 边框；URL param 深链由各页自带）。App Detail 布局本体是路由式锚点，
// 不经本件；本件服务 search-param 驱动的模块页。泛型 T 让 onChange 回传
// 收窄后的 tab value 字面量联合，调用侧 navigate({ search }) 免 cast。
export function PageTabs<T extends string>({
  tabs,
  current,
  onChange,
  className,
}: {
  tabs: Array<{ value: T; label: string }>;
  current: T;
  onChange: (value: T) => void;
  className?: string;
}) {
  return (
    <div className={cn("mb-4 flex gap-1 border-b", className)}>
      {tabs.map((tab) => {
        const active = tab.value === current;
        return (
          <button
            key={tab.value}
            type="button"
            onClick={() => onChange(tab.value)}
            aria-current={active ? "page" : undefined}
            className={`-mb-px rounded-t-md border-b-2 px-3.5 py-2 text-[13px] font-medium transition-colors ${
              active
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:bg-muted hover:text-foreground"
            }`}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}
