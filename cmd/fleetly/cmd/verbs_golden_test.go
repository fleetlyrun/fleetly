package cmd

// 业务动词 golden 双形态（F0.22 核心）：真 RPC（apitest bufconn 夹具）→
// 输出归一（ULID/指纹/时间戳占位化，fake clock 时间本身确定）→ golden。
// 再生成：go test ./cmd/fleetly/cmd -update。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// normalizeGolden 把非确定输出占位化（ULID 主键/短哈希指纹/内容寻址
// digest——digest 由含 ULID 的 spec 派生，逐次不同；fake clock 时间戳
// 确定不占位；凭证明文随机逐次不同）。
var (
	ulidRe       = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)
	lowerULIDRe  = regexp.MustCompile(`[0-9a-hjkmnp-tv-z]{26}`) // engine 铸名（task-/run- DNS 名）
	digestRe     = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	fprRe        = regexp.MustCompile(`\b[0-9a-f]{16}\b`)
	tokenRe      = regexp.MustCompile(`flt_[A-Za-z0-9_-]{4,}`)
	// browseTicketRe 是 Launcher Ticket 的 base64url 43 字符形态（随机
	// 32B；entry URL 与回显字段双落点——ADR-0051）。
	browseTicketRe = regexp.MustCompile(`[A-Za-z0-9_-]{43}`)
	invitationRe = regexp.MustCompile(`fltinv_[A-Za-z0-9_-]{4,}`)
	hookTokenRe  = regexp.MustCompile(`flthook_[A-Za-z0-9_-]{4,}`)
	credPathRe   = regexp.MustCompile(`(?m)^credentials saved: .*$`)
	credJSONRe   = regexp.MustCompile(`"credentials_path": ".*?"`)
)

func normalizeGolden(s string) string {
	s = invitationRe.ReplaceAllString(s, "<INVITATION>")
	s = hookTokenRe.ReplaceAllString(s, "<HOOKTOKEN>")
	s = credPathRe.ReplaceAllString(s, "credentials saved: <CREDS>")
	s = credJSONRe.ReplaceAllString(s, `"credentials_path": "<CREDS>"`)
	s = tokenRe.ReplaceAllString(s, "<TOKEN>")
	s = browseTicketRe.ReplaceAllString(s, "<BROWSETICKET>")
	// digest/指纹先行（完整十六进制串），铸名的小写 ULID 后行——顺序颠倒
	// 会把 digest 中恰符合 ULID 字符集的 26 长段误占位（revisions_list 实证）。
	s = digestRe.ReplaceAllString(s, "<DIGEST>")
	s = fprRe.ReplaceAllString(s, "<FP>")
	s = lowerULIDRe.ReplaceAllString(s, "<ULID>")
	s = ulidRe.ReplaceAllString(s, "<ULID>")
	return s
}

func goldenFile(verb string) string {
	return regexp.MustCompile(`\s+`).ReplaceAllString(verb, "-")
}

// goldenNodeID 是 FakeRuntime 集群观测里的固定平台节点 ID（apitest 夹具
// DescribeCluster 返回值）。
const goldenNodeID = "01JD0NODE00000000000000000"

// TestGoldenBusinessVerbs 顺序跑完整业务流（同一夹具状态），每动词比较
// 双形态 golden。
func TestGoldenBusinessVerbs(t *testing.T) {
	h := newGoldenHarness(t)

	type step struct {
		verb string
		args []string
		// code 是期望退出码（默认 0；diff 类有变化 = 2，stdout 照常比较）。
		code int
	}
	steps := []step{
		{"projects create", []string{"projects", "create", "shop"}, 0},
		{"projects list", []string{"projects", "list"}, 0},
		// 分页读面（ADR-0026）：只读幂等进主流——--json 轮同参重跑安全。
		{"projects list page", []string{"projects", "list", "--limit", "1"}, 0},
		{"apps create", []string{"apps", "create", "--project", "GOLDEN_PROJECT", "web"}, 0},
		{"apps list", []string{"apps", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"apps list page", []string{"apps", "list", "--project", "GOLDEN_PROJECT", "--limit", "1"}, 0},
		{"deploy", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.27"}, 0},
		// standalone 等待面（D22）：deploy 步已驱动到 succeeded（promoteToSucceeded
		// 后置），wait 附着终态行——单帧即收，--json 轮幂等重放同响应。
		{"deployments wait", []string{"deployments", "wait", "--deployment", "GOLDEN_DEPLOYMENT"}, 0},
		{"rollback", []string{"rollback", "--app", "GOLDEN_APP"}, 0},
		{"deployments list", []string{"deployments", "list", "--app", "GOLDEN_APP"}, 0},
		// after 游标形态（新→旧）：锚 = 人工轮回滚部署（倒数第二新）——页
		// 跳过 --json 轮重放再铸的最新行，落更旧两行（游标截断非空形态）。
		{"deployments list after", []string{"deployments", "list", "--app", "GOLDEN_APP", "--after", "GOLDEN_ROLLBACK_DEPLOYMENT"}, 0},
		{"revisions list", []string{"revisions", "list", "--app", "GOLDEN_APP"}, 0},
		// 分页读面：R1..Rn 升序的首页截断。
		{"revisions list page", []string{"revisions", "list", "--app", "GOLDEN_APP", "--limit", "1"}, 0},
		// 第二镜像 → R2（与 R1 有字段差）：revisions diff 的有变化形态。
		{"deploy second image", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.26"}, 0},
		{"revisions diff", []string{"revisions", "diff", "--app", "GOLDEN_APP", "--from", "1", "--to", "2"}, exitChanges},
		{"builds list", []string{"builds", "list", "--app", "GOLDEN_APP"}, 0},
		// 分页读面：空行集 + limit（镜像直投无构建行）。
		{"builds list page", []string{"builds", "list", "--app", "GOLDEN_APP", "--limit", "1"}, 0},
		{"secrets put", []string{"secrets", "put", "--project", "GOLDEN_PROJECT", "--value", "s3cret", "api-token"}, 0},
		{"secrets list", []string{"secrets", "list", "--project", "GOLDEN_PROJECT"}, 0},
		// after 游标形态（name 字典序轴）：api-token 之后的页（api-token-json 行）。
		{"secrets list after", []string{"secrets", "list", "--project", "GOLDEN_PROJECT", "--after", "api-token"}, 0},
		{"configs put", []string{"configs", "put", "--project", "GOLDEN_PROJECT", "--value", "mode=gold", "app.ini"}, 0},
		{"configs list", []string{"configs", "list", "--project", "GOLDEN_PROJECT"}, 0},
		// 分页读面：name 字典序首页截断（app-json.ini < app.ini）。
		{"configs list page", []string{"configs", "list", "--project", "GOLDEN_PROJECT", "--limit", "1"}, 0},
		{"volumes create", []string{"volumes", "create", "--project", "GOLDEN_PROJECT", "data"}, 0},
		// default 网络随项目出生（F-C）——本步建的是第二个网 internal，
		// 动词面照常覆盖；list 形态 = 出生 default + internal 两行。
		{"networks create", []string{"networks", "create", "--project", "GOLDEN_PROJECT", "internal"}, 0},
		// 只读 list：--json 轮幂等重跑同响应（本轮 default+internal 两网——
		// messaging 的网在后续步骤才建）。
		{"networks list", []string{"networks", "list", "--project", "GOLDEN_PROJECT"}, 0},
		// 分页读面：name 字典序首页截断（default < internal）。
		{"networks list page", []string{"networks", "list", "--project", "GOLDEN_PROJECT", "--limit", "1"}, 0},
		// 网络重建（ADR-0046）：假底座无存量载体网——人类轮走 create 腿
		//（detached 0）；--json 轮撞快速路径（已 attachable 零扰动）——
		// 双形态同输出，幂等面即断言。
		{"networks rebuild", []string{"networks", "rebuild", "--project", "GOLDEN_PROJECT", "internal"}, 0},
		{"routes create", []string{"routes", "create", "--project", "GOLDEN_PROJECT", "--host", "shop.127.0.0.1.sslip.io", "--app", "GOLDEN_APP", "--process", "web", "--port", "8080", "--protocol", "h2c"}, 0},
		{"routes list", []string{"routes", "list"}, 0},
		// 分页读面：ULID 创建序首页截断（首建路由行）。
		{"routes list page", []string{"routes", "list", "--limit", "1"}, 0},

		// 跨 Project peer 声明链（F1.8，ADR-0013 附录 A）：接收方项目建网 →
		// 挂靠方 declare（幂等键——--json 轮重放同响应）→ 接收方 approve →
		// 双侧视图 list → revoke（幂等）。置于 events follow 前：重放面覆盖
		// peer 三拍事件。
		{"projects create messaging", []string{"projects", "create", "messaging"}, 0},
		{"networks create messaging", []string{"networks", "create", "--project", "GOLDEN_PROJECT2", "bus"}, 0},
		{"networks declare", []string{"networks", "declare", "--network", "GOLDEN_NETWORK", "--project", "GOLDEN_PROJECT", "--idempotency-key", "peer-declare"}, 0},
		{"networks approve", []string{"networks", "approve", "GOLDEN_PEER"}, 0},
		{"networks peers", []string{"networks", "peers", "--project", "GOLDEN_PROJECT"}, 0},
		{"networks revoke", []string{"networks", "revoke", "GOLDEN_PEER"}, 0},
		// 事件订阅面的有界形态（F1.2）：--replay 重放保留窗后退出（follow
		// 无界不进 golden）。
		{"events follow", []string{"events", "follow", "--replay"}, 0},
		{"nodes list", []string{"nodes", "list"}, 0},
		{"nodes enroll", []string{"nodes", "enroll"}, 0},
		// 节点运维三动词（F0.19 RuntimeAdmin 面）：FakeRuntime 集群里的固定
		// 平台节点 ID。含 'O'（非 ULID 字符）不进归一，golden 逐字确定。
		{"nodes drain", []string{"nodes", "drain", "--node", goldenNodeID}, 0},
		{"nodes cordon", []string{"nodes", "cordon", "--node", goldenNodeID}, 0},
		{"nodes uncordon", []string{"nodes", "uncordon", "--node", goldenNodeID}, 0},

		// Automation 动词（F1.5/F1.6）：one-shot 全链 + resident 池。tasks
		// create 的 --command 走可重复旗标形态（D24：每次一 argv 元素）。
		{"tasks create", []string{"tasks", "create", "--project", "GOLDEN_PROJECT", "--name", "migrate",
			"--image", "busybox:1.37", "--network-group", "dispatcher", "--env", "POOL=gold", "--ttl-seconds", "3600",
			"--command", "sh", "--command", "-c", "--command", "migrate up"}, 0},
		{"tasks list", []string{"tasks", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"tasks get", []string{"tasks", "get", "--task", "GOLDEN_TASK"}, 0},
		{"runs list", []string{"runs", "list", "--task", "GOLDEN_TASK"}, 0},
		{"runs get", []string{"runs", "get", "--run", "GOLDEN_RUN"}, 0},
		{"runs stop", []string{"runs", "stop", "--run", "GOLDEN_RUN"}, 0},
		{"runs wait", []string{"runs", "wait", "--run", "GOLDEN_RUN"}, 0},
		{"tasks create resident", []string{"tasks", "create", "--project", "GOLDEN_PROJECT", "--name", "dispatcher",
			"--form", "resident", "--image", "ghcr.io/torchwood/dispatcher:1", "--concurrency", "2"}, 0},
		{"tasks scale", []string{"tasks", "scale", "--task", "GOLDEN_TASK2", "--concurrency", "3"}, 0},
		{"tasks renew", []string{"tasks", "renew", "--task", "GOLDEN_TASK2"}, 0},
		{"tasks stop", []string{"tasks", "stop", "--task", "GOLDEN_TASK2", "--force"}, 0},
		{"tasks delete", []string{"tasks", "delete", "--task", "GOLDEN_TASK"}, 0},
		{"tasks delete resident", []string{"tasks", "delete", "--task", "GOLDEN_TASK2"}, 0},

		// Automation 动词（F1.7）：Schedule 时区 cron——create（东京 12:00 =
		// UTC 03:00，fake 时钟已推过若干分钟不影响当日拍点）→ list/get →
		// trigger（立即铸 Task；下一拍不动）→ delete。
		{"schedules create", []string{"schedules", "create", "--project", "GOLDEN_PROJECT", "--name", "nightly-report",
			"--cron", "0 12 * * *", "--timezone", "Asia/Tokyo", "--image", "busybox:1.37", "--ttl-seconds", "3600",
			"--command", "sh", "--command", "-c", "--command", "nightly report"}, 0},
		{"schedules list", []string{"schedules", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"schedules get", []string{"schedules", "get", "--schedule", "GOLDEN_SCHEDULE"}, 0},
		{"schedules trigger", []string{"schedules", "trigger", "--schedule", "GOLDEN_SCHEDULE"}, 0},
		{"schedules delete", []string{"schedules", "delete", "--schedule", "GOLDEN_SCHEDULE"}, 0},

		// Governance 动词（F1.9）：freeze set（全局；幂等键让 --json 轮重放
		// 同响应——同键再 Set 会撞活跃冻结唯一索引，declare 同款手法）→
		// list（active）→ lift → list（历史行可见）。冻结期拒绝面的断言在
		// apitest（CLI 错误信封含随机 error_id，不可 golden）。
		{"freeze set", []string{"freeze", "set", "--all", "--reason", "golden maintenance", "--idempotency-key", "freeze-set"}, 0},
		{"freeze list", []string{"freeze", "list"}, 0},
		{"freeze lift", []string{"freeze", "lift", "GOLDEN_FREEZE"}, 0},
		{"freeze list after lift", []string{"freeze", "list"}, 0},

		// Upload 动词（F1.10，ADR-0019 附录 A）：put（目录 → 确定性 tar →
		// 内容寻址）→ list → deploy --from-dir 全链（上传先于 Deploy 完成；
		// manual 夹具不驱动，部署停在 queued——确定性输出。--json 轮同目录
		// 重传命中内容寻址去重：deduplicated=true 且同一 upload id）。
		{"uploads put", []string{"uploads", "put", "--project", "GOLDEN_PROJECT", "GOLDEN_SRCDIR"}, 0},
		{"uploads list", []string{"uploads", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"deploy from dir", []string{"deploy", "--app", "GOLDEN_APP", "--from-dir", "GOLDEN_SRCDIR"}, 0},
		// Builder strategy 面（F1.14，ADR-0032）：railpack 钉版形态与 static
		// 产物目录形态（manual 夹具不驱动，部署停在 queued——确定性输出）。
		{"deploy from dir railpack", []string{"deploy", "--app", "GOLDEN_APP", "--from-dir", "GOLDEN_SRCDIR",
			"--builder", "railpack", "--railpack-version", "0.39.0"}, 0},
		{"deploy from dir static", []string{"deploy", "--app", "GOLDEN_APP", "--from-dir", "GOLDEN_SRCDIR",
			"--builder", "static", "--output-dir", "web"}, 0},

		// firstBootJobs intake（F1.15 前置，ADR-0033）：独立 app（免遭本流
		// 既有部署序列的 supersede 干扰）——compose 扩展键部署 → 驱动到
		// 锚定（releasing 等 job 终态）→ deployments list 钉
		// first_boot_task_id 可见（ADR-0030 开放锚的 protojson golden）。
		// 幂等键让 --json 轮重放同响应（不铸第二部署抢占 job 等待）。
		{"apps create fbjobs", []string{"apps", "create", "--project", "GOLDEN_PROJECT", "fbjobs"}, 0},
		{"deploy compose jobs", []string{"deploy", "--app", "GOLDEN_APP2", "--compose-file", "GOLDEN_COMPOSE",
			"--idempotency-key", "compose-jobs-1"}, 0},
		{"deployments list first boot", []string{"deployments", "list", "--app", "GOLDEN_APP2"}, 0},

		// P10 判定附注四形态收官步：superseded（app2 compose 部署已驱动到
		// releasing 在途——显式抢占；--json 轮同参重发落在 merged）与
		// deduplicated（app1 尾部 queued 先被 merged 受理；--json 轮同
		// commit 重发命中 admission 去重拿既有引用——Agent 场景锚，不设
		// 幂等键：幂等层重放会掩盖 engine 层去重形态）。
		{"deploy supersede", []string{"deploy", "--app", "GOLDEN_APP2", "--image", "nginx:1.28", "--supersede"}, 0},
		{"deploy commit dedup", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.29",
			"--commit", "deadbeefcafe0000000000000000000000000000"}, 0},

		// 两级变量合成（F2.9，ADR-0043）：App 级 env 直传（--env）→ 共享
		// 变量 put → 归一化期合成进 R5（Project 层在下）→ 改共享变量的
		// 受影响 App 提示 → 删除同理。置于 events follow 后：事件重放
		// golden 不受本段新事件影响。
		{"deploy env direct", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.25",
			"--env", "LOG_LEVEL=warn", "--env", "ONLY_APP=1"}, 0},
		{"revisions diff env", []string{"revisions", "diff", "--app", "GOLDEN_APP", "--from", "6", "--to", "7"}, exitChanges},
		{"shared-variables put", []string{"shared-variables", "put", "--project", "GOLDEN_PROJECT",
			"--value", "postgres://golden", "DATABASE_URL"}, 0},
		{"deploy shared merge", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.25"}, 0},
		{"revisions diff shared merge", []string{"revisions", "diff", "--app", "GOLDEN_APP", "--from", "7", "--to", "8"}, exitChanges},
		// 改值（v1 冻结在 R5）与新增键（R5 缺席）都提示同一受影响 App；
		// 重部署取新值由 deploy cache merge 步兑现（R6 冻结两层合成）。
		{"shared-variables put update", []string{"shared-variables", "put", "--project", "GOLDEN_PROJECT",
			"--value", "postgres://golden-v2", "DATABASE_URL"}, 0},
		{"shared-variables put cache", []string{"shared-variables", "put", "--project", "GOLDEN_PROJECT",
			"--value", "cache.internal", "CACHE_HOST"}, 0},
		{"deploy cache merge", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.24"}, 0},
		{"shared-variables list", []string{"shared-variables", "list", "--project", "GOLDEN_PROJECT"}, 0},
		{"shared-variables list page", []string{"shared-variables", "list", "--project", "GOLDEN_PROJECT",
			"--after", "DATABASE_URL"}, 0},
		{"shared-variables delete", []string{"shared-variables", "delete", "--project", "GOLDEN_PROJECT",
			"CACHE_HOST"}, 0},

		// 端口声明（F3.5）：直投形态的 --port/--protocol（Route-facing 面
		// ——static Route 404 的受理闭口；归一化产物断言在 apitest
		// portdecl 双件）。置于变量段后：不挪既有 revisions diff 的序号锚。
		{"deploy image with port", []string{"deploy", "--app", "GOLDEN_APP", "--image", "nginx:1.23",
			"--port", "8080", "--protocol", "h2c"}, 0},

		// spec_file 裸 AppSpec（第四源，F3.5）：全字段 intake——两进程/
		// 端口/网络直接声明（互斥、AppRef 覆写与 upload 执法在 apitest
		// specfile 三件）。
		{"deploy spec file", []string{"deploy", "--app", "GOLDEN_APP", "--spec-file", "GOLDEN_SPECFILE"}, 0},

		// 部署策略 intake（ADR-0048 决策 6：compose 扩展键 deploy.strategy
		// 是 spec_file 面外的受理形态；DeployRequest 不加 strategy 面）。
		// 独立 app3（免扰本流部署序）：compose blue-green 冻结 R1（归一化
		// 产物断言）→ 无策略镜像直投冻结 R2（缺省零值 = rolling，冻结体
		// 无 strategy 字段——存量零漂移形态）→ revisions diff 钉
		// strategy 字段增量（protojson 枚举名规范形）。值域拒绝文本在
		// spec 单测（CLI 错误信封含随机 error_id，不可 golden——freeze
		// 拒绝面同款先例）。
		{"apps create bgapp", []string{"apps", "create", "--project", "GOLDEN_PROJECT", "bgapp"}, 0},
		{"deploy compose strategy", []string{"deploy", "--app", "GOLDEN_APP3", "--compose-file", "GOLDEN_COMPOSE_BG",
			"--idempotency-key", "compose-bg-1"}, 0},
		{"deploy image no strategy", []string{"deploy", "--app", "GOLDEN_APP3", "--image", "nginx:1.27",
			"--idempotency-key", "bgimg-1"}, 0},
		{"revisions diff strategy", []string{"revisions", "diff", "--app", "GOLDEN_APP3", "--from", "1", "--to", "2"}, exitChanges},

		// F3.1 API 可见面（from_generation / process_strategies 读面）：
		// "deploy compose strategy" 步后钩子把 app3 蓝绿 R1 驱动到 succeeded
		//（代次化载体 g1 的 L1 + 观察窗——基线 gen 1 落位）；本段第三笔
		// blue-green 部署（换镜像 → R3 新冻结）携带 from_generation=1 与
		// merged 受理（R2 的 queued 请求被合并取代）。deployments get 钉
		// 单行双代窗叙事（generation 3←1），revisions list 钉策略目录
		//（R1/R3 web:blue-green、R2 缺省 rolling 的零值省略形态）。
		{"deploy compose strategy bump", []string{"deploy", "--app", "GOLDEN_APP3", "--compose-file", "GOLDEN_COMPOSE_BG_BUMP",
			"--idempotency-key", "compose-bg-3"}, 0},
		{"deployments get", []string{"deployments", "get", "--deployment", "GOLDEN_BG_DEPLOYMENT"}, 0},
		{"revisions list strategies", []string{"revisions", "list", "--app", "GOLDEN_APP3"}, 0},

		// Platform 动词（F2.3，ADR-0039 决策 10）：手动触发（同步执行——
		// 幂等键让 --json 轮重放同响应）与快照列举（假 restic 的 canned
		// 集；golden 双形态）。
		{"platform backup", []string{"platform", "backup", "--idempotency-key", "platform-backup-1"}, 0},
		{"platform backups", []string{"platform", "backups"}, 0},
	}

	// GOLDEN_SRCDIR 是上传 golden 的固定内容目录（确定性 tar → digest 确定，
	// golden 逐字稳定；路径本身不进输出）。
	srcDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(srcDir, "web"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "web", "index.html"), []byte("golden\n"), 0o600))
	// GOLDEN_COMPOSE 是 firstBootJobs 扩展键的固定 compose（独立目录——
	// 不污染 GOLDEN_SRCDIR 的内容寻址 digest；api-token 已由本流 secrets
	// put 步预置——材料面缺行即 Run 不可铸）。
	composeFile := filepath.Join(t.TempDir(), "compose-firstboot.yaml")
	require.NoError(t, os.WriteFile(composeFile, []byte(
		"services:\n  web:\n    image: nginx:1.27\nx-fleetly-first-boot-jobs:\n  - name: migrate\n    image: migrate/migrate:v4.18.1\n    command: [\"sh\", \"-c\", \"migrate -database \\\"$(cat /run/secrets/api-token)\\\" up\"]\n    secrets:\n      - api-token\n    ttl: 300s\n"), 0o600))
	// GOLDEN_SPECFILE 是裸 AppSpec 的固定 spec（F3.5 第四源；protojson
	// 规范形——枚举名与 Revision 冻结体同形；AppRef 缺席 = 服务端覆写）。
	specFile := filepath.Join(t.TempDir(), "appspec.json")
	require.NoError(t, os.WriteFile(specFile, []byte(
		`{"source":{"image":{"ref":"nginx:1.22"}},"processes":[{"name":"web","image":"nginx:1.22","ports":[{"port":8080,"protocol":"PROTOCOL_HTTP"}],"networks":["default"]},{"name":"worker","image":"busybox:1.37"}]}`), 0o600))
	// GOLDEN_COMPOSE_BG 是部署策略扩展键的固定 compose（ADR-0048：
	// deploy.strategy: blue-green——app3 的 R1 冻结源；独立目录同
	// GOLDEN_COMPOSE 先例）。
	composeBGFile := filepath.Join(t.TempDir(), "compose-bg.yaml")
	require.NoError(t, os.WriteFile(composeBGFile, []byte(
		"services:\n  web:\n    image: nginx:1.27\n    deploy:\n      strategy: blue-green\n"), 0o600))
	// GOLDEN_COMPOSE_BG_BUMP 是基线之上的第二笔 blue-green（换镜像 →
	// 内容寻址新冻结 R3；strategy 同键——from_generation golden 的载体）。
	composeBGBumpFile := filepath.Join(t.TempDir(), "compose-bg-bump.yaml")
	require.NoError(t, os.WriteFile(composeBGBumpFile, []byte(
		"services:\n  web:\n    image: nginx:1.28\n    deploy:\n      strategy: blue-green\n"), 0o600))

	// GOLDEN_PROJECT/GOLDEN_APP 占位替换为夹具真实 ID（项目 ID 是 ULID，
	// 归一后可预测）。
	var projectID, project2ID, networkID, peerID, appID, app2ID, app3ID, taskID, task2ID, runID, scheduleID, freezeID, deployID, rollbackDeployID, bgDeployID string
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				if a == "GOLDEN_PROJECT" {
					args[i] = projectID
				}
				if a == "GOLDEN_PROJECT2" {
					args[i] = project2ID
				}
				if a == "GOLDEN_NETWORK" {
					args[i] = networkID
				}
				if a == "GOLDEN_PEER" {
					args[i] = peerID
				}
				if a == "GOLDEN_APP" {
					args[i] = appID
				}
				if a == "GOLDEN_APP2" {
					args[i] = app2ID
				}
				if a == "GOLDEN_APP3" {
					args[i] = app3ID
				}
				if a == "GOLDEN_DEPLOYMENT" {
					args[i] = deployID
				}
				if a == "GOLDEN_ROLLBACK_DEPLOYMENT" {
					args[i] = rollbackDeployID
				}
				if a == "GOLDEN_TASK" {
					args[i] = taskID
				}
				if a == "GOLDEN_TASK2" {
					args[i] = task2ID
				}
				if a == "GOLDEN_RUN" {
					args[i] = runID
				}
				if a == "GOLDEN_SCHEDULE" {
					args[i] = scheduleID
				}
				if a == "GOLDEN_FREEZE" {
					args[i] = freezeID
				}
				if a == "GOLDEN_SRCDIR" {
					args[i] = srcDir
				}
				if a == "GOLDEN_COMPOSE" {
					args[i] = composeFile
				}
				if a == "GOLDEN_COMPOSE_BG" {
					args[i] = composeBGFile
				}
				if a == "GOLDEN_COMPOSE_BG_BUMP" {
					args[i] = composeBGBumpFile
				}
				if a == "GOLDEN_BG_DEPLOYMENT" {
					args[i] = bgDeployID
				}
				if a == "GOLDEN_SPECFILE" {
					args[i] = specFile
				}
			}
			code, out, stderr := runCLI(t, args...)
			wantStderr := ""
			if st.code == exitChanges {
				wantStderr = "\n" // errChanges 渲染为空，框架打一行换行
			}
			if code != st.code || stderr != wantStderr {
				t.Fatalf("%s: code=%d stderr=%q args=%q", st.verb, code, stderr, args)
			}
			// 捕获后续步骤需要的 ID（人类形态行：created project shop (id X)）。
			if st.verb == "projects create" {
				projectID = extractTailID(out)
			}
			if st.verb == "projects create messaging" {
				project2ID = extractTailID(out)
			}
			if st.verb == "networks create messaging" {
				networkID = extractTailID(out)
			}
			if st.verb == "networks declare" {
				peerID = extractPeerID(t, out)
			}
			if st.verb == "apps create" {
				appID = extractTailID(out)
			}
			if st.verb == "apps create fbjobs" {
				app2ID = extractTailID(out)
			}
			if st.verb == "apps create bgapp" {
				app3ID = extractTailID(out)
			}
			if st.verb == "tasks create" {
				taskID = extractTaskID(t, out)
				spawnTaskRun(t, h, taskID)
				runID = firstRunID(t, h, taskID)
			}
			if st.verb == "tasks create resident" {
				task2ID = extractTaskID(t, out)
				h.Drive(sdk.WithToken(context.Background(), h.Token))
			}
			if st.verb == "schedules create" {
				scheduleID = extractScheduleID(t, out)
			}
			if st.verb == "freeze set" {
				freezeID = extractTailID(out)
			}
			// deploy 后推进到 succeeded（rollback 的 golden 需要成功基线）；
			// 部署 ID 取自人类形态首行（"deployment X queued ..."）——下一
			// 步 deployments wait 的附着锚。
			if st.verb == "deploy" {
				m := deploymentQueuedRe.FindStringSubmatch(out)
				if len(m) < 2 {
					t.Fatalf("cannot extract deployment id from deploy output: %q", out)
				}
				deployID = m[1]
				promoteToSucceeded(t, h, appID)
			}
			// 变量段的三次部署（F2.9）不驱动：断言面是 Revision 冻结体
			//（Deploy RPC 内同步完成）与受影响提示，不依赖部署终态；驱动
			// 收口会推进假时钟且迭代数随调度漂移（-race 下时间戳不稳）。
			// 部署行留在 queued（from-dir 步同款先例——manual 夹具不驱动）。
			// 回滚部署 ID 取自人类形态首行（deployments list after 的游标锚
			// ——人工轮铸行，其后 --json 轮再铸的最新行被游标跳过）。
			if st.verb == "rollback" {
				m := rollbackDeploymentRe.FindStringSubmatch(out)
				if len(m) < 2 {
					t.Fatalf("cannot extract rollback deployment id from output: %q", out)
				}
				rollbackDeployID = m[1]
			}
			// compose 扩展键部署驱动到锚定即止（releasing 等 job 终态——
			// 下一步 deployments list 的 golden 钉 first_boot_task_id 在场；
			// 不驱动完成：done 游标下字段归空）。
			if st.verb == "deploy compose jobs" {
				anchorFirstBootJob(t, h, app2ID)
			}
			// app3 蓝绿 R1 驱动到 succeeded（F3.1 from_generation 基线）：
			// 首代无旧代（from=0，窗口退化为单代），载体是代次化 g1——
			// promoteToSucceeded 的平名上报对蓝绿新代不命中。
			if st.verb == "deploy compose strategy" {
				promoteScopedToSucceeded(t, h, app3ID, 1)
			}
			// 蓝绿基线之上的第三笔部署 ID（deployments get 步的锚）。
			if st.verb == "deploy compose strategy bump" {
				m := deploymentQueuedRe.FindStringSubmatch(out)
				if len(m) < 2 {
					t.Fatalf("cannot extract deployment id from bump output: %q", out)
				}
				bgDeployID = m[1]
			}
			// runs stop 后收口到终态（runs wait 的前置：WaitRun 首帧即终态）。
			if st.verb == "runs stop" {
				h.Runtime.ReportStopped(runID, 1)
				h.Drive(sdk.WithToken(context.Background(), h.Token))
			}
			// resident 池 stop --force 后注入停止观测（不驱动到 drained——
			// --json 轮的 StopTask 需 draining 非终态；删除步不受影响）。
			if st.verb == "tasks stop" {
				for _, r := range taskRunIDs(t, h, task2ID) {
					h.Runtime.ReportStopped(r, 1)
				}
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			// --json 轮：create/put 类换名字撞唯一名；list/diff 类幂等直跑。
			jsonArgs := append([]string{}, args...)
			if over, ok := jsonArgOverrides[st.verb]; ok {
				for i, v := range over {
					jsonArgs[i] = v
				}
			}
			code, out, stderr = runCLI(t, append(jsonArgs, "--json")...)
			if code != st.code || stderr != wantStderr {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, st.code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// jsonArgOverrides 是 --json 轮的位置/唯一值替换（索引 → 新值；GOLDEN_*
// 占位在运行时同样被替换）。
var jsonArgOverrides = map[string]map[int]string{
	"projects create":           {2: "shop-json"},
	"projects create messaging": {2: "messaging-json"},
	"apps create":               {4: "web-json"},
	"apps create fbjobs":        {4: "fbjobs-json"},
	"apps create bgapp":         {4: "bgapp-json"},
	"secrets put":               {6: "api-token-json"},
	"configs put":               {6: "app-json.ini"},
	// 删除步的 --json 轮换名（CACHE_HOST 已被人轮删——重删 404 非零退出；
	// DATABASE_URL 在场，受影响提示同形）。
	"shared-variables delete":   {4: "DATABASE_URL"},
	"volumes create":            {4: "data-json"},
	"networks create":           {4: "internal-json"},
	"networks create messaging": {4: "bus-json"},
	"routes create":             {5: "json.127.0.0.1.sslip.io"},
	"tasks create":              {5: "migrate-json"},
	"tasks create resident":     {5: "dispatcher-json"},
	"schedules create":          {5: "nightly-report-json"},
}

// anchorFirstBootJob 驱动到 firstBootJobs 铸造锚可见（releasing 等 job
// 终态；first_boot_task_id 非空）即止——job 完成驱动归 apitest 全链。
func anchorFirstBootJob(t *testing.T, h *apitest.Harness, appID string) {
	t.Helper()
	ctx := sdk.WithToken(context.Background(), h.Token)
	client := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	for i := 0; i < 40; i++ {
		h.Drive(ctx)
		list, err := client.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
		if err == nil && len(list.GetDeployments()) > 0 && list.GetDeployments()[0].GetFirstBootTaskId() != "" {
			return
		}
	}
	t.Fatal("first boot job was never anchored (first_boot_task_id stayed empty)")
}

// extractTailID 取 "... (id X)" 尾部的 ID。
var tailIDRe = regexp.MustCompile(`\(id ([0-9A-HJKMNP-TV-Z]{26})\)`)

func extractTailID(out string) string {
	m := tailIDRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// promoteToSucceeded 手动驱动到 succeeded（观测注入 + 假时钟推过 L1/L3；
// rollback 的成功基线前置——手动形态保证 golden 确定性）。
func promoteToSucceeded(t *testing.T, h *apitest.Harness, appID string) {
	t.Helper()
	promoteCarrierToSucceeded(t, h, appID, appID+"-web", 1)
}

// promoteScopedToSucceeded 驱动蓝绿部署到 succeeded（F3.1 from_generation
// golden 的基线前置）：新代载体是代次化名 app-web-g<gen>（ADR-0048 决策
// 1.4——genScopedWorkloadID 公式），running 上报按代次名命中 L1。
func promoteScopedToSucceeded(t *testing.T, h *apitest.Harness, appID string, gen uint64) {
	t.Helper()
	promoteCarrierToSucceeded(t, h, appID, fmt.Sprintf("%s-web-g%d", appID, gen), gen)
}

// promoteCarrierToSucceeded 是驱动公因子：载体 running 观测 + 假时钟推过
// L1/L3，直到该 App 最新部署行 succeeded。
func promoteCarrierToSucceeded(t *testing.T, h *apitest.Harness, appID, workloadID string, gen uint64) {
	t.Helper()
	client := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	ctx := sdk.WithToken(context.Background(), h.Token) // 执法链激活后读路径同样要凭证
	for i := 0; i < 20; i++ {
		h.Runtime.ReportRunning(workloadID, capability.Generation(gen))
		h.Drive(ctx)
		h.Clock.Advance(120 * time.Second)
		h.Drive(ctx)
		list, err := client.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
		if err == nil && len(list.GetDeployments()) > 0 && list.GetDeployments()[0].GetState() == "succeeded" {
			return
		}
	}
	t.Fatal("deployment did not reach succeeded within the manual drive budget")
}

// rollbackDeploymentRe 取 rollback 人类形态 "rolling back via deployment X"
// 的部署 ID（deployments list after 步的游标锚）。
var rollbackDeploymentRe = regexp.MustCompile(`rolling back via deployment ([0-9A-HJKMNP-TV-Z]{26})`)

// taskCreatedRe 取人类形态 "task X created (...)" 的 ID。
var taskCreatedRe = regexp.MustCompile(`task ([0-9A-HJKMNP-TV-Z]{26}) created`)

func extractTaskID(t *testing.T, out string) string {
	t.Helper()
	m := taskCreatedRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract task id from output: %q", out)
	}
	return m[1]
}

// scheduleCreatedRe 取人类形态 "schedule X created (...)" 的 ID。
var scheduleCreatedRe = regexp.MustCompile(`schedule ([0-9A-HJKMNP-TV-Z]{26}) created`)

func extractScheduleID(t *testing.T, out string) string {
	t.Helper()
	m := scheduleCreatedRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract schedule id from output: %q", out)
	}
	return m[1]
}

// peerDeclaredRe 取人类形态 "declared peer X on network ..." 的 ID。
var peerDeclaredRe = regexp.MustCompile(`declared peer ([0-9A-HJKMNP-TV-Z]{26}) on network`)

func extractPeerID(t *testing.T, out string) string {
	t.Helper()
	m := peerDeclaredRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract peer id from output: %q", out)
	}
	return m[1]
}

// spawnTaskRun 驱动补足 + 观测 running（后续 golden 步骤的状态确定性）。
func spawnTaskRun(t *testing.T, h *apitest.Harness, taskID string) {
	t.Helper()
	ctx := sdk.WithToken(context.Background(), h.Token)
	h.Drive(ctx)
	for _, r := range taskRunIDs(t, h, taskID) {
		h.Runtime.ReportRunning(r, 1)
	}
	h.Drive(ctx)
}

// taskRunIDs 返回 Task 名下全部 Run 的 ID。
func taskRunIDs(t *testing.T, h *apitest.Harness, taskID string) []string {
	t.Helper()
	rc := automationv1.NewRunsServiceClient(h.Conn)
	resp, err := rc.ListRuns(sdk.WithToken(context.Background(), h.Token), &automationv1.ListRunsRequest{TaskId: taskID})
	if err != nil {
		t.Fatalf("list runs for golden fixture: %v", err)
	}
	var ids []string
	for _, r := range resp.GetRuns() {
		ids = append(ids, r.GetId())
	}
	return ids
}

// firstRunID 返回首条 Run ID（新→旧）。
func firstRunID(t *testing.T, h *apitest.Harness, taskID string) string {
	t.Helper()
	ids := taskRunIDs(t, h, taskID)
	if len(ids) == 0 {
		t.Fatalf("no runs spawned for task %s", taskID)
	}
	return ids[0]
}
