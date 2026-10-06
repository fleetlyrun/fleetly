// Package dbbrowser 是数据浏览器方言注册表（ADR-0051 决策 3）：
// database engine → 承接工具（pgweb/redis-commander/adminer/mongoku）的
// 钉版镜像 + 端口 + 凭证注入方言 + 只读方言。dbtemplate 同款纪律：per-
// browser 接口 adapter、注册表零 switch、digest 钉定常量住 adapter 文件
// （Image() 恒为 tag@sha256 双段形态）。
//
// 方言事实全部经本地 docker 实证（2026-10-07，ADR-0051 决策 3 的表格是
// 真源）；本包 stdlib-only 近叶子（import 守卫口径同 dbtemplate）——渲染
// 产物是中立形态（Rendering），capability.Workload 的组装住 engine。
package dbbrowser

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed assets/router.php
var assets embed.FS

// AdminerRouterPHP 是 vendored Adminer 胶水的单源暴露面（engine 落为
// browse 会话的材料文件；测试钉 sha256——改动 = 刻意动作同 commit 更新）。
func AdminerRouterPHP() []byte {
	b, err := assets.ReadFile("assets/router.php")
	if err != nil {
		// embed 编译期在场，读失败 = 程序性不可能；保守 panic（不吞）。
		panic(fmt.Sprintf("dbbrowser: embedded router.php unreadable: %v", err))
	}
	return b
}

// Enforcement 是只读执法层级（ADR-0051 决策 6 的诚实分层；受理面据此收
// scope：none 档只读浏览需 databases:write）。
type Enforcement string

const (
	// EnforcementSession：服务端会话执法（PG 家族——连接 URL 的
	// options=-c default_transaction_read_only=on 由 postgres 本身拒绝写）。
	EnforcementSession Enforcement = "session"
	// EnforcementTool：工具 HTTP API 层门（redis-commander 变更路由 403
	// 中间件 + CLI 白名单；mongoku 写端点门）——非 UI 摆设，但非服务端。
	EnforcementTool Enforcement = "tool"
	// EnforcementNone：无方言（Adminer）——只读浏览按 databases:write 门。
	EnforcementNone Enforcement = "none"
)

// Conn 是 browse 会话的连接参数（engine 从 database:<name> Secret 的连接
// URL 解出——单真源回读，PasswordFromURL 同口径）。
type Conn struct {
	Host     string
	Port     int32
	User     string
	Password string
	DB       string
}

// Rendering 是方言渲染产物（中立形态）：Env/Command 进 Workload 投影，
// Files 落 Materials.SecretFiles（swarm 载体 /run/secrets/<名>，0444——
// 非 root 工具用户可读）。密码只允许出现在 Files 或方言必须的 env
// （redis-commander/Mongoku 的边界，ADR-0051 后果节记档）。
type Rendering struct {
	Env     map[string]string
	Command []string
	Files   map[string][]byte
}

// Browser 是一个承接工具的完整方言知识（per-browser adapter 接口面）。
type Browser interface {
	// Name 返回工具名（Workload.Process 与 API 回显 browser 字段共用）。
	Name() string
	// Image 返回钉版镜像引用（tag@sha256 index digest 双段形态，ADR-0045
	// 口径）。
	Image() string
	// ImageDigest 是 digest 钉定面（与 Image() 的 digest 段恒一致）。
	ImageDigest() string
	// Port 是工具监听端口（四件统一 8080——方言各自的改写通道落实）。
	Port() int32
	// ReadOnlyEnforcement 返回该方言的只读执法层级。
	ReadOnlyEnforcement() Enforcement
	// Render 渲染一次 browse 会话的载体方言（readonly 形态按方言落；
	// EnforcementNone 的工具收到 readonly=true 属受理面漏洞——返回错误
	// fail-closed 兜底）。
	Render(conn Conn, readonly bool) (Rendering, error)
}

// registry 是 engine 值域 → 承接工具的映射（受理位与投影共用的单源；
// 对 dbtemplate.Engines 的全 totality 由测试钉死——E_BROWSER_UNSUPPORTED
// 只在新增引擎漏配时可达）。
var registry = map[string]Browser{
	"postgres": pgwebTemplate{},
	"pgvector": pgwebTemplate{},
	"redis":    redisCommanderTemplate{},
	"mysql":    adminerTemplate{},
	"mongo":    mongokuTemplate{},
}

// For 返回 engine 对应的浏览器方言（不存在 ok=false）。
func For(engine string) (Browser, bool) {
	b, ok := registry[engine]
	return b, ok
}

// Engines 返回在册引擎值域（排序稳定——测试与守卫消费）。
func Engines() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Browsers 返回去重后的承接工具名集（排序稳定）。
func Browsers() []string {
	seen := map[string]bool{}
	for _, b := range registry {
		seen[b.Name()] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
