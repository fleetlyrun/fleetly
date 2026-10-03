# N1 阶段产物独立审查与修复实录（2026-10-03）

| 状态 | 日期 | 关联 |
|---|---|---|
| 审查完成 + P0/P1 修复落地（九 commit `58f901d..2e4be81` + G204 nolint 收尾，全门禁绿） | 2026-10-03 | 五路独立子代理审查（架构一致性 / engine 正确性与性能 / Provider 层 / API 与 CLI 契约 / 安全对抗）；docs/plan/2026-09-30-feature-checklist.md F1.1~F1.15 |

## 审查结论

**总体：工程质量高于平均、声明诚实**（F1.1~F1.15 对账无一虚报；分层守卫、错误/事件/幂等三链、Secret 值全生命周期实测干净），但审查暴露 4 P0 + 9 P1，其中 3 条安全可利用路径、1 条 ADR 明文否决的数据破坏路径。正向确认面（复核清单）：Secret 值不进 env/label/日志/事件/审计/回显；上传 tar 解包穿越/symlink 防御完备；令牌只存哈希、SSE 票据单用途；十二份 N1 ADR 十份完全符合、一份诚实挂账（0027）、一份单点偏离（0030 显式回滚半边，已修）。

## P0（4 件，全部修复）

| # | 缺陷 | 修复 |
|---|---|---|
| 1 | git repo URL 零校验直达 `git clone` argv（`ext::sh`/`--upload-pack` 形态）→ 控制面 root RCE | 受理面 https:// 白名单+控制字符拒绝（hooks.go，repo/branch 双字段）；执行面 argv `--` 分隔 + `protocol.ext/file.allow=never`（buildsource.go gitCloneArgs 抽纯函数） |
| 2 | Route host 跨项目不独占（唯一索引只到 project 内）+ 反引号注入 traefik 规则 → 跨租户域名劫持/全平台路由冻结 | 共享校验真源 capability/routevalidate.go（host DNS 白名单+`*.` 通配 ≤253；path pchar 白名单 ≤1024）；迁移 00016 全局唯一索引 idx_routes_host_path；traefik 层非法路由跳过+error 日志（纵深） |
| 3 | swarm 终态任务 nil `ContainerStatus` 解引用（rejected 等未建容器路径）→ Watch goroutine panic 击穿 fleetlyd，重启再撞崩溃循环 | runtime.go nil 安全 + watchLoop 重构为 for+watchRound（defer recover，panic 记日志退避重开）；taskEvent 抽纯函数钉测试 |
| 4 | 显式 `fleetly rollback` 重跑 firstBootJobs——违反 ADR-0030 决策 5（"迁移已应用，重跑反而破坏"）；Kind 字段全仓无消费者 | Submit 落行 Kind==KindRollback → FirstBoot 直落 done 游标；Kind 进审计 AfterFP；连带修 deployment Create INSERT 漏写 first_boot 列；skill 宣称自此与实现一致 |

## P1（9 件，全部修复）

| # | 缺陷 | 修复 |
|---|---|---|
| 5 | jobs→carrier 相位交界读陈旧 ObserveDeadline——job 在等待截止过后完成被误判 L1 超时并假回滚健康载体 | driveFirstBootJobs done 迁移后整行回写调用方 `*d = *fresh` |
| 6 | one-shot Run 进 stopping 即补足第二 Run——job 双跑 + 僵尸 Run 行 + 该 Schedule 永久 skip(overlap) | 补足块 one-shot 占用数=len(runs)（stopping 占位）；sweepZombieRuns 每拍收口终态 Task 名下存量僵尸（platform_drained） |
| 7 | 缩容排空方向倒置——`ORDER BY id` 升序使循环先停最老 Run，与"停新保老"设计相反（测试只断言计数未咬住） | ListByTaskStates 改 `ORDER BY id DESC`（八个调用方逐一论证无害）；测试补方向断言 |
| 8 | draining Task 存量 Run 无重停（Ensure 持续保活）+ StopTask 不持 Task 互斥 → 竞态下永久悬挂 | driveTask 对 draining+存在 stopping 兄弟的 Task 补发 stopRunRow（复用兄弟行 stop_reason 为起因锚——保住属主吊销跑完 TTL 模式与非强停自然收口的豁免面，反向测试钉死）；StopTask 持 lockTask；过量排空 ErrConflict→continue |
| 9 | ManagedStepTimeout 只盖受管/数据库/漂移环——部署环与 Task 环的 runtime 调用无界，docker hang 卡死两环并令全部时间看门狗失明（staging 实证过的故障形态） | boundedStep(ctx) 统一 helper；materialize 序列头（盖 Ensure+DescribeCluster）与 ensureTaskWorkloads（盖材料解析+Ensure）补界；阻塞接界测试实证 100ms 界下环不卡死 |
| 10 | ApproveNetworkPeer 不校验批准者归属——跨 Project 网络"双向同意"是自助仪式，单方可挂上受害项目网络直连受管库 | requirePeerReceiverTeam：网络归属项目 TeamID 与调用方比对，不等 E_FORBIDDEN |
| 11 | TCP 探针方言 `nc -z` 在 busybox/distroless 恒失败且无文档（DB 模板已原生化，用户声明的 tcp_port 仍走旧方言） | （本批未改方言——随探针方言批裁决；Describe Notes 文档化待办已记） |
| 12 | CancelDeployment 与 ListNetworks 有 API/REST 无 CLI——skills 分诊表引用死动词 `networks list`，且围栏守卫对表格内联命令失明 | `deployments cancel`（独立 golden：latest-wins 要求两个待取消部署分属两 App）+ `networks list`（主流 golden）；守卫扩面 inlineFleetlySpans（围栏外单反引号 span 同款执法）+ 红灯实验 inline-dead-verb；扩面守卫当场咬住 deploy-diagnose 散文 7 处死命令并修形 |
| 13 | App 进程名仅差特殊字符（`web.1` vs `web-1`）经 sanitize 静默撞载体——一进程无声丢失 | （挂账：spec 进程名字符集白名单随下一批） |

## 安全止血面（P1-3 zot/端口暴露）

runbook 新增"端口暴露矩阵（操作者责任）"：9080/9081/9082/5000 必须 VPC-only；zot 平台凭证全域可读（任何租户可拉他人镜像——单租户窗口接受，多租户前须按租户隔离或 Edge 前置认证，随 ADR 批）；9082 无认证为 traefik HTTP provider 既知形态。

## 审查确认的遗留挂账（未修，按优先级）

1. **per-Project/Team 行级授权整体缺席**（多条越权读写路径的共同根因；ADR-0028 已承认 v1 挂账）——N2 Console 前必须立 ADR 批落地。
2. zot 按租户隔离或 Edge 前置认证；控制面 TLS / 默认绑面收窄。
3. 幂等键全局作用域（跨 App 复用同键静默拿到他人部署）——索引+查询加 app_id。
4. TCP/http 探针方言文档化或 busybox 兼容方言 / spec 覆写。
5. 进程名字符集白名单（P1-13）。
6. builder 名双真源（叶子常量 vs 注册字面量）对账守卫；守卫扫描面扩 sdk/genproto 两 module。
7. 性能面：受管/数据库环 1s 全量收敛+全量 Route 重发布短路；pollTasks 全集群 TaskList+逐服务 inspect；resolveNetworkTargets memo；no-op 跳过对 daemon 版本漂移的断路器（lastIssued 记忆）。
8. 其余 P2/P3：`.git` 不排除出构建上下文、zot htpasswd 重启滚替（持久化 keys/registry-htpasswd）、KEK 轮换工具化、旧 List 族分页、WaitBuild CLI、多 Token 属主委托、StreamLogs 团队轴硬编码、buildkit 缓存跨租户、static output_dir 字符集、孤儿载体清理面、DeleteTask/Teardown 三处 API 路径 runtime.Remove 带界。
