# Console IA v3 二期⑤b 换装 + 真机浏览器走查报告

日期：2026-10-10；对象：`a483335-iav3p2b` 换装后的现役 staging（fleetly-dev 单机，goose 28 零迁移）。
范围：二期⑤b 四件（DB 密码轮换 / zot catalog 代理 / Volume 删除 + 磁盘水位 / Variables 暂存式编辑流）+ W-1/W-2 crumb 修复 + Storage reskin 的首轮真机浏览器验证。

## 换装实录

- 五步序照 runbook：前置 Platform Backup `e384e66f` + 十卷 tar（torchwood-pg/redis-data/r1/minio-data/zot×2/VL/VM/acme×2 至 `/root/upgrade-iav3b/vols/`，39G 盘余量）+ 旧二进制留存（`fleetlyd-7a74b9f`）→ SIGTERM 排水 → 二进制替换（本机 `mise build` 同款 ldflags，版本钉 `a483335-iav3p2b`，fleetlyd + fleetly 双件）→ 起服。
- **零迁移**（goose 停 28）、install.sh 零变更、config 键面零新增。
- drop-in 五件全存活（10-logging/11-metrics/browse/railpack/registry）。
- 健康核对：`fleetly status` healthy、doctor **10 ok / 2 warning / 0 failed**（warnings 为通知渠道建议，既有）。
- traefik 载体未滚动（Up 34h 跨越换装）——路由发布面零扰动。

## 换装排障实录（假警报三重奏，记入剧本）

换装后公网探针 `https://tw.dev.fleetly.run` 恒 000，逐层排查后确认**路由健康、探针姿势错**：

1. 本机 curl 走了已死的本地代理（127.0.0.1:10801）→ 000。
2. manager 本机 curl 严格校验 TLS：本环境 ACME caserver 钉 LE **staging**（测试口径），系统信任库无 staging 根 → 证书校验失败 → curl 报 000。**正确姿势 = `curl -sk`**。
3. `http://127.0.0.1:80` 带 Host 探针 404：带 `tls:` 段的路由只在 443 服务（traefik v3 正常行为），80 探针必 404。**正确姿势 = `--resolve host:443:127.0.0.1` + `-k`**。
4. 排查中引入的对照面：9082 proxy/config 内容核对（6/6 routers 在册）、traefik insecure-api 诊断容器（routers `enabled` + `using:["web"]`）、`openssl s_client` 证书链（CN 正确）。
5. 终验：manager 回环 `-k` 下 tw.dev / n0.dev 双 200。

**剧本沉淀**：staging 路由探针 = `curl -sk -o /dev/null -w "%{http_code}" --resolve <host>:443:127.0.0.1 https://<host>/`；勿用 80 探 TLS 路由；勿信带本地代理的 shell（`--noproxy '*'` 或对端执行）。

## 走查结果：PASS（浏览器真机，token walk-iav3p2b 走查毕即吊销）

| 面 | 结果 | 证据 |
|---|---|---|
| W-1 顶栏 crumb 项目名 | ✅ | Projects / torchwood / Apps / Storage 全链项目名（目录加载窗口期裸 ID 闪现 ≤2s，见 W-5） |
| W-2 内容区 crumb 首段项目名 | ✅ | App 详情 "n0reg / 01M3SGPMMW…"（项目名≠app 名区分度实证）；DB/Task 详情同款 ContentCrumb |
| Variables 暂存式编辑流 | ✅ **真机全链** | n0reg web：Add variable → staged 徽标 + 卡片描边 + Deploy changes 脏态门启用 → 提交 → **R3 部署 succeeded** → 冻结 spec 含 WALK_NOTE → Remove → R4 还原 succeeded。spec_file 第四源零 proto 兑现 |
| DB 密码轮换（数据面方言） | ✅ **真机全链** | 一次性 walkpg（postgres）：确认页级联披露（无引用方诚实文案）→ Rotate → gRPC 1.0s 完成 → **outbox seq 118025 `database.credentials_rotated`** + Secret 指纹同刻变更（utility ALTER USER 数据面全链真机绿）。真实库 torchwood-pg 的确认页点名引用方 "torchwood, torchwood" 后 Cancel（不动真库） |
| Volume 删除 | ✅ **真机全链** | walk-vol：v2 Dialog 创建 → AlertDialog 确认（runtime 侧留存披露）→ 行消失。服务端拒绝面 CLI 实证：删 torchwood-pg 卷 → E_CONFLICT 点名 "database torchwood-pg (carrier data volume)" |
| 磁盘水位 | ✅ | Managed Providers 页 Node disk watermark：fleetly-dev 水位条 "48% used"（与 CLI 探针 47.3% 一致，cadvisor 真数据，node→hostname 反解） |
| zot catalog 代理 | ✅ | CLI + console 双面：torchwood/n0reg catalog 诚实空态；tags 对不存在的 in-scope 仓名精确拒绝（zot NAME_UNKNOWN 透传，见 W-6） |
| Storage reskin | ✅ | v2 表格/token 类/RelativeTime 截图确认；挂载判据 in use / delete 分列正确 |
| 演练收尾 | ✅ | walkpg 删除（载体拆、卷+secret 按留存语义在册）、walk-vol 删、token 吊销；R3/R4 双部署 succeeded 全程 web 服务零中断感知 |

## 走查发现（W 项，均非阻断）

- **W-3**：指向**不存在 app** 的 Variables 页渲染 react-query 内部错误文本（`["resources","app-spec",…] data is undefined`）——GetAppSpec 404 → queryFn 返回 undefined → TanStack v5 判 error。应走 EmptyState（"No frozen spec yet"）。修复：404 时返回 `null` 并以 `data === null` 判空，或 queryFn 抛出结构化错误。随批 7 修。
- **W-4**：轮换成功后确认框不关（toast 已弹、数据已换、框还开着）——Rotate 的 AlertDialogAction `preventDefault` 后 onSuccess 未关框（Delete 同款写法靠跳页掩盖）。修复：受控 open 态 + onSuccess 关框。随批 7 修。
- **W-5**：目录（projects）查询加载窗口期（≤2s）顶栏 crumb 闪现裸 ID 截断（回退文案工作正常，仅瞬态）。可接受；如需消除可在 loading 期显示 "…"。
- **W-6**：registry tags 对 in-scope 但不存在的仓名返回 E_INTERNAL（zot NAME_UNKNOWN 404 裸包）——应映射 E_NOT_FOUND。修复：content.Tags 上游 404 → `state.ErrNotFound`。随批 7 修。
- **W-7**：Storage 页 Usage index（"torchwood-pg unmounted"）与 Volumes 行（"in use"）口径不一致——前者只扫 App spec 引用，后者含 Database 挂靠判据。语义都对（Usage index 是 App 引用视角）但同屏矛盾；建议 Usage index 增 Database 挂靠行或加注。随批 7 修。
- **数据观察（非本批引入）**：`n0.dev` 路由指向 tombstone app `01M3STKQPS…`（apps 列表已无此 app，routes 仍挂）——路由悬挂。修法：删该 route 或路由受理面校验 app 存活。另：CLI secrets 组无 delete 动词（API 有 DeleteSecret）——CLI 缺口记账。

## 边界

- 浏览器走查覆盖：登录态全站导航 + Variables/DB Settings/Storage/Providers/Registry 五面操作级 + 双截图（Providers 水位、Storage reskin）；CLI 对拍补 registry/reject/事件面。
- walkpg 残留：credential secret `database:walkpg` 与卷行 walkpg 按平台留存语义在册（删库路径明示保留）；清理 = DeleteSecret API 调用 + 卷行 tombstone（CLI secrets delete 缺口修复后可直接 CLI）。
