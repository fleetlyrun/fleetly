// 保留窗常量（ADR-0101）：Revision 行按保留窗清理，审计链由 Deployment
// 时间线承载。常量单源——App 与 Function（ADR-0102 建表）同律。
package api

// RevisionRetentionPerApp 是每聚合保留的最新 Revision 行数上限（ADR-0101
// 决策 1）。在流量的不可删（活跃集超窗全保留 = 软上限）；新 Revision 落账
// 同事务清最旧非活跃至该窗；聚合终局（App/Function 删除）即时全清。v1 钉
// 常量不做 per-project 旋钮（行业对照：CF 交互面 100 / AWS Web Functions
// 50 / Cloud Run 1000）。
const RevisionRetentionPerApp = 100
