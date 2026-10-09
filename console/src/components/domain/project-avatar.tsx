import { cn } from "cn";

// ProjectAvatar（品牌时刻，克制使用）：按资源 ID 确定性取 5 色渐变之一
// ——同一项目/App 永远同色（跨页面/会话稳定），渐变只用在这里与 hero。
const GRADIENTS = [
  "linear-gradient(135deg, oklch(0.62 0.19 293), oklch(0.68 0.15 235))",
  "linear-gradient(135deg, oklch(0.6 0.16 340), oklch(0.7 0.15 60))",
  "linear-gradient(135deg, oklch(0.58 0.13 160), oklch(0.64 0.14 200))",
  "linear-gradient(135deg, oklch(0.6 0.15 25), oklch(0.66 0.16 320))",
  "linear-gradient(135deg, oklch(0.55 0.12 200), oklch(0.66 0.17 293))",
];

export function gradientFor(seed: string | undefined): string {
  let hash = 0;
  for (let i = 0; i < (seed ?? "").length; i += 1) hash = (hash * 31 + seed!.charCodeAt(i)) >>> 0;
  return GRADIENTS[hash % GRADIENTS.length];
}

export function ProjectAvatar({
  seed,
  label,
  className,
}: {
  seed: string | undefined;
  label: string;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      style={{ backgroundImage: gradientFor(seed) }}
      className={cn(
        "grid size-7 flex-none place-items-center rounded-lg text-[11px] font-bold text-white",
        className,
      )}
    >
      {(label || "?").slice(0, 1).toUpperCase()}
    </span>
  );
}
