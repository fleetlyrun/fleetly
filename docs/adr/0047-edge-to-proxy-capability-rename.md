# ADR-0047: Edge→Proxy 词条更名——流量接入 Capability 定名机制词

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted（实施挂起：并行批落地后一次性 sweep，验收锚随批勾选） | 2026-10-05 | ADR-0007（词汇冻结与显式更名出口）、F0.15（Edge 落地面）、ADR-0044（console 生成物纪律）、ADR-0039 决策 4（flag-day 操作序文化） |

## 背景

ADR-0007 把 Edge 列入禁改清单，同时留了出口：发现词条不合适视为设计缺陷，
走显式 ADR 一次性全库更名。本 ADR 即走该出口，动因三层：

1. **同音词结构恶化**：Edge 的同音词是异域的（边缘计算、edge functions、
   Microsoft Edge、Portainer Edge Agent）——语境无法消歧，且行业对 "edge"
   的第一联想正从 traefik 时代（早年自称 edge router，近年已转以 reverse
   proxy / application proxy 自述）向 CDN 边缘计算迁移。对照 Proxy 的同音词
   （forward proxy、HTTP_PROXY、userland-proxy）全部同在"流量中介"语义域
   内，语境即消歧。
2. **中文语境放大**：本仓散文/注释/ADR 主语言是中文，Edge 的天然对译
   "边缘"在中文技术语境几乎专指边缘计算——该词最常出现的地方恰是歧义
   最强的地方。
3. **窗口期事实（2026-10-05 核查）**：kind 字符串 "edge" 零持久化
   （RegisterFactory 启动期注册，state 无此值）→ 无数据迁移；
   errcode/eventcode 注册表零含 edge；console/src 源码零含该词；CLI 动词面
   （routes/certs）本就无 edge 词；SDK 为仓内生成物，无外部 API 消费者。
   此刻是更名将来最便宜的时刻，窗口单调关闭。

## 决策

1. **定名 Proxy**（流量接入 Capability：Route 发布与证书管理——职责不变，
   仅更名）。正面理由：机制准确（traefik 即反向代理，TLS 终结天然发生在
   proxy 上；Route 协议含 tcp 的 L4 面与 traefik middleware 类未来扩展都在
   词义内——词不挡功能）；同受众先例（Coolify/Dokku/Fly 的 proxy）；仓内
   零冲突；中文对译"反向代理"为教科书词；机制词无生态寄生衰变（对照：
   借 K8s 当红词汇会随其换代漂移）。

2. **否决备选**（防重议，浓缩 2026-10-05 四轮论证）：
   - **维持 Edge**：异域同音随行业趋势恶化 + 中文语境最差（背景 1/2），
     消歧句治标不治本。
   - **Ingress**：K8s Ingress 是纯 L7 资源，而 Route 协议含 tcp（F0.15：
     http|h2c|tcp）——语义覆盖硬性不足；且与 Network 词条 egress:none 的
     方向语汇叠词。
   - **Router**：撞 traefik 动态配置内部词汇 router/service
     （routevalidate.go/traefik config.go 错误文案已在使用——"Router 把
     Route 发布成 router+service"句内自撞）；中文"路由器"另撞家用硬件。
   - **Gateway**：**仓内已占用两处且不可移除**——REST gateway
     （grpc-gateway 库通名，config/defaults.go:8、model/errcode/errcode.go:8
     注释族）与 BuildKit frontend `gateway.v0`（providers/builders/
     daemon.go:124，BuildKit 固定标识符）；且 N0–N3 清单与两份设计书均无
     流量面限流/鉴权/IP 规则承诺（唯一"限流"是 F1.9 API 受理面治理刹车），
     角色词预支无处兑现。守卫 skippedTokens 现行分诊即以 "REST gateway
     为技术术语" 保留其语境——占用是执法事实，不止注释。

3. **更名清单（一次性、全库、含 golden——ADR-0007 口径）**：
   - **词汇与守卫**：CONTEXT.md Edge→Proxy 词条（_Avoid_: edge, ingress,
     gateway, load balancer——edge 入表防回流）；Capability 正文七端口
     列表（Runtime、Builder、Registry、**Proxy**、Logging、Metrics、
     ObjectStore）；wording 守卫 edge 词条分诊入表（裸词有 edge case 等
     通用英文误伤面——进 skipped 带理由注释，或 banned 用复合形态正则
     （edge config/edgev1/Fleetly-Edge 等，机械无歧义优先），实施批定）；
     gateway 条目理由文同步（"Edge 同义词语境禁"→"Proxy 同义词语境禁"）；
     TestAvoidTokensTriaged/TestNoBannedWording 双向保鲜绿。
   - **proto 与生成物**：proto/fleetly/edge/v1/edge.proto →
     proxy/v1/proxy.proto（package fleetly.proxy.v1、go_package proxyv1）；
     mise run generate:all 全量再生成；**buf breaking 对 origin/main 必红
     （包/服务整体迁移）——本 ADR 即豁免依据**，buf.yaml breaking 例外
     逐条带注释链接本 ADR、豁免范围最小化（仅 edge→proxy 迁移面，不开
     全局口子）；console:gen（swagger→openapi-typescript）+ console:build
     （dist 同 commit，console:verify 零漂移）。
   - **capability 面**：KindEdge/"edge" → KindProxy/"proxy"；Edge 接口与
     WithEdgeAuthToken/EdgeAuthTokenFromContext 等标识符全量；
     providers/traefik 注册面与测试 fake（fakeEdge 等）。
   - **跨进程契约（flag-day 一次）**：受管 traefik 动态配置回拉 header
     X-Fleetly-Edge-Token → X-Fleetly-Proxy-Token（fleetly 与 traefik 容器
     同 commit 换代）；env FLEETLY_EDGE_CONFIG_ENDPOINT →
     FLEETLY_PROXY_CONFIG_ENDPOINT；config EdgeConfig/edge_config →
     ProxyConfig/proxy_config（DefaultProxyConfigAddr :9082 端口不变）。
     **不设兼容读层**——外部消费者已核证为零，staging 为自有狗粮，runbook
     记一次 flag-day 换装序（ADR-0039 决策 4 同款文化：存量不改不炸、
     平台动词与 runbook 承载迁移）。
   - **文案与 golden**：doctor_exposure.go（"edge config exposure" 文案、
     env 常量、端口常量注释）及其双形态 golden；README 与 skills/ 散文中
     的 Edge 字样（守卫扫面内兜底）；全部含 Edge 字样的 CLI/事件 golden。
   - **标识符长尾**：internal（engine/assembly/api/config/capability/
     providers）与 cmd 下全部 Edge/edge 复合标识符（2026-10-05 实测约
     600 处，sweep 批机械扫）。

4. **不追改历史**：docs/adr/ 已入册 ADR 与 docs/reviews/ 历史评审中的
   Edge 是不可变历史记录，原文保留（守卫扫面不含 docs/ 历史面，无执法
   冲突）；活文档（架构 §2/§11、领域模型、checklist F0.15 注记、runbook）
   同批更新并链接本 ADR。

5. **时序**：本 ADR 先行入册（决策真源）；实施批待当前并行批（数据库
   级联删除，六文件未提交）落地后执行——两批文件面无交集（docs/adr/
   新文件 vs internal/api+internal/engine），但 sweep 是全仓机械扫，必须
   在干净树上跑全门禁。

## 后果

- 用户可见面变化：配置文件 edge_config 段、FLEETLY_EDGE_CONFIG_ENDPOINT
  env、doctor 文案——升级即 flag-day（runbook 承载）；CLI 动词与 API
  资源路径无变化（routes/certs 本就无 edge 词）。
- 词汇表新增防回流项：edge 进 Proxy 词条 _Avoid_，与 ingress/gateway/
  load balancer 并列；CONTEXT.md 唯一真源机制不变。
- 守卫执法强度注记：edge 若分诊进 skipped（语境词），回流防护是人工评审
  级而非机械级——由 sweep 批的 golden 钉死与 usage 反扫兜底；实施批若能
  构造机械无歧义的复合形态正则，优先 banned。
- traefik Provider 的自我描述与 X-Fleetly-Proxy-Token 是平台自有契约面，
  无上游库约束。
- 本 ADR 落地后，"Gateway 是否更合适"的讨论以仓内占用事实（决策 2）终结；
  除非路线未来真实承诺流量面治理特性簇（限流/鉴权/IP 规则入清单）——届时
  特性先行、词条再议，Proxy 不挡功能，无需预支。

## 验收锚

- [ ] CONTEXT.md：Edge→Proxy 词条（_Avoid_: edge, ingress, gateway, load
      balancer）+ Capability 七端口正文；TestAvoidTokensTriaged 双向保鲜绿
- [ ] proto 迁移 fleetly.proxy.v1 + generate:all 零漂移（generate:verify）
      + buf breaking 例外条目最小化并注释链接本 ADR
- [ ] capability 面全量更名（Kind/接口/context helper/fake）；全仓无 Edge
      词条残留（机械扫 + usage 反扫；docs/adr 与 docs/reviews 历史文件除外）
- [ ] 跨进程契约换代：X-Fleetly-Proxy-Token / FLEETLY_PROXY_CONFIG_ENDPOINT
      / proxy_config；无兼容层；staging flag-day 换装序回写 runbook（含
      FLEETLY_EDGE_CONFIG_ENDPOINT 旧值清理注记）
- [ ] console:gen + console:build 同 commit，console:verify 零漂移
- [ ] doctor 文案与双形态 golden 更新；含 Edge 字样 golden 全量再生成
- [ ] wording 守卫：edge 分诊入表带理由；gateway 条目理由文更新；全门禁绿
      （mise run test 三 module -race + lint + generate:verify +
      console:verify）
- [ ] e2e:h2c（受管 traefik 链路含新 header）dind 演练绿
- [ ] 活文档更新：架构/领域模型/checklist F0.15 注记/runbook——历史 ADR
      与评审原文零追改
