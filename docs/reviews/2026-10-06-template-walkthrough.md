# F3.3 模板库 staging 走查（2026-10-06，9df27c9-f33tpl 换装批）

走查环境：manager fleetly-dev（05df932-f32exec2 → 9df27c9-f33tpl 升级序换装，
goose 00026 template_catalog 干净前滚）+ worker 143.198.234.68（AgentCommand
重跑，relay_online=true）。本环境无浏览器后端——渲染层由 tsc+vitest 承载
（F3.1 同口径），本报告覆盖 HTTP/消费契约级。

## 判定

**PASS**——四面全绿：升级零扰动、目录读面（CLI+REST）、实例化全链（nginx
string+domain 链 / grafana secret 链）、台账（事件/审计）。无 W 级发现。

## 证据锚

### 升级零扰动（ADR-0015 序）

- Platform Backup 前置：snapshot `013d0362`（2026-10-06T17:30:17Z）。
- 手工卷快照五份（`/root/upgrade-f33/`：acme×2/victorialogs/victoriametrics/zot）。
- 换装后 `fleetly status` = `healthy server=9df27c9-f33tpl`；journal 无 goose 报错
  （`migrated database to version: 26`）。
- 受管 db task 行零新增（`Running 8 hours ago` 不变）；首启路由冷窗（backend
  unresolved skipping route）属已知有界行为，~60s 后全恢复。
- 探针三锚换装前后原样：`tw.dev.fleetly.run` 200 / `n0.dev.fleetly.run` 200 /
  `ml-api.dev.fleetly.run` 415（预期形态）。
- worker 代理刷新：AgentCommand sed 抽取原样执行 → 双节点 `relay_online=true`。

### A：目录读面

- CLI `fleetly templates list`：`source builtin` + grafana/nginx 两目录条
  （version/description 齐）。
- REST（gateway :9081）`GET /v1/templates`：protojson 规范形态（uint64 字符串、
  digest `sha256:<64hex>`、variables 三型声明）。
- REST `GET /v1/templates/nginx`：body 原文（x-fleetly-template 声明 + compose
  子集；插值前形态，无 secret 材料）。

### B：refresh 未配置源的精确拒绝

`fleetly templates refresh` → `E_INVALID_ARGUMENT: the template catalog refresh
is disabled: server.templates_catalog_url is empty and the embedded catalog is
the whole catalog`（诚实边界面——未配置 = 内嵌目录即全部）。

### C：nginx 实例化全链（string+domain 变量链）

```
fleetly templates instantiate --project staging-tpl --app demo-site \
  --set host=tpl.dev.fleetly.run nginx
→ app demo-site created / deployment created / route created /
  deployment succeeded（66s：真镜像拉取 + 收敛 + 观察窗）/ open http://…
```
- Route 探针：`tpl.dev.fleetly.run:80 → 200`（body `Welcome to nginx!`）。
- 幂等重跑：`--json` 报告 `reused: true`（app/route），新部署行（latest-wins）。

### D：grafana 实例化全链（secret 变量链）

- `template:dash:admin_password` 落库，ListSecrets 指纹面 `02f09a47…`（值零
  回显——ADR-0014 红线在真机同执法）。
- 部署 succeeded；`dash.dev.fleetly.run:80 → 302`（grafana 登录重定向 = 应用
  活体，GF_SECURITY_ADMIN_PASSWORD__FILE 文件注入链生效）。

### E：台账

- outbox：`template.instantiated` ×3（seq 117825/117831/117839；aggregate=app；
  payload `{template, version, deployment}` 三元组）。
- events API 游标面：`--after-seq 117820 --limit 40` 窗内 3 拍可见（after_seq
  游标语义照旧——从窗头查恒是老事件，坑实录有效）。
- audit：`template.instantiate` ×3（resource=app/<id>，AfterFP=nginx@1.0.0 /
  grafana@1.0.0）。

### 走查运维脚注（非本批缺陷）

- 事件尾被 `database.backup_failed` 洪水覆盖（既有面，备份失败每拍轰炸——
  处置另行）；走查台账取证经 outbox 直查。
- 走查资源已清理（routes/apps/project 删除；`template:dash:*` secret 行随
  项目级材料保留——与 database: 凭证同口径）。

## 浏览器级补档（2026-10-07，Console Templates 页）

后端在场环境（ZCode IAB，bundle `index-CSOhUoLd.js`）补做 Templates 页浏览器级走查：

| 面 | 结果 | 活体锚 |
|---|---|---|
| 目录列表 | ✅ | `Templates — catalog source: builtin` 标题 + secret 平台生成注记 + grafana/nginx 双条目（version/description/show·instantiate） |
| 详情弹窗 | ✅ | nginx：变量声明 `host (domain, required)` + digest `sha256:8334…` + 模板体原文（x-fleetly-template 头 + routes var 引用 + compose 服务）只读渲染 |
| 实例化向导 | ✅(带注) | 表单渲染全（Project 下拉/App name 复用注记/host 变量字段带 hint）；**提交面当时被 Console Modal 嵌套 form 缺陷（W1）拦截**——实例化经 CLI 完成，向导 UI 提交随 W1 修复批（`6ff4f08`，换装 6ff4f08-w1fix 后 Modal 表单提交活体恢复） |
| 实例化部署详情 | ✅ | CLI 实例化的 nginx 部署在浏览器详情页渲染（`app demo-site · succeeded`，DNS 名 `web · web.demo-site · web.g1`，from_revision `—`）；Route `tplb.dev.fleetly.run:80 → 200` |

- **grafana 详情死窗**：本批走查发现 Templates 详情弹窗 Escape/关闭后再开无响应——Console Modal 受控态失同步（W3），同批修复（`6ff4f08`）；修复后 close 事件同步 + 再开成功活体复验（详见 F3.1 报告补档节 W3 条）。
- **W2（挂账）**：`templates instantiate --project` 传项目 **ID** 被按名字 get-or-create 静默建幽灵项目（名字=裸 ID，实测 `01M49WV6C3…` 项目 + demo-site 落入）；与 `apps create --project` 只收 ID 的语义相反。走查残留已清理；名字/ID 解析归一挂后续批裁决。
