# Spike B — 发布与路由验证 FINDINGS

状态：**已完成**（2026-09-17，单段执行，全部 9 组实验在全新 `docker:29.8.1-dind` 内完成）。

环境基线：本机 Docker Desktop 29.7.2（宿主）+ 特权 `docker:29.8.1-dind`（inner engine **29.8.1** / API 1.56，与门禁一致）。单节点 swarm（`--advertise-addr` eth0）、attachable overlay `spike-b-net`、**Traefik `v3.5`**（image-id `sha256:16acb89c6db3…`，HTTP provider `providers.http.endpoint` 指向自备配置服务，pollInterval=2s）。探针：独立 Go module `spike/b`（stdlib-only 单二进制，宿主交叉编译 linux/amd64 静态链接），fixture 镜像 alpine:3.20 + `/probe`（buildx `--provenance=false --sbom=false`，5 个变体：v1 / v2 / v2bad〔/health 恒 503〕/ v2slow〔延迟 6s 监听〕/ crash〔启动即退〕）。证据全部出自 `spike/b/artifacts/logs/`，dind 内单一内核时钟（探针 JSONL 毫秒 + docker events 纳秒可直接对齐）。

设计依据：`docs/design/2026-09-17-architecture.md` §4.1 Spike B 行（验收真源）、§2.5 发布流程与不变量；`docs/design/2026-09-17-release-semantics.md` §1/§2/§6（B1–B5 扩展）；`docs/research/2026-09-17-swarm-substrate-assessment.md` §2。

---

## 0. 实验矩阵总览

| # | 假设 | 方法 | 结果 | 结论 | 设计影响 |
|---|------|------|------|------|----------|
| B1/V1 | health 失败不切流：新任务 FAILED、更新 paused、旧任务持续服务 | v1→v2bad（start-first+pause），外部 prober 200ms 连续探测全程 | ✅ 4/4 断言 PASS；298 样本 0 失败全 v1 | health 门 + pause 的「失败不切流」在 29.8.1 实测成立 | 架构 §2.5 失败=不切流量（启动期）成立；失败矩阵第 5/6 行行为得到实测锚点 |
| B2 | 归位零成本：pause 后同内容 spec 重放，任务 id 不变 | 重放前后 `docker service ps` task id 对照；另做 7 步字段脏检矩阵 | ✅ 旧 task id `j5s05wepi6ew` 跨失败+重放不变，零新增任务；字段矩阵符合 TaskSpec 深度相等 | `IsTaskDirty` 零成本归位成立；`--force` 必重建 | release-semantics §2.4「归位零成本」去掉「待验证」；平台归位重放禁止 `--force` |
| **B3** | **LB 端点时机：新任务是否早于 healthy 进入端点集合（头号开放问题）** | start-first 更新窗口内，overlay 内探针 100ms 分辨率持续打 VIP + 解析 `tasks.<svc>` DNSRR，与 health_status 事件对齐 | ✅ **带 healthcheck：端点入集晚于 healthy 45–87ms，失真=0**；无 healthcheck 对照：入集早于就绪，27 次连续失败 | 「失败=不切流量」对带 healthcheck 的重发布**成立**；health_gate=none 路径有真实暴露 | 架构 §5 LB 风险行关闭（带 healthcheck 场景）；降级表「无 healthcheck」行要写实危害 |
| V3 | 更新窗口经 Traefik 路由零失败；VIP 前后不变 | HTTP provider 下发 v3.local 路由，健康更新期间 100ms 连续探测 + `network inspect -v` 前后 VIP 对照 | ✅ 395 样本 0 失败；VIP `10.0.1.163` 前后不变 | 路由发布指向 service VIP 的形态在更新窗口稳定 | 架构 §4.1 V3 行通过；控制面路由可安全指向 `<service>` VIP |
| V4 | keep-alive 陈旧连接（Dokploy #5281）可复现且可用 serversTransport 消除 | 三轮：①空闲池+干净退出 ②in-flight kill × serversTransport ③in-flight kill × 优雅退出（POST 不可重放） | ⚠️ ①②自愈/无感（Go transport 对幂等请求透明重试）；③**abrupt 必现 502（每次 kill 恰 1 个），优雅退出 0 失败** | 用户可见失败的真实来源是 **in-flight 非幂等请求被 kill**；治理主键=应用侧优雅退出，serversTransport 只管空闲池 | 架构 §2.5 连接治理与 §5 keep-alive 行要改：serversTransport 从「消除」降级为「辅助」；应用侧 SIGTERM 优雅退出升为必要条件（含正确实现模板） |
| Provider | HTTP provider 下发纪律：正常/空/单应用坏/不可达 四态 | cfgsvc 重写 config.json 四种载荷 + 探测 + Traefik 日志 | ✅ 含关键反转：空 map 载荷被 Traefik 拒绝（保留）；**裸 `{}` 会清空全部路由（实测 404）** | 「空配置不落盘」必须由控制面强制，Traefik 只挡得住显式空 map，挡不住 `{}` | 架构 §2.5「空 routers/services 不落盘」的校验必须含「键必须存在且合成后非空」；坏配置先校验后写（Traefik 会应用含坏 router 的整份配置） |
| Matrix | 失败矩阵 3 场景 × 2 顺序：任务状态/UpdateStatus/旧任务命运/恢复 | crash-on-start、health 永不通过、拉取失败 × start-first/stop-first | ✅ 6/6：全部 `UpdateStatus=paused` + update exit 1；sf 旧任务在、探测 0 失败；xf 旧任务先死、停机 10–12s、恢复重建 | 错误码分类素材齐：crash→E_TASK_START_FAILED、badhealth→E_HEALTH_TIMEOUT、pullfail→E_IMAGE_PULL_FAILED | release-semantics §2.5 场景 3/5/6/13 行为全部实测锚定；stop-first 停机量级（秒级）可进文档 |
| RR | 快照重放回滚 <60s 且旧版本恢复服务 | v1→v2（9s）→以 v1 spec 重放计时 | ✅ 11s（<60s），v1 探测 3/3 200 | 回滚=按快照重放满足分钟级承诺 | release-semantics nightly「回滚一分钟内完成」通过 |
| 意外发现 | — | 见 §9 | 10 条 | — | 观测实现（docker events 无 task 事件）、优雅退出模板（os.Exit 坑）、 Traefik `{}` 清空等直接进实现注意事项 |

通用结论标注：✅=实测证据支持；⚠️=有证据但有局限（局限明示）。

---

## 1. B1/V1 — health 失败不切流（4 断言全 PASS）

**方法**：`scripts/in-b1-v1b2.sh`。`app-v1`（healthcheck interval 1s / retries 3 / start-period 2s，`--update-failure-action=pause --update-order=start-first --update-monitor=5s`）健康后，外部 prober（`lbwatch`，200ms 分辨率，走 service VIP）覆盖「失败更新 + 归位重放」全程；更新切 `v2bad`（同一二进制，`HEALTH_MODE=fail` 使 /health 恒 503）。

**原始输出**（`artifacts/logs/in-b1-v1b2.log`）：

```
[18:01:10] old task id=j5s05wepi6ew
[18:01:19] update exit=1
  service update paused: update paused due to failure or early termination of task 59ywfqrqysyv…
53qim9lewroyx2u app-v1.1 spike-b/app:v2bad Ready 2 seconds ago Ready      ← 见 §9#3 重启churn
59ywfqrqysyvafm app-v1.1 spike-b/app:v2bad Failed 4 seconds ago Shutdown
j5s05wepi6ewsde app-v1.1 spike-b/app:v1  Running 21 seconds ago Running
UpdateStatus={"State":"paused",…,"Message":"update paused due to failure or early termination of task 59ywf…"}
V1-ASSERT-1 UPDATE_PAUSED: PASS
V1-ASSERT-2 NEW_TASK_FAILED: PASS (59ywfqrqysyv)
V1-ASSERT-3 OLD_TASK_STILL_RUNNING: PASS
[18:02:07] prober fails=0 version-histogram: 298 "ver":"v1"
V1-ASSERT-4 EXTERNAL_PROBE_ZERO_FAILURE: PASS
```

外部探测是「旧任务持续服务」的独立证据：失败更新全程 298 次 VIP 请求全部 200 且全部 `v1`，未健康的新版本一个字节都没漏出去。

**结论**：✅ 成立。V1 为「失败=不切流量」提供了运行时证据（与评估报告 §1 的源码级结论一致）。

**设计影响**：架构 §4.1 B 行「health 失败时新任务 FAILED、更新 paused、旧任务不中断」——通过。失败矩阵第 5/6 行（启动即崩 / health 永不通过）的 start-first 行为自此有实测锚点。

**重跑**：`spike\b\scripts\b1-run.bat`（需先 b0-up）。

---

## 2. B2 — 归位零成本 + 字段脏检矩阵

**方法**：V1 失败 pause 后，用**同内容 spec**（`docker service update --image spike-b/app:v1`，无 `--force`）重放；对照 `app-dirty` 服务做 7 步字段矩阵，每步前后取 task id。

**原始输出**（同日志 B1.7/B1.9 段）：

```
j5s05wepi6ew app-v1.1 spike-b/app:v1  Running 26 seconds ago Running    ← 重放后旧任务原样
B2-ASSERT-1 OLD_TASK_ID_UNCHANGED_AFTER_REPLAY: PASS
B2-ASSERT-2 NO_EXTRA_TASK_CREATED: PASS
UpdateStatus-after-replay={"State":"completed",…,"Message":"update completed"}
```

字段矩阵（`B2b[…] rc=0 -> …` 行）：

| 变更 | 结果 | 解释 |
|---|---|---|
| 重放完全相同 image | UNCHANGED | 零成本归位本体 |
| `--label-add foo=bar`（服务级 label） | UNCHANGED | 不在 TaskSpec |
| `--container-label-add cl2=x` | **REPLACED** | 在 TaskSpec |
| `--update-parallelism 2` | UNCHANGED | 服务级更新配置 |
| `--env-add E1=1` | **REPLACED** | 在 TaskSpec |
| `--restart-condition on-failure` | **REPLACED** | 在 TaskSpec |
| `--force`（零内容变化） | **REPLACED** | 强制重建 |

**结论**：✅ 成立。同内容重放零任务变动；脏检按 **TaskSpec 深度相等**（服务级字段不脏）。失败矩阵里 pull-sf 的恢复再次独立复现（旧 task id `ofr4v7gajhgj` 跨拉取失败更新+恢复不变，`RESTORE-ZERO-CHURN: PASS`）。

**设计影响**：release-semantics §2.4「归位零成本（待验证）」→ 已验证成立；§5 风险行同改。**新增实现纪律：归位重放不得携带 `--force`**（必重建）；`deploy.update_config`、服务 label 类变更不会引发滚动。

**重跑**：`spike\b\scripts\b1-run.bat`（含 B2b 矩阵）。

---

## 3. B3 — LB 端点时机（头号产出）

**方法**：`scripts/in-b2-b3.sh`。三段子实验，探针容器挂在同一 overlay，100ms 分辨率：VIP 路径连续 GET `http://<svc>:8080/`（每次新连接，读响应体版本戳）+ `tasks.<svc>` DNSRR 集合变化；与 `docker events` 的 health_status/容器事件（纳秒时间戳，同内核时钟）对齐。

### B3a 健康慢启动更新（v1 → v2slow：6s 后才监听，start-period 15s）

时间线（`in-b2-b3.log` B3a 段，全部毫秒可复核）：

| 事件 | 时刻 (UTC) | 相对 |
|---|---|---|
| 新任务容器 start（未监听） | 18:10:09.922 | t0 |
| **health_status: healthy**（新任务） | 18:10:19.**973** | t0+10.05s |
| **DNSRR 集合加入新 IP**（10.0.1.42→42,44） | 18:10:20.**018** | **healthy +45ms** |
| **首个 v2 响应经 VIP 返回** | 18:10:20.**060** | **healthy +87ms** |
| DNSRR 移除旧 IP（流量全切） | 18:10:21.118 | healthy +1.15s |
| 旧容器 die（SIGTERM） | 18:10:23.350 | healthy +3.38s |

```
B3a-RESULT: first-v2-through-VIP ms=1789668620060 ; healthy-event ms=1789668619973 ; delta=87 ms
lbwatch-summary: total_ok=542 total_fail=0  per_version={v1:159, v2:383}
```

542 个样本零失败：慢启动的新任务在监听前（t0~t0+6s）没有任何一个请求落到它上面——STARTING 任务不在端点集合。

### B3b 失败更新（v1 → v2bad：会服务 / 但 /health 恒 503）

```
health_status: unhealthy (v2bad)  18:11:30.351；container die 18:11:32.568
UpdateStatus={"State":"paused",…}
dnsrr transitions: 只有初始旧 IP 一行 —— 未健康任务从未入集
lbwatch-summary: total_ok=443 total_fail=0  per_version={v1:443}
```

**失真量 = 0 个请求样本、0 次 DNSRR 变化**。从未 healthy 的任务被杀前始终不在 LB 端点。

### B3c 对照：无 healthcheck（health_gate=none 形态，v1 → v2slow）

```
container start 18:12:24.478 → DNSRR 加入新 IP 18:12:24.507（+29ms，container start 即入集）
首个失败 18:12:24.551 dial tcp 10.0.1.52:8080: connect: connection refused（VIP 转发到未监听任务）
连续失败 27 个样本，跨越 ~6.1s（直到 18:12:30.596 首个 v2 响应）
旧任务 die 18:12:27.915 —— 窗口内新旧任务同时在端点里，打到新任务的请求全灭
```

### B3 结论（对设计直接负责）

1. **带 healthcheck：端点入集严格晚于 healthy（实测 +45ms/+87ms）**——「失败=不切流量」在 start-first 重发布场景**不失真**；架构 §5 风险行「LB 端点可能早于 health 加入（开放问题）」对带健康门服务**关闭**，对外口径**无需降级**为「切换期少量 5xx」，观察窗起点=切流（首个目标实例健康）无需前移。
2. **无 healthcheck：暴露真实存在**——新任务 container start 即入端点（+29ms），6s 预热窗口内 27 次连续失败（约 4.4 失败/s，新任务在端点期间命中它的请求全部失败）。`health_gate=none` + `W_DEPLOY_NO_HEALTHCHECK` 的降级说明必须写明这个量级。
3. 机制解释（与源码结论互相印证）：端点集合由 RUNNING 任务驱动，而执行器对定义了 healthcheck 的任务只有 healthy 才报 RUNNING；无 healthcheck 时 container start 即 RUNNING。

**局限（如实声明）**：单节点单副本；探针为合成服务（真实应用TLS/重定向等未覆盖）；分辨率 100ms，"+45ms" 的绝对值受采样限制，但「晚于 healthy」的序关系由 DNSRR/事件双源一致确认。

**设计影响**：架构 §4.1 B 行「start-period 内任务是否已进 LB 端点集合（B3，最高优先级开放问题）」→ 已回答；§5 风险行与 release-semantics §6 B3 → 按上述改写；§2.5 降级表「无 healthcheck」行补量化口径。

**重跑**：`spike\b\scripts\b2-run.bat`。

---

## 4. V3 — 更新窗口经 Traefik 路由零失败 + VIP 稳定

**方法**：`scripts/in-b3-v3v4.sh`。Traefik v3.5（swarm service，HTTP provider 轮询 cfgsvc）路由 `Host(`v3.local`)` → `http://app-v3:8080`（service VIP）；健康更新 v1→v2 期间 100ms 连续探测；`docker network inspect -v` 前后取 VIP。

**原始输出**：

```
VIP before: … app-v3=10.0.1.163 …
T0: healthy update v1 -> v2   update exit=0
VIP after:  … app-v3=10.0.1.163 …            ← 逐字节相同
client-summary: total_ok=395 total_fail=0  per_version={v1:93, v2:302}
V3-ASSERT-1 ROUTING_ZERO_FAILURE: PASS
V3-ASSERT-2 VIP_STABLE: PASS
```

**结论**：✅ 成立。更新窗口内经 Traefik→VIP 的新连接零失败，版本 v1→v2 干净切换，VIP 地址跨更新不变（评估报告 §2「VIP 预期随 service 生命周期稳定，无官方承诺」补充了实测正证；残留风险仅剩社区负例，控制面缓存 VIP 引用时仍建议容忍 VIP 变化）。

**设计影响**：架构 §4.1 V3 行通过；「路由发布严格晚于 health gate + 指向 VIP」的组合形态成立。

**重跑**：`spike\b\scripts\b3-run.bat`。

---

## 5. V4 — keep-alive 陈旧连接（三轮，#5281 的真实形态）

**方法**：三轮实验。R1（`in-b3-v3v4.sh`）：空闲池 staleness（keep-alive 客户端 6s 间隔 + 干净退出后端）；R2（`in-b3c-v4v2.sh`）：in-flight kill × 三配置（abrupt / +serversTransport `idleConnTimeout=1s,maxIdleConnsPerHost=1` / +graceful）；R3（`in-b3d-v4post.sh`）：**POST（不可重放）连续 in-flight**（请求体 2s 慢响应，100% in-flight 覆盖，每阶段两次 kill：image 更新 + `--force`）。

**R1 空闲池 + 干净退出：自愈，用户无感**

```
ka 客户端 summary: total_ok=19 total_fail=0（6s 间隔长连接跨更新）
control 客户端:    total_ok=220 total_fail=0
```

后端进程干净退出时内核发 FIN，Traefik 的 Go transport 把死连接剔出空闲池并在重用时透明重试幂等请求——空闲池 staleness 在 Go 技术栈里**不会变成用户可见失败**。

**R2 in-flight kill：GET 几乎无感，serversTransport 管不了 in-flight**

三轮客户端累计 0 个 502；唯一可见痕迹是 1 次 3s 客户端超时（`context deadline exceeded`，18:32:06，恰在 kill 窗口，且发生在**带 serversTransport** 的 v4b 上）——幂等 GET 被透明重试，代价只是时延毛刺；重试拉长后超过客户端超时才显形。

**R3 POST 连续 in-flight：abrupt 必现 502，优雅退出零失败**

```
A(abrupt):  total_fail=2  per_version={"?":2, v1:6, v2:21}
  502 @18:49:11.581  ← 精确等于旧容器 die 时刻 18:49:11.581（exitCode=2，SIGTERM 即死）
  502 @18:49:24.314  ← 第二次 kill（--force），同样逐毫秒对齐
C(graceful): total_fail=0  per_version={v1:6, v2:22}   两次 kill 全程零失败
  被杀容器日志: graceful-shutdown-begin 18:50:08.855 → done 18:50:09.939（等完 in-flight，drain 1.08s）exit=0
```

**结论**：⚠️ 有条件成立（真实机制比设计假设的更细）：
1. 用户可见失败的真实来源是 **in-flight 非幂等请求在后端被 kill**（abrupt 停止下每次 kill 恰好 1 个 502，时间戳与容器 die 逐毫秒对齐）。
2. **空闲池 staleness 与幂等 GET 都被 Go transport 自愈**——Dokploy #5281 的「偶发」性质由此解释（只有 in-flight 命中才可见）。
3. **优雅退出是消除手段**：SIGTERM 后 drain（等 in-flight 完成）+ `stop_grace_period > drain 时长`，实测 502 → 0。
4. `serversTransport`（idleConnTimeout/maxIdleConnsPerHost）只作用于空闲池，对 in-flight kill 无效（R2 超时恰恰发生在开启它的路由上）——保留为卫生项，不再是「消除」手段。

**设计影响**：架构 §2.5「连接治理：keep-alive…Traefik serversTransport 调小 idle 连接 + 应用侧 SIGTERM 后 Connection: close」与 §5 keep-alive 行 → 改为：主键=应用侧优雅退出（模板必须给出正确实现，见 §9#4 os.Exit 坑；stop_grace 须 > drain 预算），serversTransport 降为辅助卫生项；对外口径「新连接零失败」成立（V3 实测），「in-flight 非幂等零失败」依赖应用优雅退出，文档如实分层。
**局限**：Go 技术栈（Traefik 内置 transport 与 Go 后端）；非 Go 应用/中间件链行为可能不同；WS/SSE 未测（设计已声明断线重连语义）。

**重跑**：`spike\b\scripts\b3-run.bat`（R1）→ `b3c-run.bat`（R2）→ `b3d-run.bat`（R3）。

---

## 6. Traefik HTTP provider 下发纪律（四态）

**方法**：`scripts/in-b4-provider.sh`。cfgsvc（控制面替身）逐次重写 `/config`，Traefik pollInterval=2s，每态等待 ≥8s（4 个轮询周期）后探测。

```
a) 好配置:            x1 200 v1, x2 200 v1                       → 双应用路由生效
b1) {"http":{"routers":{},"services":{}}}:
     Traefik log: Provider error … "cannot decode configuration data:
       routers cannot be a standalone element (type map[string]*dynamic.Router)"
     探测: x1 200, x2 200 → 路由保留（载荷被拒绝 = 变相「空配置不落盘」）
b2) {}:
     探测: 404 page not found → 路由被清空！
c1) 单应用坏配置（x2 引用不存在的 middleware nope@file）:
     log: error="middleware \"nope@file\" does not exist" routerName=x2@http
     探测: x1 200（隔离成立）, x2 404（坏 router 被应用后失效）
c2) 裸坏 JSON（not-json{{{）:
     log: Provider error … unmarshal errors → Traefik 保留上一份**成功应用的**配置
     探测: x1 200；x2 仍 404（上一份=c1 坏配置，坏应用不会被 Traefik 自愈）
d) 配置服务不可达（docker stop cfg）:
     log: Provider error … fetch … timeout / lookup cfg … no such host（退避重试）
     探测: x1 200, x2 200 → 控制面挂了入口不坏
```

**结论**：✅ 四态成立，但有一个关键反转：**Traefik 只挡得住「显式空 map」和「解析失败」，挡不住合法的 `{}`**——后者会解码为空配置并清掉全部路由。b/d 证明「保留上一份成功配置」的兜底真实存在（含指数退避日志）；c 证明故障隔离是 router 级（坏 middleware 只打死该应用）。

**设计影响**：架构 §2.5「空 routers/services 不落盘、单应用坏配置不得影响其他应用」→ 实现细则落地：控制面合成配置必须保证 `http.routers/services` **键存在且非空**，坏配置**先校验后落盘**（Traefik 会应用含坏 router 的整份配置，不会替平台拦）；「配置服务不可达入口不坏」由 Traefik 原生保证（实测），控制面恢复后配置自动续上。

**重跑**：`spike\b\scripts\b4-run.bat`。

---

## 7. 失败矩阵（3 场景 × start-first/stop-first）

**方法**：`scripts/in-b5a-matrix.sh` + `in-b5b-matrix.sh`。统一协议：健康 v1 基线 → 更新到失败内容 → 轮询 UpdateStatus → 快照 `service ps` + 探测 → 以 v1 spec 重放恢复 → 计时。

| 组合 | 新任务终态 | UpdateStatus | update 退出码 | 旧任务 | 探测（失败窗口） | 恢复 |
|---|---|---|---|---|---|---|
| crash × start-first | Failed（+重启churn） | paused | 1 | **Running** | 3/3 v1 200 | 零成本归位（旧 id 不变） |
| crash × stop-first | Failed | paused | 1 | **Shutdown（先停）** | 0 ok / 3 fail | 重建 v1 任务，10s 恢复 |
| badhealth × start-first | Failed（unhealthy 被杀） | paused | 1 | **Running** | 3/3 v1 200 | 零成本归位 |
| badhealth × stop-first | Failed | paused | 1 | **Shutdown（先停）** | 0 ok / 3 fail | 重建 v1 任务，12s 恢复 |
| pullfail × start-first | **Rejected**（pull 失败） | paused | 1 | **Running** | —（未测流量，ps 为证） | 零成本归位（`RESTORE-ZERO-CHURN: PASS`，旧 id `ofr4v7gajhgj` 不变） |
| pullfail × stop-first | Rejected | paused | 1 | **Shutdown（先停）** | — | 重建 v1 任务，11s 恢复 |

代表性原文（`in-b5a-matrix.log`）：

```
[18:55:43] UpdateStatus=paused
  UpdateStatus-msg=update paused due to failure or early termination of task z3re50a1rawm…
  z3re50a1rawm … spike-b/app:crash Failed …  xiyvfxrvrwdy … Assigned …
  xe7ruepx0fd6 … spike-b/app:v1   Shutdown …            ← stop-first：旧任务先死
  prober: total_fail=3 total_ok=0                       ← 停机窗口实测
[18:54:31] prober(crash-sf): total_fail=0 per_version={v1:3}  ← start-first：旧任务撑住
```

**结论**：✅ 六格齐。三场景错误分类素材：crash→`E_TASK_START_FAILED`（Failed+churn）、badhealth→`E_HEALTH_TIMEOUT`（unhealthy→被杀）、pullfail→`E_IMAGE_PULL_FAILED`（**Rejected**，从未启动）。start-first 全程零流量损失；stop-first 停机实测 10–12s（判定+恢复全程）。失败动作全部落 `paused`，无一处自动回滚（Swarm 原生回滚确实没被触发——与设计「永不调用原生回滚」无冲突）。

**如实声明**：脚本里恢复完成的轮询用了 `service ps` 首行（受 §9#3 churn 干扰），三处打印了假 "restore FAILED"——以同日志末尾的 `service ps`（旧 v1 任务 Running、失败版本全部 Shutdown）与 UpdateStatus 为准；恢复秒数仅 stop-first 三格有效（重建新任务的真实耗时）。

**设计影响**：release-semantics §2.5 场景 3/5/6/13 全部有实测锚点；§2.2 L1 的「任务 `Status.Err`」在 ps 里可读（crash 的 non-zero exit / pullfail 的 pull access denied）；stop-first「停机持续到恢复完成」量化为秒级。

**重跑**：`spike\b\scripts\b5-run.bat`。

---

## 8. 快照重放回滚计时

**方法**：`scripts/in-b6-rr.sh`。健康 v1 → 更新 v2（image 切换）→ 以 v1 spec 重放，完成判据 = 任务 Running 且 VIP 探测返回 v1。

```
upgrade: exit=0, 9s   probe: {v2:3} 0 fail
replay:  exit=0, 11s（完成轮询 0s 即命中）  probe: {v1:3} 0 fail
B6-ASSERT ROLLBACK_UNDER_60S: PASS (11s)
task 对照: itkhqqk9…(v1 Running) ← 全新任务（版本不同必须滚动）
           od2u2ueu…(v2 Shutdown) / pl7ounk9…(v1 最初任务, Shutdown)
```

**结论**：✅ 11s ≪ 60s。注意与 B2 的分工：回滚到**不同版本**是真实滚动更新（id 必变）；「零任务变动」只属于同内容归位重放（B2/pull-sf）。两层语义在实测中分得清清楚楚，设计的单层重放原语两种场景通用。

**设计影响**：release-semantics nightly「回滚（快照重放）一分钟内完成」通过；§2.4 重放语义无修正。

**重跑**：`spike\b\scripts\b6-run.bat`。

---

## 9. 意外发现（坑与数据点）

1. **Engine 29.8.1 的 `docker events` 不再输出 task 类型事件**：全程 367 行只有 container/network/service（连 `--filter type=task` 也为 0 行）。发布状态机的观测实现不能依赖 events 的 task 流，需轮询 `docker service ps` / Task API。B3 时间线因此用 container 事件 + DNSRR 重建（证据等价）。
2. **Traefik v3.5 对 `{}` 的行为是清空全部路由**（实测 404），而对 `{"http":{"routers":{},"services":{}}}` 反而报 decode 错误并保留旧配置——「空配置不落盘」的平台校验必须覆盖「键缺失/合成结果为空」，不能指望 Traefik 兜底。
3. **pause 后失败任务会被 restart-policy 反复重启**（`--restart-condition` 默认 any，约 5–10s 一轮，每轮新 task id）：paused 冻结的是 updater，不是重启监督器。归位重放会一并停掉 churn（实测 v2bad 任务转 Shutdown）。副作用：`docker service ps` 首行在 churn 期间永远是不健康版本的瞬时行——脚本轮询必须按「指定 image 的 Running 行」等待（`wait_img`），不能 head -1。
4. **优雅退出最容易写错的地方：`Serve` 返回 `ErrServerClosed` 后的 `os.Exit(0)` 会立刻杀死整个进程，使 `Shutdown` 的 drain 形同虚设**——本探针第一版就带着这个 bug 跑出了「graceful 也 502」的假结果（begin 有、done 无、exit 0 距 begin 仅 1.3ms）。修正（信号协程持有退出权，Serve 返回后阻塞等待）后同一实验 502→0。平台给用户的应用模板/文档必须把这个模式写对。
5. **Traefik 内置（Go）transport 会对幂等请求做透明重试**：后端连接死亡对 GET 几乎不可见（R1/R2 零 502），只留下时延毛刺；超过客户端超时才显形（R2 唯一失败样本是一个 3s 超时）。非幂等 POST 则如实 502。做 LB 层故障演练时必须区分这两类请求。
6. **`docker service create` 没有 `--env-add`**（那是 update 的旗标；create 用 `--env`）——脚本三次踩中，报错形态是 usage dump。
7. **`docker network inspect` 默认不显示 `.Services`**，需要 `-v`，且 Services 是 map（模板要 `{{range $n, $s := .Services}}`）；V3 第一轮因此拿到空 VIP、断言空转（已修复重跑取实证）。
8. **start-first 的切流顺序**：B3a 显示旧任务先被移出端点（healthy+1.15s），**2.2s 后**容器才 die——swarm 先摘流量再杀进程，这对优雅退出是友好顺序（SIGTERM 时已无新流量）。
9. **每次 `service create/update` 都打「image could not be accessed on a registry to record its digest」提示**：tag 引用必触发 registry 尝试（Spike A E5 结论复现）；v0.1 平台用 digest 引用则零尝试。对 spike 无害，但日志降噪应换 digest 引用。
10. **stop-first 的停机是「判定+恢复」两段**：旧任务在更新发起后 ~5s 内 Shutdown，恢复重建 10–12s——与设计 §2.6「downtime_ms 如实累计」的账目结构一致，量级可写进文档。

---

## 10. 复现指引

前置：本机 Docker Desktop；仓库根目录；cmd。全程约 30–40 分钟（b0 的 traefik 拉取视网络 1–5 分钟）。

```bat
:: 全新环境（交叉编译探针 + dind + swarm + overlay + traefik v3.5 + 5 fixture + cfgsvc）
spike\b\scripts\b0-up.bat

:: B1/V1 + B2（含字段脏检矩阵，~2 分钟）
spike\b\scripts\b1-run.bat

:: B3 三段（B3a/B3b/B3c，~5 分钟）——头号实验
spike\b\scripts\b2-run.bat

:: V3 + V4-R1（Traefik 路径，~4 分钟）
spike\b\scripts\b3-run.bat

:: V4-R2 in-flight kill 三配置（自重建 fixture，~5 分钟）
spike\b\scripts\b3c-run.bat

:: V4-R3 POST in-flight（abrupt vs graceful，含 os.Exit 修复后的探针，~4 分钟）
spike\b\scripts\b3d-run.bat

:: Traefik HTTP provider 四态（~2 分钟）
spike\b\scripts\b4-run.bat

:: 失败矩阵 3×2（~6 分钟）+ 快照重放回滚计时（~1 分钟）
spike\b\scripts\b5-run.bat
spike\b\scripts\b6-run.bat

:: 清理（dind 拆除 = 全部内部状态消失 + 宿主核查）
spike\b\scripts\b9-down.bat
```

日志（证据原文，保留入库）：`spike/b/artifacts/logs/in-b0-infra.log`、`in-b1-v1b2.log`、`in-b2-b3.log`、`in-b3-v3v4.log`、`in-b3c-v4v2.log`、`in-b3d-v4post.log`、`in-b4-provider.log`、`in-b5a-matrix.log`、`in-b5b-matrix.log`、`in-b6-rr.log`。

主仓回归（围栏验证）：`go build ./... && go test ./... ./sdk/go/... -race`。

产物索引：
- 探针 module：`spike/b/cmd/probe/main.go`（server/hc/lbwatch/client/cfgsvc/resolve/ts 七模式单二进制）
- fixture 模板：`spike/b/dockerctx/Dockerfile.fixture`（APP_VERSION/HEALTH_MODE/LISTEN_DELAY_S/CRASH_ON_START 四旋钮）
- 脚本：`spike/b/scripts/`（宿主 .bat 编排 + dind 内 in-*.sh，均为幂等可重跑）
