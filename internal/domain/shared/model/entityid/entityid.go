// Package entityid 是平台实体 id 的铸造与校验单源（ADR-0092）：id 是
// 26 位小写 ULID——全局唯一、时间有序、计数器重置免疫（restore-to-T /
// 平台快照回滚后新铸 id 永不撞旧 id）。id 铸成后不可变；id 是不透明
// 字符串，全仓禁止解析其内部结构（归属判定只查平台表，架构 §8）。
// 派生实体 id 走父链律（Derive/Revision：<父id>-<kind><seq>）；创建序
// 与游标轴仍是每表 seq 列（ADR-0070 决策 1 保留），与 id 形态解耦——
// 换形不触碰任何排序/分页/游标面。旧名基形（<name>-<seq>）id 在快照
// 恢复共存面合法（Valid 双形收，铸造只出新形）。
package entityid

import (
	"crypto/rand"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// MaxLen 是 id 总长上限（TEXT 主键无实际约束，此界只为派生名/DNS 链条
// 留出可推理的边界；载体名超长由各 Provider 既有截断+哈希兜底）。
const MaxLen = 128

// ulidPattern 是新形 id：26 位小写 Crockford base32（无连字符）——
// 字符集天然全落 RFC1123 标签，派生载体名/DNS 名无需转义。
var ulidPattern = regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{26}$`)

// hyphenPattern 是带连字符的合法 id 形：派生形（<父id>-<kind><seq>，
// 如 01jbx…-d3）与旧名基形（<name>-<seq>，快照恢复共存面）。
var hyphenPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

// NamePatternSource 是 namePattern 的正则字面源（P4-1 批 1 单源对齐）：
// proto 面的 protovalidate CEL 注解无法引用 Go 常量，正则在 structure.v1
// CreateProjectRequest 注解与本包各存一份，字节一致由本包 descriptor 对
// 齐测试执法（从生成的请求 descriptor 反读 cel 选项，断言 shape 表达式
// 包含本字面与 MaxNameLen）。改形态必须 proto 与本包同 commit 同改——
// 对齐测试即红提示。
const NamePatternSource = `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`

// namePattern 是有名实体 name 的形态（dns 标签，ADR-0070 决策 4 保留）：
// 字符集安全是 <process>.<appName> 网络别名、Task 名等 name 基派生名的
// 前提。
var namePattern = regexp.MustCompile(NamePatternSource)

// MaxNameLen 是有名实体 name 长度上限：dns 标签惯例 63。
const MaxNameLen = 63

// 铸造源：生产 = crypto/rand 熵；测试 = SwapForTest 注入确定性熵
// （golden 钉死 id 串）。时间不取墙钟——按 ADR-0087 决策 2 纯度判据以
// 参数入域（调用方持时钟）；swap 面仅限 testsupport 调用（守卫执法——
// 生产代码调用即红）。mutex 兼顾并发铸造与测试换源。
var (
	mintMu  sync.Mutex
	entropy io.Reader = rand.Reader
)

// SwapForTest 换铸造熵并返回还原函数（defer 调用）。确定性 golden 的
// 唯一合法入口：repo 时钟（FakeClock 固定起点）+ 计数熵 ⇒ 全程可复现
// 的 ULID 串。
func SwapForTest(e io.Reader) (restore func()) {
	mintMu.Lock()
	prev := entropy
	entropy = e
	mintMu.Unlock()
	return func() {
		mintMu.Lock()
		entropy = prev
		mintMu.Unlock()
	}
}

// Mint 铸一枚实体 id（26 位小写 ULID；now 进 ULID 时间段，由调用方
// 时钟给出——域不持墙钟）。有名/独立实体全量唯一铸造入口（repo Create
// 的 ID 空分支）；全局唯一免协调（80 位熵/毫秒），SQLite 单写者下无碰撞
// 面；id 与 name 无关——同名重建即换 id（身份消歧），创建序看每表 seq
// 列。
func Mint(now time.Time) string {
	mintMu.Lock()
	defer mintMu.Unlock()
	id := ulid.MustNew(ulid.Timestamp(now.UTC()), entropy)
	return strings.ToLower(id.String())
}

// Valid 报告 s 是否为合法平台实体 id 形态（新形裸 ULID / 派生形 / 旧
// 名基形——后者仅快照恢复共存，铸造不再产出）。
func Valid(s string) bool {
	if len(s) > MaxLen {
		return false
	}
	return ulidPattern.MatchString(s) || hyphenPattern.MatchString(s)
}

// ValidName 报告 s 是否为合法有名实体 name 形态（dns 标签）。
func ValidName(s string) bool {
	return len(s) <= MaxNameLen && namePattern.MatchString(s)
}

// Derive 装配派生实体 id：<父id>-<kind><seq>（01jbx…-d7）。kind 字母消歧
// 同父兄弟的跨表同号（deployment/build 同以 app 为父、各自表 seq 独立
// 计数——无 kind 则 <id>-1 双关）。
func Derive(parentID, kind string, seq int64) string {
	return fmt.Sprintf("%s-%s%d", parentID, kind, seq)
}

// Revision 由 app id 与 revision per-App 序号派生确定性 revision id
// （01jbx…-r7）：per-App 修订号是 UI 面既有语义（r<n>），seq 沿用。
func Revision(appID string, seq int64) string {
	return fmt.Sprintf("%s-r%d", appID, seq)
}
