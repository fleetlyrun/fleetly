# Console UI v2 走查报告（批 6 · staging 真机）

日期：2026-10-09；环境：fleetly-dev 单机 staging（v9c1fe3d-split），vite dev + `FLEETLY_DEV_API` 指隧道口 `127.0.0.1:19527→:9081`；凭证 = founder 密码会话（演练值），走查毕已吊销本会话 token（01M4FWKA…，用户自己的三枚 password session 未动）。

## 覆盖面（全部真机渲染，暗/亮双主题抽查）

| 面 | 结果 | 证据要点 |
|---|---|---|
| 登录（密码形态） | PASS | C6 密码会话铸 token → 壳层接管；W1' 守卫面未回退 |
| 舰队总览 | PASS | 真实平台条（Nodes 1/4 available、Alerts 0、Projects 5）+ 5 项目磁贴（确定性渐变+app 计数） |
| 项目语境树 | PASS | /p/\$projectId/apps 真数据表（torchwood/messaging），切换器选中即跳 |
| App 详情 tab 化 | PASS | hero（状态徽章+ID 复制+Deploy/Logs/Terminal）+ Overview/Deployments/Logs/Metrics tab |
| 部署列表 | PASS | messageloop 两代真实部署（succeeded，g1/g2），5s 轮询 |
| 部署详情旗舰页 | PASS | R3 全绿时间线（queued→…→succeeded ✓）+ started/duration + 进程策略表（3 进程 DNS 面）+ Revision diff + Raw 折叠；wait 流 + 断线重连 + 轮询兜底未触发异常 |
| 日志工作台 | PASS | messageloop 真流（mlbridge JSON 帧）经 react-virtual 渲染；Follow/Stop/Download/Clear 全活；智能跟随浮标未误现 |
| 指标工作台 | PASS* | Memory working set 5 真序列渲染（坐标轴/网格/图例末值）；*CPU (% of node) 预设吃后端 500（见发现 F-B1，前端同形 curl 亦 500） |
| 告警页 | PASS | firing 空（all quiet）+ Rules/Channels tab 骨架与空态 |
| 双主题 | PASS | 暗默认/亮切换全站 token 生效，图表配色两态正确 |
| 404/死链 | PASS | v2 路由族收窄后的未知路径落根 notFound 诚实态 |

## 发现与处置

- **F-1（已修）** ProjectSwitcher 展示首个项目但 store 为空——项目域导航全灰的表里不一。修：store 空时落首个项目（c7bdc9d）。
- **F-2（已修）** 面包屑裸 ULID。修：>20 字符段短显 + title 保全值（同批）。
- **F-3（已修）** 总览 Deploy 按钮指向已删除的平铺 /deployments。修：摘除（部署是项目语境动作）（同批）。
- **F-4（已修）** 根 404 无诚实形态。修：notFoundComponent + 回总览 CTA（同批）。
- **F-B1（后端，不在 UI 批处置）** `GET /v1/metrics` 对 `… / on(node) machine_cpu_cores` 除法查询返回 E_INTERNAL（error_id 1426e23def91215ae4340c1f5ac8f100；同形 curl 亦 500；无除法的裸指标与 memory 预设正常）。旧页同款预设同病——建议查 VictoriaMetrics 侧 `machine_cpu_cores` 的 `on(node)` join（C1 走查 47 序列锚时的行为需复核）。

## 遗留（如实入档）

- Identity/Settings/Nodes/Templates/Quickstart/Terminal/Events/Login 仍为旧页挂新壳（批 5 范围未动）：功能完整、走查过的 mutation 语义原样；v2 reskin 与 Templates 非法 DOM 修复、Terminal 自适应、Quickstart 向导化留待下一批（ADR-0057 验收锚相应条目保持未勾）。
- 反模式守卫执法面维持新世界目录（components/domain、lib、features、hooks、routes）；旧页消亡后扩全量 src/**。
- Data/Configuration/Tasks/Networks/Routes 内部表格暂保旧形态（语义零漂移优先），批 6 后续 reskin。

## 门禁

批 0-4 + 走查修复共 7 commit；每批 vitest 全绿（71 用例）/tsc/build/console:verify（routeTree.gen.ts 已纳入零漂移断言）。
