# fleetly compose 受理语言（ADR-0074）

fleetly 的 App 部署声明语言是 **compose-spec 官方语法 + `x-fleetly-*` 扩展**，
由 [compose-go](https://github.com/compose-spec/compose-go)（Docker Compose
CLI 同源的官方参考实现）解析。同一份文件可以在本地 `docker compose up`
调试——docker 对 `x-fleetly-*` 键静默忽略（compose-spec 11-extension 的
规范义务）。

编辑器支持：`docs/reference/compose-dialect.schema.json`（draft 2020-12，
含扩展键定义；SchemaStore 社区提交挂账）。方言的封闭性在受理位执法，
schema 只做输入辅助。

## 收面

| 面 | 形态 |
|---|---|
| 服务 | `image`（必填；build 源走 git push 批次）、`command`（string/list 两形态）、`environment`（map/list 两形态；bare 条目拒） |
| 端口 | `ports: ["8080"]` 或 `{target: 8080, app_protocol: http\|h2c\|tcp}`——**target-only**；host 映射（`9090:80`、`127.0.0.1:8080:80`、`published:`）显式拒（流量走 Route/Proxy）；路由协议缺省 http |
| 副本/资源 | `deploy.replicas`、`deploy.resources.limits.{cpus,memory}` |
| 策略 | `deploy.x-fleetly-strategy: rolling\|blue-green`（官方 #extensions 通道） |
| 探针 | `healthcheck.test`（exec，CMD/CMD-SHELL）或 `healthcheck.x-fleetly-http-path` / `x-fleetly-tcp-port`，三选一互斥；节律键官方面 |
| 网络 | service 级名单引用；顶层 `external: true` 声明（见下） |
| 卷 | 短语法 `name:/target[:ro|rw]`；顶层 external 声明 |
| Secret | 名引用（值永不进 compose——`fleetly secrets put` 落库，注入 `/run/secrets/<名>`）；顶层 external 声明 |
| 首启作业 | 顶层 `x-fleetly-first-boot-jobs`（ADR-0033 形态） |

## external 声明律

平台实体（Secret/Volume/Network）经官方 `external: true` 语义引用——
"资源存在于 compose 之外"恰是平台管理实体的事实。声明键必须落在 schema
安全字符集（`[a-zA-Z0-9._-]`），带冒号的平台命名走 `name` 覆盖：

```yaml
services:
  web:
    image: app:1
    networks: [default]
    secrets: [dbcred]
networks:
  default:
    external: true
secrets:
  dbcred:
    external: true
    name: "database:pg"
```

未声明的引用由官方一致性检查拒绝（`refers to undefined ...`）。

## 插值律

官方插值开启，**环境 = 平台变量**（Project SharedVariable ∪ App 层
AppVariable，App 胜出）。未消解的 `${VAR}` 是受理错误（fail-closed）；
`${VAR:-default}` 自带回退；`$${VAR}` 转义保字面。变量在受理期消解并
冻结进 Revision。

## 拒面（官方面未开放，白名单只增）

受管语义：`deploy.restart_policy/update_config/rollback_config/placement/
labels/mode`（重启/滚动/调度是平台状态机与 Placement 域收编面）。
未开放官方面：`build`、`depends_on`、`env_file`、`profiles`、`include`、
`extends`、`configs`、`container_name`、`privileged`、udp 端口、bind/匿名卷、
secrets 值形态（`file:`/`environment:`）、网络长语法（aliases 等）、跨
Project 引用（`project:<id>/<name>` 带冒号进不了声明键——该面走 spec_file）。
上游新增字段默认落入拒绝面。

## 与 spec_file 的分立

裸 AppSpec（protojson，`deploy --spec-file`）是全量 IR 面：git 源 +
railpack/static builder、跨 Project 网络引用等 compose 表达不了的形态
住那里。两面对齐同一冻结咽喉（Revision）。
