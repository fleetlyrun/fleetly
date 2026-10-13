# ADR-0073: Git 仓库访问材料——GitHub App 一等集成与通用 Secret 通道

日期:2026-10-13;状态:已采纳(阶段 B 同批落地 B1–B8;阶段 A 随后批)。

## 背景

私有 Git 仓库是结构性缺口:受理面只收 https:// 直连 URL(`validateGitRepo`,hooks.go),clone 路径(`engine.cloneSource`)是裸 `git clone`,无任何凭据注入。私有仓今天唯一"能用"的形态是把 token 内嵌进 repo URL——该形态把材料冻结进每条 Revision,并经 GetRevision / GetAppSpec / CLI `apps spec` / console Spec tab 四读面明文可见,与"材料永不进视图"(ADR-0071 决策 5)和"值永不回显只回指纹"(Secret 词条)冲突。这是补丁,不是机制。

业界形态参照(dokploy 源码核查,2026-10-13):"平台持钥、构建期注入"是主流——GitHub 走 GitHub App 安装(库里持 App 私钥,部署期现铸 1h installation token 拼进 clone URL),通用 Git 走 SSH key 实体,而其自定义 HTTP 通道无凭据面。fleetly 已有全套材料执法(age 信封 `material.Cipher`、读面指纹 SQL 层 NULL 投影、KEK rewrap、构建出口单点脱敏 P11/ADR-0040),缺的是把 Git 克隆接进这套定律。

## 决策

1. **总定律(不变式)**:repo 以干净 URL 冻结进 Revision GitSource;访问材料永不进冻结体与任何读面;构建期(clone 前)按仓库 host+属主解析材料,明文只进 clone argv(`url.UserPassword` 转义),出口单点脱敏(stripSecret + redactTable 登记)。解析优先级:**Project 级 GitHub App → Global GitHub App → `git:<host>` Secret(阶段 A)→ 匿名 clone**;实体命中而材料不可用 = 部署精确失败(不静默降级匿名——与 ADR-0036 凭证铸造失败不降级同口径),无任何命中才匿名(公有仓合法形态)。

2. **GitHub App 实体(阶段 B,新聚合 `github_apps`)**:字段 app_id、installation_id、account_login(安装属主,**系统填充不可写**)、api_base(缺省 https://api.github.com,支持 GHES)、private_key 密文(age 信封)+ 指纹。双作用域:project_id 非空 = Project 级(行级 Project 授权,项目成员管理);project_id 空 = 平台级 Global(平台管理授权,全项目可用)。唯一性:Global 按 account_login,Project 按 (project_id, account_login)(均 WHERE 未删);解析时 Project 级遮蔽 Global。实体属接入材料,**不进 ADR-0072 所有权分裂面**(MergeHookOwned 不动);webhook 侧沿用 GitHook 既有 flthook_ 通道,零改动。

3. **受理即验证(Owner 自动解析)**:SetGitHubApp 落库前实调 GitHub API——私钥签 RS256 JWT 换 installation 访问,读 installation 对象校验三元组(app_id 匹配、installation 存在)并取 account.login 入库;Owner 不设用户输入面。错值在受理面精确报错(E_INVALID_ARGUMENT 带原因),不等到首次构建;上游不可达 = E_GITHUB_UPSTREAM(REST 502)。工具链:golang-jwt/jwt/v5(仅签名单向)+ stdlib HTTP,不引 go-github。

4. **installation token 现铸与缓存**:构建命中实体时铸 1h token(RS256 JWT → POST /app/installations/{id}/access_tokens),进程内缓存至到期前 5 分钟,缓存键 = 实体 id + 私钥指纹(轮换/删除自然失效)。token 只进 clone argv,入 redactTable 与 stripSecret 脱敏面。SetGitHubApp/DeleteGitHubApp 入 FrozenVerbs(与 SetGitHook 同桶);scopeguard 新 resource `github_app`;写面走既有幂等键机制。

5. **console/CLI 面(视觉真源 = `docs/design/2026-10-13-github-app-material-prototype.html`,实现逐屏回照)**:Project 级管理卡 = Registry 页第三 tab;平台级 = Fleet 段 GitHub Apps 页;表单三字段(App ID / Installation ID / 私钥)+ Advanced(api_base),默认收起;行内 Repos 面板 = ListGitHubAppRepos 现铸列仓,兼作安装健康验证面。CLI `github-apps put/list/delete/repos`(`--project` 与 `--global` 互斥必一;私钥 `--private-key-file`/stdin;输出只回指纹)双形态 golden。

6. **阶段 A(随后批,本 ADR 预留解析位)**:通用通道 = Project Secret 保留名 `git:<host>`(值 JSON username/secret,镜像 `registry:<host>` 定律,ADR-0019 附录 B.3 同族),解析序第三位。同批收紧受理面:hook `validateGitRepo` 与 spec 校验(git source)两写面拒绝 URL 内嵌 userinfo——封补丁通道(存量已冻结行不受影响,纵深防线 argv 照旧,同 P0 注释口径)。SSH/deploy key 不做:受理面 P0 法线(ssh/file/ext 拒绝)不动,https+token 覆盖 GitHub/GitLab/Gitea。

7. **词汇**:实体定名 **GitHub App**(CONTEXT.md 新词条,零 Avoid 变更)——Provider 词条已被"Capability 的具体实现"占用,更名即设计缺陷。

## Consequences

- 构建期新增对 api.github.com(或 GHES api_base)的运行时依赖:命中实体而上游不可达 = 部署精确失败(E_GITHUB_UPSTREAM 带原因);未命中实体的仓库行为与今天完全一致。
- 用户需在 GitHub 侧手动创建/安装 App(dokploy 同款门槛);skills 散文与 console Install guide 提供指引;GitHub App manifest 自动建流后续再议。
- `admin rewrap` 扩展覆盖 github_apps(私钥重封),KEK 轮换仍单命令。
- 阶段 A 起,URL 内嵌 token 的 repo 形态被两写面拒绝;此前描述的"唯一通道"退役。
- W-8(hook 无删除通道)维持独立挂账,不入本批。

## 验收锚

- [x] internal/github 单测:JWT 签名形态、installation token 铸造、缓存命中/过期重铸、坏 PEM、受理即验证的安装解析(httptest 假 GitHub,零真网;签名用公钥完整验签——client_test)。
- [x] state 单测:双作用域唯一索引行为、fingerprint 读面密文 NULL 投影、软删;rewrap 回归覆盖 github_apps 两态行(admin/rewrap_test 全流程)。
- [x] apitest:SetGitHubApp 受理即验证(TLS httptest api_base;错配/404 E_INVALID_ARGUMENT 精确原因;上游不可达 E_GITHUB_UPSTREAM → REST 502 映射,errcodeToHTTP 覆盖守卫钉死);Global 行非平台授权精确拒;List/ListRepos 授权随作用域;冻结/scope 分诊回归(freezeguard/scopeguard 自动对账)。
- [x] engine 解析单测:两态命中 / Project 遮蔽 Global / host 失配 / 无匹配匿名 / 铸造失败精确失败 / argv 与错误输出无明文泄漏(gitmaterial_test 表驱动 + cloneErr 脱敏端到端)。
- [x] CLI golden 双形态 ×4 动词 + 组帮助(github-apps*.golden 十三件);`generate:verify` / `console:verify` 零漂移(提交门禁复绿)。
- [x] 阶段 A 补锚:engine `git:<host>` Secret 解析单测(命中/username 缺省/坏 JSON 精确失败/被 GitHub App 遮蔽);apitest 两写面 userinfo 拒(hooks 合法形态翻转 + spec_file git 源负例/正例)。
- [ ] staging 真机:GitHub 建 App → 安装 → put(双态各一)→ hook push → 现铸 token → clone → build → deploy 全链绿;读面(GetRevision / apps spec / console)repo 恒为干净 URL。〔挂账:需用户在 GitHub 侧建 App/安装——操作序已入 runbook「GitHub App 接入」节;代码面锚全绿(cff7887/edce1b0/8226734/00b15a9 + A 段 6a65372/5bc0fdc)。〕
