// shadcn 标准别名 shim：fleet-ui 块（components/fleet/**）按 shadcn 惯例从
// @/lib/utils 取 cn；本仓的 cn 实现是 npm 包 cn（components/ui/** 的上游
// 形态），在此转发单源——两个世界共用同一实现，零行为分叉。
export { cn } from "cn";
