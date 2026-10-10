# Console IA v3 一期+二期 staging 真机走查报告

日期：2026-10-10；对象：`7a74b9f-iav3p2` 换装后的现役 staging（fleetly-dev 单机，goose 28 零迁移）。
范围：IA v3 一期（T1-T10）+ 二期①-⑤a 五批实现的首轮真机验证。

## 换装实录

- 按平台升级操作序五步走完：前置 Platform Backup `129a0aff` + 六卷 tar（torchwood-pg/mlredis/twredis 三库 + zot + VL/VM 至 `/root/upgrade-iav3/vols/`）+ 旧二进制留存（fleetlyd-69d7608）→ SIGTERM 排水 → 二进制替换 → 起服。
- **零迁移**（goose 停在 28，区间无新迁移）、install.sh 零变更、config 键面零新增（840d8da 同形态核验）。
- drop-in 五件全存活（10-logging/11-metrics/browse/railpack/registry——runbook 教训项复核通过）。
- 健康核对：`fleetly status` healthy、doctor **0 failed**（2 warnings 为通知渠道建议，既有）；tw.dev 首探 000 = 路由冷窗（已知 ~60s），复探 **200**。
- 9080 是 gRPC 面——console HTTP 在 **9081 网关**（走查探针曾探错口，记入教训）。

## 走查结果：PASS

| 面 | 结果 | 证据 |
|---|---|---|
| 侧栏两域七组 + + New | ✅ | 项目域五用途分组 + PLATFORM 分界 + Fleet/Admin；真实五项目磁贴总览 |
| Apps 列表 | ✅ | web/probe 双 succeeded 真数据 |
| App 详情 8-tab | ✅ | **Variables tab 真机点亮**：冻结 Spec 读面（alpine:3.20 来源、×2 副本、诚实空 env、No build 诚实卡）；tab 集全在位 |
| Managed Providers | ✅ | 四实名卡**真健康全绿**（GetStatus components 真探测 details："managed registry reachable at 10.124.0.3:5000"）；页头 platform healthy 聚合徽标；**VictoriaMetrics ingest freshness "1s ago" 真探针活体**；VL 卡诚实 n/a（三期）；Restart… 按钮在场 |
| 组件重启（真机实做） | ✅ | `fleetly components restart zot` → "restarted zot (1 carriers rescheduled)"；~15s 后 GetStatus 全组件回 healthy（滚动重排自愈实证）；未知组件 E_NOT_FOUND 精确拒绝 |
| logs 三轴（db 轴） | ✅ | `fleetly logs --database <torchwood-pg>` 真库载体日志流出（PostgreSQL 启动横幅）——T8 预案真机兑现 |
| 备份下载 | ✅ | `databases download-backup` → 55287 bytes 落盘与台账 SIZE 一致（torchwood-pg 四颗 succeeded 调度备份真数据；备份健康度列真锚） |
| GetAppSpec（CLI） | ✅ | protojson 冻结 Spec 回读与应用详情 Variables 面一致 |
| token 纪律 | ✅ | walk-iav3（owner）走查毕即吊销 |

## 走查发现（W 项，均轻微）

- **W-1**：Apps 列表/详情族面包屑显示裸项目 ID 而非项目名（n0reg 页 crumb "p > 01M3SGPMK8…"）。
- **W-2**：App variables 页内容区 crumb 首段显示 app 名（"web / web"），首段应为项目名。
- 两项均为 crumb 文案源问题，不影响功能；随批 6 reskin 修。

## 边界与遗留

- 浏览器走查覆盖：总览/Apps/Variables/Managed Providers 四屏截图级 + 其余屏 DOM 快照/API 级（Sessions 限制下以 URL 直达 + CLI 对拍补全；DOM 定位器与 shadcn Sidebar 双拷贝结构不兼容的走查工具限制，非产品问题）。
- 二期⑤b 待办不变：DB 密码轮换（方言级）、zot catalog 代理、Volume 删除、Variables 暂存编辑流、磁盘水位。
- zot 重启为真机实做（滚动重排一次）；traefik/VL/VM 未真重启（避免无谓面中断——Restart 按钮与错误路径已由 zot + 单测覆盖）。
