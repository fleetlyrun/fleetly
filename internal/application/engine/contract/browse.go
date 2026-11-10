// browse.go 是 Browse 会话域应用模块的核/模块契约面（engine 去应用模块
// import 第四批，2026-11-10）：engine 核只持 Browse 接口与构造端口袋，具体
// 装配器由组合根经 engine.Deps.BrowseFactory 注入——核不再 import 浏览会话
// 应用模块包。值类型/常量/哨兵随契约面下沉（gRPC 受理面与 assembly 构造面
// 的既名引用经 engine 别名零改）；端口形态全部落在 domain 面（capability/
// state/repo 签名），契约叶子保持零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// browse 限额常量（ADR-0051 决策 1；编译期常量先例 = execMaxSessionsPerTeam 族）。
const (
	// BrowseMaxSessionsPerTeam 是 per-Team 并发 browse 会话上限（浏览器
	// 载体是常驻容器，比 exec 会话更重——上限取 exec 的一半；api 受理面
	// 的拒绝文案共用本常量）。
	BrowseMaxSessionsPerTeam = 4
	// BrowseHardTTL 是会话硬时效（受理起算；api 层铸台账行的 expires_at
	// 共用本常量）。
	BrowseHardTTL = 30 * time.Minute
)

// browse 域哨兵（api 层映射稳定 errcode）。
var (
	// ErrBrowseQuota 是 per-Team 并发 browse 会话超限。
	ErrBrowseQuota = errors.New("too many concurrent browse sessions for team")
	// ErrBrowseSessionNotFound 是会话不存在或已收口（entry/authorize 面）。
	ErrBrowseSessionNotFound = errors.New("browse session not found")
)

// BrowseConfig 是 browse 面的配置快照（engine Options.Data.Browse 注入；
// HostSuffix 或 GatewayURL 空 = 面停用，ADR-0051 决策 5）。P5 wave-3
// 随域拆包迁入 browses，engine 去应用模块 import 第四批随契约面下沉——
// options/assembly 构造面经 engine 类型别名零改。
type BrowseConfig struct {
	HostSuffix string
	GatewayURL string
	TLSMode    string // none（缺省）| auto
}

// Configured 报告 browse 面是否装配可用。
func (c BrowseConfig) Configured() bool { return c.HostSuffix != "" && c.GatewayURL != "" }

// BrowseInput 是会话注册输入（api 层在受理事务提交后注入）。P5 wave-3
// 随域拆包迁入 browses，engine 去应用模块 import 第四批随契约面下沉——
// gRPC 受理面经 engine 类型别名零改。
type BrowseInput struct {
	SessionID    string
	TeamID       string
	ProjectID    string
	DatabaseID   string
	DatabaseName string
	EngineName   string
	ReadOnly     bool
}

// BrowseSessionInfo 是注册产品（api 层回显面）。同上随契约面下沉。
type BrowseSessionInfo struct {
	ID          string
	Browser     string
	ReadOnly    bool
	Enforcement dbbrowser.Enforcement
	ExpiresIn   int32
}

// Browse 是 Browse 会话域应用模块的核消费面：engine 循环表经 Module.Loops
// 取 browse 步，routepublish 的 ephemeral 路由合并/签名面经
// BrowseCapabilityRoutes/BrowseRoutesFingerprint，gRPC 受理/grant 面与
// assembly gateway 的 cookie 校验面经其余方法（消费点零改）。
type Browse interface {
	Module
	// BrowseConfigured 报告 browse 面装配可用（api 受理面先拒 E_BROWSE_DISABLED）。
	BrowseConfigured() bool
	// BrowseCheckQuota 是受理面 quota 闸（在册数 + 本受理 ≤ 上限）。
	BrowseCheckQuota(teamID string) error
	// RegisterBrowseSession 注册一个刚受理的会话（api 层在 commit 后调用）。
	RegisterBrowseSession(in BrowseInput) (BrowseSessionInfo, error)
	// BrowseEntryURL 铸会话入口 URL（api 响应面）。
	BrowseEntryURL(sessionID string) string
	// BrowseSessionGrant 兑换一枚新 grant（entry 烧票后调用）。
	BrowseSessionGrant(sessionID string) (string, int, bool)
	// BrowseValidateGrant 校验 cookie 值的 grant（authorize 每请求调用）。
	BrowseValidateGrant(sessionID, grant string) bool
	// BrowseRoutesFingerprint 是会话集指纹（routepublish 的签名面）。
	BrowseRoutesFingerprint() string
	// BrowseCapabilityRoutes 铸在册会话的 ephemeral 双 Route（publishRoutes 合并面）。
	BrowseCapabilityRoutes(ctx context.Context) []capability.Route
}

// BrowsePorts 是浏览会话装配器的构造端口袋（组合根在 engine construct.go
// 绑定核方法值/闭包与 repo；形态与浏览会话应用模块的 Deps 逐字段同形，
// 契约叶子只依赖 domain 类型——应用模块包类型不得穿过本叶子）。
type BrowsePorts struct {
	Runtime      capability.Runtime
	Repos        transact.Stores
	Cipher       *material.Cipher
	Log          *slog.Logger
	Clock        state.Clock
	BrowseConfig func() BrowseConfig

	LockMaintenance              func() func()
	BoundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh                  EnsureFresh
	EnsureRemember               EnsureRemember
	EnsureForget                 EnsureForget
	ProjectTeam                  func(ctx context.Context, projectID string) (string, error)
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	// RecordOwners/RecordExpected 是观测窄接口的 browse 域窄化端口
	//（核绑定闭包内铸造 browseOwner——域判别留核）。
	RecordOwners     func(sessionID string, gen uint64, ws []capability.Workload)
	RecordExpected   func(sessionID string, gen uint64)
	KickBrowse       func()
	PublishRoutesNow func()

	// EnsureMemo 是 per-会话 Ensure 确保缓存表本体（组合根 make 后注入）。
	EnsureMemo map[string]Memo
}

// BrowseFactory 由组合根注入（engine.Deps.BrowseFactory），把端口袋装配
// 成 Browse。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时
// Browse 面停用：browse 环退化为 no-op 步，routepublish 不合并 ephemeral
// 路由，门面 nil-safe。
type BrowseFactory func(BrowsePorts) Browse
