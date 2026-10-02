# 构建器扩展：railpack（钉版）与 static（F1.14）

F1.14 裁决。BuildSpec.builder 注释早列三名（dockerfile / railpack / static，CONTEXT.md Builder 词条单源），但 F1.11 止 strategy oneof 只有 dockerfile|railpack 且 railpack 字段全仓零消费、static 连字段都没有——本批把两个从空面接成真面。外部事实（2026-10-02 核证）：railpack 是 Rust 外部构建器（plan 生成型，Nixpacks 后继），生产推荐链 = CLI `railpack prepare` 产 plan + BuildKit `gateway.v0` 前端镜像 `ghcr.io/railwayapp/railpack-frontend` 执行（CLI 自带 build 走 docker load 管道，官方自评不适合生产吞吐，弃用）；发布资产 `railpack-v<ver>-<target>.tar.gz`（musl 静态二进制）。

## 决策

### 1. Builder 是 spec 路由家族（ADR-0003 措辞精确化）

七端口中其余六端口维持"平台配置单选在册"；**Builder 改为家族在册、spec 路由**——三 Provider（dockerfile/railpack/static）全部注册全部装配，`BuildSpec.builder` 是路由键。注册表机制不动（本就 Kind→名→工厂多名）；装配面从"取唯一在册者"改为"构造全部在册者"传入 engine（map 名→Builder）；engine 按 Revision 冻结体的 strategy 分派。与 ADR-0003 不冲突的依据：CONTEXT.md Builder 词条本就列举多名，"同期唯一在册"的立法意图是防"同一 Capability 两套在役真源"，Builder 家族三名是**一个构建能力的三种策略前端**，spec 钉哪名用哪名，不存在双真源。

### 2. 包形态：builders 单包三 Provider

`internal/providers/dockerbuild` 更名 `internal/providers/builders`，单包承载三 Provider（dockerfile.go / railpack.go / static.go）+ `daemon.go` 共享机械。理由：import 守卫规定 providers 只准 import capability/spec/model——三 builder 共享 daemon 内嵌 buildkit Solve（session exporter → moby 导入）+ ImagePush + digest 提取 + 进度流机械（原 dockerbuild/build.go 收编为 daemon.go 唯一真源）若跨包复用必改守卫清单；包内共享不触"providers 互不 import"（那是包间纪律，语义是 Provider 互不依赖彼此的存在）。三 Provider 仍各自独立实现 Builder 端口、各自工厂自注册、各自 Describe/Health。

### 3. railpack 调用形态：钉版宿主二进制 + 钉版 frontend 镜像（双侧钉版）

- **prepare = 宿主二进制**：`railpack prepare <ContextDir> --plan-out <planDir>/railpack-plan.json`。二进制路径 env `FLEETLY_RAILPACK_BIN`（缺省 PATH 上的 `railpack`）；install.sh 钉版下载（musl 静态资产，见决策 5）。exit 1=永久失败（无 provider 等，stdout 进构建日志）、exit 75=瞬态（错误文本建议重试 deploy）；产物 plan 落 per-build 临时目录（不污染幂等上下文目录——"解包是纯函数"不变式保持）。
- **执行 = daemon 内嵌 buildkit `gateway.v0`** + FrontendAttrs `source=ghcr.io/railwayapp/railpack-frontend:v<pin>`；plan 文件经 dockerfile mount（`filename=railpack-plan.json`，与 buildx `BUILDKIT_SYNTAX`/`-f plan.json` 同语义）、用户目录经 context mount；`build-arg:cache-key` = 推送目标的 repo 前缀（per-app mount 缓存隔离，跨 revision 稳定——BuildID/序号 tag 都不稳定，repo 前缀稳定）。
- **产物通道复用**：moby exporter 导入本机 daemon → daemon ImagePush → manifest digest aux 回填（与 dockerfile 完全同机械，ADR-0019 附录 B.6）。

### 4. 钉版执法面：平台常量单源，双侧执法

平台常量 `railpackPinnedVersion = "0.39.0"`（ADR-0021 归档验证基线 = tech-stack 表 0.39.0 = 现行 frontend latest 同版；升级走 Platform 升级序 ADR-0015，**无 env 覆写**——钉版可被环境变量漂移等于没钉）。

- **操作者侧**：Provider 构造期探测二进制版本（`railpack --version` 输出末 token 去 `v` 前缀），≠常量 → Health 不健康（doctor 面）+ Build 精确失败；二进制缺席 → 同款诚实降级（Provider 仍在册，railpack 构建精确失败，dockerfile/static/镜像直投不受影响）。
- **用户侧**：`RailpackBuilder.pinned_version` 必填 bare semver（叶子校验，拒 `latest`/空/带 v 形态），≠常量 → Build 精确失败（错误文本带平台版本与 CLI 旗标示例）。spec 再钉一版的理由：Revision 可重放重建（崩溃回 queued 重跑），构建结果必须可归因到"产生该 plan 的 railpack 版本"——spec pin 是 Revision 冻结体的复现锚，平台常量是执行锚，两锚相等是复现性成立的充要。frontend 镜像 tag 由 spec pin 派生（`:v<pin>`），plan 与执行器同版由同一字段保证。
- **同 commit 纪律（静态执法）**：install.sh 下载 URL 的版本段与代码常量一致——guards 断言 install.sh 含 `railpack-v<常量>` 字面量，改版本不改脚本即红。

### 5. install.sh：钉版下载 railpack（Linux 控制面）

```
railpack-v0.39.0-x86_64-unknown-linux-musl.tar.gz（amd64）
railpack-v0.39.0-arm64-unknown-linux-musl.tar.gz（arm64）
```
装 `/usr/local/bin/railpack`（0755）。失败不阻断安装（打印指引——railpack 部署会精确失败，其余能力不受影响）。dev 环境 mise registry 无 railpack（2026-10 核证），开发机按需 curl 同款资产或设 `FLEETLY_RAILPACK_BIN`；测试全部走 exec 接缝假底座，不依赖本机二进制。

### 6. static 形态：产物目录 + 钉版 Caddy 包装（无构建步）

- **语义**：上传上下文的产物子目录（`output_dir`，缺省 "."）整体 COPY 进钉版 `caddy:2.11-alpine`（/srv），生成 Caddyfile（`:8080` + `try_files {path} /index.html` SPA 回退 + `file_server`）经 Dockerfile heredoc COPY 注入 `/etc/caddy/Caddyfile`——不写用户上下文目录（幂等不变式保持）、不需要 named context。overlay 临时目录只放生成的 Dockerfile（挂 dockerfile mount）。监听 8080 常量。
- **源码构建不属 static**（`npm run build` 之类是 railpack 的地盘）——static 是"已构建产物的托管伺服"，诚实边界写进 Describe Notes。
- **镜像钉版**：`caddy:2.11-alpine`（major.minor + 变体档——zot 全 patch / postgres major+suite 之间的中间档：file-server 用途面窄，minor 档平衡安全更新与复现）。
- **叶子校验**：output_dir 相对路径、拒 `..`、拒绝对路径（路径逃逸 fail-closed，tar-slip 同理）。
- **端口声明面**：static 产物监听 8080，但 upload 形态本无端口声明旗标（Route 投影需要 PortSpec）——与 dockerfile upload 同款既有缺口，随 API 扩展批（spec_file）收口，不在本批。

### 7. proto（only-add）

- `BuildSpec.strategy` + `StaticBuilder static = 5`（`cache_from = 4` 已占号）；`StaticBuilder{ string output_dir = 1; }`。
- `DeployRequest` + `builder = 12` / `railpack_version = 13` / `output_dir = 14`——upload 形态专属（image/compose 形态携带即拒；builder 缺省 dockerfile）。
- webhook 面不动（hook 恒 dockerfile）；git 源 railpack 需求出现时再扩 SetGitHook 面（F1.15 若需即先落）。

### 8. intake 与端口扩展

- CLI `deploy --builder dockerfile|railpack|static`（--from-dir 专属，缺省 dockerfile）+ `--railpack-version`（builder=railpack 必填）+ `--output-dir`（builder=static 可选）。
- `spec.UploadDeploy` 扩为接三 strategy 组装；新增叶子 `ValidateBuild` 挂 `ValidateApp`：builder 名值域 = 词条三名单源冻结（dockerfile/railpack/static，叶子无注册表面，值域漂移=词条变更=走 ADR）、builder 名与 strategy oneof 配对执法、pinned_version/output_dir 格式。
- `capability.BuildRequest` + `Builder`（路由名）+ `Railpack *{PinnedVersion}` / `Static *{OutputDir}`（nil = 非该 strategy）；engine `Deps.Builders map[string]Builder` 替换单 Builder；strategy oneof 缺席的存量 Revision 防御性按 dockerfile 路由（存量全部是 dockerfile）。

### 9. 零新面承诺

零新 RPC / errcode / eventcode（F1.11 同口径：注册表即所见，railpack Describe Notes 带平台钉版版本与二进制要求；叶子校验=E_INVALID_ARGUMENT，构建失败=错误文本）。skills deploy-diagnose 增 railpack/static 分诊行（围栏守卫同 commit 喂食）。

### 10. 诚实边界

- `gateway.v0`+frontend 与 heredoc COPY 的真机实证随 F1.15 staging（单测/守卫全绿先行——F1.11 推送链同纪律）。
- railpack secrets 面（prepare `--env` 名单 / `secrets-hash` / `github-token` attr）不进本批；`cache_from` 字段仍无消费者（占位不动）；多 arch 延后（B.5④ 口径不变）。
- railpack plan 缓存（同源同版跳过 prepare）不做——prepare 是纯源码分析，成本可控，plan 每构建重生成。

## 验收锚（同批落地 2026-10-02，证据=测试在树；真机实证随 F1.15 staging）

- [x] 叶子 ValidateBuild：builder 名值域与 strategy 配对、pinned_version 格式（拒空/latest/v 前缀）、output_dir 穿越/绝对路径拒绝（spec 单测 TestValidateBuild）
- [x] UploadDeploy railpack/static 归一化产物断言（BuildSpec.strategy 单源形态；spec 单测 TestUploadDeployStrategies）
- [x] engine 路由：三 strategy 的 BuildRequest 分派断言（Builder 路由名 + strategy 载荷）+ 未知 builder 精确终态失败 + 存量无 strategy 行防御性 dockerfile 路由（engine 单测 buildroute_test 三件）
- [x] railpack Provider：版本探测解析、spec pin≠平台常量执法（错误文本带平台版本）、frontend ref 组装 `ghcr.io/railwayapp/railpack-frontend:v<pin>`、cache-key 推导=repo 前缀、prepare 命令构造与 exit 1/75 映射、二进制缺席精确失败（runCmd 接缝假底座 railpack_test 六件；cache-key/attrs 组装钉在 Build 路径——daemon Solve 前置检查使纯单测可达）
- [x] static Provider：生成 Dockerfile/Caddyfile 黄金文本（heredoc 形态、caddy 钉版 tag、output_dir 内插）、output_dir 校验复执（static_test 三件）
- [x] apitest：upload+builder=railpack/static/dockerfile 全链（FakeBuilder 捕获路由名与载荷）+ builder 面互斥执法八形态（builders_test 两件）
- [x] CLI golden：deploy from dir railpack/static 双形态进业务流 golden（组清单钉面）
- [x] install.sh 版本一致守卫：guards TestRailpackPinConstantAndInstallerAgree（常量抽取 + URL 模板与 RAILPACK_VERSION 字面量双断言）
- [x] schema golden：build oneOf 三选一（explain/schema-json/assembly selfdescription 三处 golden 同 commit）
- [x] skills：deploy-diagnose railpack/static 分诊行 + 两条围栏过 TestSkillsFencedCommandsResolve
