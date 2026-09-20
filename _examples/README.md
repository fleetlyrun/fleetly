# _examples — 常用应用类型示例

面向 fleetly 受控 Compose 子集的可部署示例。**v0.1 单节点只部署节点本机已有的镜像**：
image 模式不代拉（缺镜像 → `E_IMAGE_PULL_FAILED`），先 `docker pull <image>` 预拉再部署；
带 `build:` 的源码构建模式走 git push / webhook，或 `fleetly build` 先行出镜像。

| 目录 | 应用类型 | 要点 |
|---|---|---|
| `hello-web/` | 最小无状态 HTTP 服务 | 单服务、纯 image、无域名；演示 `expose` 与资源限制 |
| `static-site/` | 静态站点 / 入口路由 | `fleetly.domains` 平台 label + 健康检查（health gate） |
| `kv-cache/` | 内部服务（缓存） | `command` 覆写 + 命名卷；无 `expose` 不对外发布 |
| `stateful-db/` | 有状态数据库 | 命名卷 + 环境变量；卷挂载使发布走 stop-first 语义 |

## 部署

```bash
export FLEETLY_ADDR=127.0.0.1:8421
export FLEETLY_TOKEN=<API token>          # 首启可用 <数据根>/bootstrap-token

docker pull traefik/whoami:v1.10                    # v0.1 需先预拉镜像到节点
fleetly validate _examples/hello-web/compose.yaml   # 本地受控子集校验
fleetly deploy   _examples/hello-web/compose.yaml   # 入队并等待终态
fleetly apps list
fleetly logs follow --service web hello-web
```

## 子集速查（详见 docs/design/2026-09-17-architecture.md §2.4）

- 支持：多服务、`build`/`image`、`healthcheck`、`environment`/`env_file`、命名卷与栈内网络、`deploy.*`、`stop_signal`/`stop_grace_period`。
- 拒绝（`E_COMPOSE_UNSUPPORTED`）：`depends_on`、`secrets`（v0.2 平台密钥库接入后开放）、外部网络、`network_mode: host`、`privileged` 等危险字段、宿主路径 bind。
- 受管字段：`deploy.update_config.failure_action` 必须 `pause`（或省略）；healthcheck `monitor` 必须省略或 `5s`。
- 变量插值关闭：`${VAR}` 与 `.env` 插值不解析，按字面处理。
- 顶层 `name` 即应用名，且必须与部署目标一致（`fleetly deploy <file>` 以文件内 `name` 为准）。

## 本地演示注意

- `static-site` 的 `site.example.com` 是占位域名：部署终态不受影响，但 ACME 证书签发需要真实公网域名与 DNS，本地环境会停在 pending。
- 应用间网络互相隔离；同 app 内服务按 compose 语义用服务短名互访（如 `redis:6379`）。
