package guards

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 词汇禁词扫描（ADR-0007）：CONTEXT.md 的 _Avoid_ 表是唯一来源。三张表：
//
//   - banned：机械可判、无歧义的禁词（词边界正则，命中即红）；
//   - skipped：语境限定词（机械扫描不可判定，人工评审把关），每条带理由；
//   - adrDirect：ADR-0007 点名但 CONTEXT.md 未列的禁词（如 duty）。
//
// 双向保鲜：新增 _Avoid_ 词条未进 banned/skipped 即红（强制分诊）；
// skipped/banned 里的词条从 CONTEXT.md 消失也红（清走死条目）。
var bannedPatterns = map[string]*regexp.Regexp{
	// ADR-0007 点名（归档仓历史词汇）。
	"duty":         wordRe(`duty|duties`),
	"substrate":    wordRe(`substrate`),
	"ingress":      wordRe(`ingress|ingresses`),
	"orchestrator": wordRe(`orchestrator|orchestrators`),
	"apply":        wordRe(`apply|applies|applied|applying`),
	// Team/Project/Environment 词条。
	"organization": wordRe(`organization|organizations`),
	"tenant":       wordRe(`tenant|tenants`),
	"workspace":    wordRe(`workspace|workspaces`),
	// Process/Database/Builder/Managed Provider 词条。
	"dyno":      wordRe(`dyno|dynos`),
	"addon":     wordRe(`addon|addons`),
	"buildpack": wordRe(`buildpack|buildpacks`),
	// Owner Lease 词条。
	"heartbeat": wordRe(`heartbeat|heartbeats`),
	"keepalive": wordRe(`keepalive|keepalives`),
	// Task 词条。
	"one-off": wordRe(`one-off`),
	// Drift/Converge/Replay/Backup/Generation 词条。
	"divergence": wordRe(`divergence`),
	"dirty":      wordRe(`dirty`),
	"heal":       wordRe(`heal|heals|healed|healing`),
	"repair":     wordRe(`repair|repairs|repaired|repairing`),
	"revert":     wordRe(`revert|reverts|reverted|reverting`),
	"dump":       wordRe(`dump|dumps|dumped|dumping`),
	"epoch":      wordRe(`epoch|epochs`),
	// Route/Edge 词条。
	"vhost":         wordRe(`vhost|vhosts`),
	"load balancer": wordRe(`load[ -]balancer|load[ -]balancers`),
	// Logging/Metrics/Build Log 词条（ADR-0040 入册；Logging 与 Metrics
	// 原合并词条分立）。
	"log pipeline": wordRe(`log[ _-]pipelines?`),
	"build output": wordRe(`build[ _]outputs?`),
	// Alert Rule/Notification Channel 词条（ADR-0041 入册）。
	"alarm": wordRe(`alarm|alarms`),
	"sink":  wordRe(`sink|sinks`),
	// Token 词条。
	"api key":        wordRe(`api[_ -]?key|api[_ -]?keys`),
	"pat":            regexp.MustCompile(`\bPAT\b`),
	"vault entry":    wordRe(`vault[ _]entr(?:y|ies)`),
	"password store": wordRe(`password[ _]store`),
	// Invitation 词条（C4 入册）。
	"signup link": wordRe(`signup[_ -]link|signup[_ -]links`),
	"invite code": wordRe(`invite[_ -]code|invite[_ -]codes`),
	"share link":  wordRe(`share[_ -]link|share[_ -]links`),
	// Acceptance 词条（ADR-0024 入册）。lynx 的 WithMiddleware 是库 API
	// 标识符（词边界内嵌不命中）；泛称义（把受理位叫 middleware）命中即红。
	"middleware":       wordRe(`middleware|middlewares`),
	"validation layer": wordRe(`validation[ _]layer`),
}

// wordRe 构造大小写不敏感、词边界的匹配器（多形态以 | 预展开）。
func wordRe(forms string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(?:` + forms + `)\b`)
}

// adrDirect 是 ADR-0007 正文点名、但 CONTEXT.md _Avoid_ 未收录的禁词
// （banned 表来源补充；词条撤销时同步清理）。
var adrDirect = map[string]bool{"duty": true}

// skippedTokens：语境限定词分诊表（token → 理由）。机械扫描会大量误伤
// 的技术通用词在此登记；人工评审时按 CONTEXT.md 语境把关。
var skippedTokens = map[string]string{
	// ---- 组织与访问 ----
	"account":    "User 同义词禁用仅限身份实体语境；账目/解释义（account for）为通用英文",
	"member":     "常见集合词（members list）；User 实体语境人工评审",
	"permission": "grpcapi authz 注解面（MethodPolicy.Permissions）为库契约",
	"policy":     "grpcapi authz PolicySet 为库契约",
	"grant":      "动词义（grant a scope）通用",
	"capability": "授权义禁用；Capability 是 fleetly 冻结实体（七类端口）",
	"credential": "DatabaseSpec.credentialsRef 为架构 §4 契约字段；Token 同义词语境人工评审",
	// ---- 结构 ----
	"namespace":        "架构 §5 Runtime 契约使用 NamespaceRef（Provider 侧隔离域）；Project 同义词语境人工评审",
	"group":            "通用词（route group 等）；Project 同义词语境人工评审",
	"stage":            "构建阶段等通用义；Environment 同义词语境人工评审",
	"profile":          "配置 profile 通用义；Environment 同义词语境人工评审",
	"environment":      "仅禁作实体名；gRPC/部署环境等通用义合法",
	"env":              "仅禁作实体名缩写；环境变量（env var）为技术通用词",
	"service":          "gRPC service / docker service 技术语合法；App 同义词语境人工评审",
	"stack":            "通用词；App 同义词语境人工评审",
	"workload":         "Workload 是 fleetly 冻结实体（Runtime 最小执行单元）；仅 App 同义词语境禁",
	"site":             "通用词；App 同义词语境人工评审",
	"container":        "技术载体词合法（平台不感知载体形态，但描述载体时用）；Process 同义词语境人工评审",
	"env var":          "仅禁作标识符（变量名/字段名）；注释与文档语境合法",
	"parameter":        "通用词",
	"setting":          "通用词",
	"job":              "firstBootJobs 为架构 §4 契约字段（部署期 init）；Task 同义词语境人工评审",
	"run":              "Run 是 fleetly 冻结实体；仅 Task 同义词语境禁",
	"function":         "通用词（函数）；FaaS 义在明确不做清单",
	"agent":            "AI Agent 是产品核心语汇；仅 Task 命名语境禁",
	"renewal":          "泛指禁用；证书/租约续期语境人工评审",
	"sandbox net":      "Task Network Group 同义词；短语机械扫描误伤面大，人工评审",
	"task net":         "仅禁作标识符",
	"execution":        "通用词；Run 同义词语境人工评审",
	"attempt":          "通用词；Run 同义词语境人工评审",
	"instance":         "实例池（resident instances）为 ADR-0012 契约语汇；Run 同义词语境人工评审",
	"cron":             "仅禁作实体名；robfig/cron 解析器为选型依赖",
	"timer":            "通用词；Schedule 同义词语境人工评审",
	"db instance":      "短语；Database 同义词语境人工评审",
	"service instance": "短语；人工评审",
	// ---- 交付 ----
	"repo":     "git repo 通用义；Source 同义词语境人工评审",
	"compile":  "通用词；Build 同义词语境人工评审",
	"pipeline": "通用词；Build 同义词语境人工评审",
	"ci":       "CI 流水线明确不做；指外部 CI 时通用",
	"version":  "schemaVersion 为 Spec 契约字段；Revision 同义词语境人工评审",
	"snapshot": "SQLite/备份技术术语；Revision/Backup 同义词语境人工评审",
	"release":  "通用动词/名词（GitHub release 等）；Deployment 同义词语境人工评审",
	"rollout":  "Deployment 同义词语境人工评审",
	"deploy":   "仅禁名词单用；动词义（deploy the app）合法",
	"throttle": "另指限流（F1.9 创建速率）合法；Admission 同义词语境禁",
	"gate":     "健康门（L1 门）语境合法；Admission 同义词语境人工评审",
	// Change Freeze 词条（ADR-0017 附录 A 入册，F1.9）。
	"maintenance window": "运维通用短语；Change Freeze 同义词语境人工评审",
	"block":              "通用词（blocker/阻塞义、FakeRuntime removeBlock 标识符）合法；tsuru Block 同义词语境禁",
	"lock":               "Go sync 锁语境（mu.Lock）合法；Change Freeze 同义词语境人工评审",
	// ---- 运行时 ----
	"manifest":        "通用词；Spec 同义词语境人工评审",
	"config":          "Config 是 fleetly 冻结实体 + 配置通用词；泛指义合法",
	"file":            "通用词；Config 同义词语境人工评审",
	"template":        "Database 模板（dbtemplate）为契约语汇；Spec 同义词语境人工评审",
	"pod":             "k8s 载体词仅 Provider 内部合法（当前无引用）；Workload 同义词语境禁",
	"unit":            "通用词；Workload 同义词语境人工评审",
	"engine":          "internal/engine 为架构 §2 包名（收敛循环）；Runtime 同义词语境禁",
	"docker":          "仅禁指平台概念；Docker/BuildKit/moby 技术语合法",
	"fleet":           "词内包含安全（fleetly 不命中词边界）；Cluster 同义词语境人工评审",
	"pool":            "连接池/实例池通用词；Cluster 同义词语境人工评审",
	"host":            "host 网络为部署形态术语；Node 同义词语境人工评审",
	"server":          "gRPC server 等通用词；Node 同义词语境人工评审",
	"worker":          "worker 进程模板（Process 名）为用户语汇；Node 同义词语境人工评审",
	"join token":      "swarm join 材料语境为 Provider 实现术语；Enrollment 同义词语境人工评审",
	"bootstrap agent": "短语；人工评审",
	"constraint":      "swarm 约束为 Provider 实现术语；Placement 同义词语境人工评审",
	"affinity":        "调度通用词；Placement 同义词语境人工评审",
	"disk":            "通用词（disk usage 诊断）；Volume 同义词语境人工评审",
	"mount":           "仅禁指 Volume 本体；挂载动作义合法",
	"overlay":         "swarm overlay 网络为 Provider 实现术语；Network 同义词语境禁",
	"subnet":          "网络技术术语；Network 同义词语境人工评审",
	"vpc":             "云网络术语；Network 同义词语境人工评审",
	// ---- 能力与路由 ----
	"plugin":           "仅禁标识符；插件模型讨论（deletion test 记录）合法",
	"module":           "Go module 技术语",
	"subsystem":        "通用词",
	"driver":           "通用词；Provider 同义词语境人工评审",
	"backend":          "通用词；Provider 同义词语境人工评审",
	"adapter":          "仅禁对外文案；Go 适配器模式义合法",
	"gateway":          "REST gateway（grpc-gateway）为技术术语；Edge 同义词语境禁",
	"domain":           "DNS domain/TLS 通用义；Route 同义词语境人工评审",
	"endpoint":         "gRPC endpoint 技术语；Route 同义词语境人工评审",
	"route rule":       "短语；人工评审",
	"cert":             "仅禁标识符；注释口语语境人工评审",
	"ssl":              "TLS 对照语境合法；命名语境人工评审",
	"mirror":           "registry mirror 语境合法；Registry 同义词语境人工评审",
	"hub":              "Docker Hub 等专名语境合法",
	"storage":          "通用词；ObjectStore 同义词语境人工评审",
	"bucket":           "S3 bucket 技术语；人工评审",
	"s3":               "S3 兼容协议名为选型契约（ObjectStore）",
	"observability":    "仅禁泛指单一系统；观测泛论义合法",
	"monitoring":       "通用英文（指标/观测泛论）；Metrics 同义词语境人工评审",
	"telemetry":        "proto telemetry 上下文是冻结包名（事件/日志查询面）；Metrics 同义词语境人工评审",
	"notifier":         "通用词（fire 的 notifier 等）；Notification Channel 同义词语境人工评审",
	"monitor":          "动词义（监视/盯）通用；Metrics/Alert Rule 同义词语境人工评审",
	"component":        "通用词；Managed Provider 同义词语境人工评审",
	"internal service": "短语；人工评审",
	// ---- 状态语义 ----
	"revision number":  "短语；Generation 同义词语境人工评审",
	"skew":             "version skew 为 ADR-0015 领域标准英文",
	"sync":             "Go 标准库 sync 包冲突；Converge 同义词语境人工评审",
	"restore":          "Restore 是 fleetly 冻结实体；仅 Replay 同义词语境禁",
	"recovery":         "泛指禁用；恢复语境人工评审",
	"state backup":     "短语；Platform Backup 同义词语境人工评审",
	"full backup":      "短语；人工评审",
	"replay":           "Replay 是 fleetly 冻结实体（回滚唯一实现）；仅 Restore 同义词语境禁",
	"initial password": "短语；Bootstrap Token 同义词语境人工评审",
	"admin key":        "短语；人工评审",
	"notification":     "N2 告警通知通道为契约语汇；Event 同义词语境禁",
	"webhook":          "GitHub webhook（F0.13）为契约语汇；仅 Event 通知通道语境禁",
}

// contextAvoidTokens 解析 CONTEXT.md 的 _Avoid_ 行，收集全部词条（剥除
// 中文/英文括号限定语）。
func contextAvoidTokens(t *testing.T) map[string]bool {
	t.Helper()
	content := readFileLF(t, "CONTEXT.md")
	tokens := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		idx := strings.Index(line, "_Avoid_:")
		if idx < 0 {
			continue
		}
		for _, tok := range strings.Split(line[idx+len("_Avoid_:"):], ",") {
			tok = strings.TrimSpace(tok)
			tok = stripQualifier(tok, '(', ')')
			tok = stripQualifier(tok, '（', '）')
			if tok == "" {
				continue
			}
			tokens[strings.ToLower(tok)] = true
		}
	}
	if len(tokens) < 50 {
		t.Fatalf("parsed only %d avoid tokens from CONTEXT.md — parser is broken", len(tokens))
	}
	return tokens
}

// stripQualifier 剥除词条尾部的括号限定语（如 "capability(授权义)"）。
func stripQualifier(s string, open, close rune) string {
	i := strings.IndexRune(s, open)
	if i < 0 {
		return s
	}
	return strings.TrimSpace(s[:i])
}

// TestAvoidTokensTriaged：CONTEXT.md 每个 _Avoid_ 词条必须进 banned 或
// skipped（新增词汇强制分诊——词汇冻结的机械执行）。
func TestAvoidTokensTriaged(t *testing.T) {
	tokens := contextAvoidTokens(t)
	for tok := range tokens {
		if _, ok := bannedPatterns[tok]; ok {
			continue
		}
		if _, ok := skippedTokens[tok]; ok {
			continue
		}
		t.Errorf("CONTEXT.md avoid token %q is not triaged into bannedPatterns or skippedTokens (ADR-0007 wording freeze)", tok)
	}
	// 双向保鲜：分诊表里的死条目必须清走（banned 以 adrDirect 或 CONTEXT
	// 为源；skipped 只认 CONTEXT）。
	for tok := range bannedPatterns {
		if adrDirect[tok] || tokens[tok] {
			continue
		}
		t.Errorf("banned token %q is neither in CONTEXT.md avoid list nor adrDirect; remove it", tok)
	}
	for tok := range skippedTokens {
		if !tokens[tok] {
			t.Errorf("skipped token %q no longer appears in CONTEXT.md avoid list; remove the entry", tok)
		}
	}
}

// wordingExemptions 是禁词命中的白名单：token → 文件 → 理由。命中在
// 白名单内不红；白名单条目不再命中（对应文件已无该词）也红——双向保鲜。
// 扩白名单前先确认命中确属框架契约等不可更名的外部 API。
var wordingExemptions = map[string]map[string]string{
	"apply": {
		"cmd/fleetlyd/main.go": "lynx boot.Bootstrap.Apply 是框架 API（钩子/服务挂载面）；ADR-0007 禁的是部署语义 apply，框架方法名不可更名",
	},
	// dbtemplate 备份执行链（F2.2，ADR-0039）：pg_dump/mysqldump/mongodump
	// 是引擎原生命令名、dump.rdb 是 redis 数据文件名——外部工具专有名，
	// 非平台 Backup 词汇面使用（ADR-0007 禁的是把 dump 当 Backup 同义词）。
	// 新引擎工具名继续在此追加（强制分诊不静默）。
	"dump": {
		"internal/capability/runtime.go":                "dump.rdb 是 redis 数据文件名（预置卷恢复挂载注释，ADR-0039），非平台词汇面",
		"internal/engine/dbtemplate/dbtemplate.go":      "pg_dump 等 dump 工具是引擎原生命令名（ADR-0039 备份执行链），非平台词汇面",
		"internal/engine/dbtemplate/dbtemplate_test.go": "渲染钉板断言引擎原生命令名（pg_dump/mysqldump/mongodump/dump.rdb），非平台词汇面",
		"internal/engine/dbtemplate/mysql.go":           "mysqldump 是 mysql 引擎原生命令名（ADR-0039），非平台词汇面",
		"internal/engine/dbtemplate/redis.go":           "dump.rdb 是 redis 数据文件名（RDB 恢复预置路径，ADR-0039），非平台词汇面",
	},
	// genproto 的 .pb.gw.go（批 D 扩面裁决：生成物入扫面后的唯一命中族，
	// 逐条豁免）：protoc-gen-grpc-gateway 生成模板的固定注释——
	// runtime.WithMiddlewares/HTTP middlewares 是 grpc-gateway 库自身术语，
	// 非平台受理位命名；生成物手改即错，命中随模板再生成。新增 service
	// 包再命中时同理由追加（强制分诊不静默）。
	"middleware": {
		"genproto/fleetly/automation/v1/automation.pb.gw.go": "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/delivery/v1/delivery.pb.gw.go":     "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/edge/v1/edge.pb.gw.go":             "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/identity/v1/identity.pb.gw.go":     "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/runtime/v1/runtime.pb.gw.go":       "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/structure/v1/structure.pb.gw.go":   "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/system/v1/governance.pb.gw.go":     "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/system/v1/system.pb.gw.go":         "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
		"genproto/fleetly/telemetry/v1/telemetry.pb.gw.go":   "grpc-gateway generated boilerplate comment (library's own middleware term), not platform admission naming",
	},
}

// TestNoBannedWording：扫描生产 .go（含测试，守卫自身除外——本文件即含
// 禁词字面量）与 .proto，批 D 扩面再加 skills/**/*.md（Agent 面用户可见
// 英文——词汇冻结对 Agent 面同样生效）与 install.sh（安装脚本面向用户
// 终端）。词汇契约=全仓文本面（批 D 裁决）：生成物（.pb.go/wire_gen.go，
// 含 genproto 生成面与 swagger 之外的全部生成 .go）不再豁免——生成文本
// 随仓发布、面向 SDK/网关消费方（扩面时全量生成物零命中实证；未来命中
// 逐条带理由豁免进 wordingExemptions）。命中处确属 Provider 实现术语等
// 合法语境时，将该 token 从 bannedPatterns 挪入 skippedTokens 并写明
// 理由（分诊表即白名单，双向保鲜）。
func TestNoBannedWording(t *testing.T) {
	root := repoRoot(t)
	var hits []string
	usedExemptions := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		inScope := scanGoModulePath(rel) ||
			strings.HasPrefix(rel, "proto/") ||
			strings.HasPrefix(rel, "skills/") ||
			rel == "install.sh"
		if !inScope {
			return nil
		}
		isSkillDoc := strings.HasPrefix(rel, "skills/") && strings.HasSuffix(rel, ".md")
		if !strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, ".proto") && !isSkillDoc && rel != "install.sh" {
			return nil
		}
		if strings.HasPrefix(rel, "internal/guards/") {
			return nil
		}
		content := readFileLF(t, rel)
		for tok, re := range bannedPatterns {
			if !re.MatchString(content) {
				continue
			}
			if reason, ok := wordingExemptions[tok][rel]; ok && reason != "" {
				usedExemptions[tok+"\x00"+rel] = true
				continue
			}
			hits = append(hits, fmt.Sprintf("%s: banned token %q", rel, tok))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Errorf("%s (ADR-0007 wording freeze; see CONTEXT.md avoid list)", h)
	}
	// 双向保鲜：白名单条目不再命中即红（清走死条目）。
	for tok, files := range wordingExemptions {
		for rel, reason := range files {
			if reason == "" {
				t.Errorf("wording exemption %s:%s must carry a reason", tok, rel)
			}
			if !usedExemptions[tok+"\x00"+rel] {
				t.Errorf("wording exemption %s:%s no longer matches; remove the entry", tok, rel)
			}
		}
	}
}
