import { useMemo, useState } from "react";
import { ChevronsUpDownIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

// 时区可搜索下拉（shadcn Combobox 模式：Popover + Command/cmdk，零新依赖）。
// 候选是浏览器原生 IANA 列表（Intl.supportedValuesOf）——展示便利面，服务
// 端 Go tzdata 校验仍是真源（非法名提交即 E_INVALID_ARGUMENT，FormError
// 诚实回显）。浏览器不支持该 API 时降级为受控文本框（行为同文本框时代）。

// detectedTimezone 返回浏览器本地时区（IANA 名）——Schedule 表单的默认
// 时区来源（用户裁定 2026-10-21：默认本地时区而非 UTC）。检测失败/为空
// 回退 "UTC"。
export function detectedTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// timezoneOptions 取浏览器支持的 IANA 时区全表（ES2022 API，类型面随 lib
// 目标浮动——本地窄化取用）；不可得返回 undefined（降级分支的判据）。
function timezoneOptions(): string[] | undefined {
  const intl = Intl as { supportedValuesOf?: (key: "timeZone") => string[] };
  if (typeof intl.supportedValuesOf !== "function") return undefined;
  try {
    return intl.supportedValuesOf.call(Intl, "timeZone");
  } catch {
    return undefined;
  }
}

// TimezoneCombobox 是受控时区选择器：Detected 组置顶浏览器本地时区（切换
// 后一键切回），其余全量按字母序；输入即过滤（cmdk 默认匹配）。
export function TimezoneCombobox({
  value,
  onChange,
}: {
  value: string;
  onChange: (timezone: string) => void;
}) {
  const [open, setOpen] = useState(false);
  // 挂载时取一次（表内 ~418 项，会话内不变；按挂载求值使测试可换桩）。
  const options = useMemo(timezoneOptions, []);

  if (options === undefined) {
    return <Input value={value} aria-label="Timezone" onChange={(event) => onChange(event.target.value)} />;
  }

  const detected = detectedTimezone();
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button type="button" variant="outline" aria-label="Timezone" className="w-full justify-between font-normal">
          {value || "UTC"}
          <ChevronsUpDownIcon className="opacity-50" />
        </Button>
      </PopoverTrigger>
      {/* onOpenAutoFocus 预防 Radix 把焦点抢到 content 容器（打字会落空）——
          焦点留给 autoFocus 的搜索框，开箱即可输入。 */}
      <PopoverContent className="w-(--radix-popover-trigger-width) p-0" align="start" onOpenAutoFocus={(event) => event.preventDefault()}>
        <Command>
          <CommandInput placeholder="Search timezone…" autoFocus />
          <CommandList>
            <CommandEmpty>No timezone found.</CommandEmpty>
            <CommandGroup heading="Detected">
              <CommandItem
                value={detected}
                data-checked={detected === value || undefined}
                onSelect={() => {
                  onChange(detected);
                  setOpen(false);
                }}
              >
                {detected}
              </CommandItem>
            </CommandGroup>
            <CommandGroup heading="All timezones">
              {options
                .filter((tz) => tz !== detected)
                .map((tz) => (
                  <CommandItem
                    key={tz}
                    value={tz}
                    data-checked={tz === value || undefined}
                    onSelect={() => {
                      onChange(tz);
                      setOpen(false);
                    }}
                  >
                    {tz}
                  </CommandItem>
                ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
