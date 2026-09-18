// Package gitserver 是 git push(SSH) 与 webhook 两条部署触发入口的 daemon
// 侧实现（T2.19）：
//
//   - SSH 服务器（golang.org/x/crypto/ssh）：认证 = 平台管理的 git 公钥
//     （state 层 git_keys 表，SHA256 指纹匹配）；exec 白名单限定
//     git-receive-pack / git-upload-pack（防 shell 注入——git 直接 exec、
//     app 名严格校验）；bare 仓库按 app 懒创建并生成 post-receive 钩子；
//   - webhook（gateway 原生 HTTP handler 消费本包 Handler）：强制 HMAC-
//     SHA256 验签 + delivery ID TTL 防重放 + (app, sha) 幂等去重 + 拉源；
//   - 两条入口汇入同一 DeployFromCommit：compose 真源在 git 对象库
//     （`git show <sha>:compose.{yaml,yml}`），不信任客户端传字节。
//
// 包纪律：不 import 任何框架类型（lynx.Service 壳在 cmd/fleetlyd）；git
// 操作全部 exec 系统 git（宿主 git 为前置条件，不引入 go-git 等重框架）。
package gitserver

import (
	"errors"
	"time"
)

// 安全默认基线（architecture §4.2）：SSH git 面默认只绑回环——VPS 上由
// 安装/文档指引改为对外；重放窗口默认 15 分钟。
const (
	// DefaultAddr 是 SSH git 面的缺省监听地址。
	DefaultAddr = "127.0.0.1:8424"
	// DefaultReplayTTL 是 webhook delivery ID 防重放缓存窗口（config
	// webhook.replay_ttl_seconds 缺省 900s = 15 分钟）。
	DefaultReplayTTL = 15 * time.Minute
	// TimestampWindow 是自定义投递方时间戳头（X-Fleetly-Timestamp）的
	// 接受窗口（±5 分钟）——绑定口径，不可配置。
	TimestampWindow = 5 * time.Minute
)

// Config 是 git 触发入口配置（config 键 git.* 与 webhook.* 归并承载——
// webhook.* 只有重放窗口一键，随本配置节装配）。
type Config struct {
	// Enabled 报告是否启用 SSH git 面（git.enabled；缺省 true）。
	Enabled bool
	// Addr 是 SSH 监听地址（git.addr；缺省 127.0.0.1:8424）。
	Addr string
	// Root 是 bare 仓库根目录（git.root；缺省与 state 库同目录下 git/，
	// 由装配方计算回落——本包不感知 db 路径）。
	Root string
	// HostKeyFile 是 SSH host key 文件路径（git.host_key_file；缺省
	// <root>/host_ed25519。首启不存在自动生成 ed25519 并持久化，二次
	// 启动复用——类比平台节点身份；私钥绝不打印）。
	HostKeyFile string
	// HookEndpoint 是 post-receive 钩子回调的控制面 HTTP 基址
	// （如 http://127.0.0.1:8420；装配方由 addr 键推导）。
	HookEndpoint string
	// ReplayTTL 是 webhook delivery ID 防重放窗口
	//（webhook.replay_ttl_seconds；缺省 15 分钟）。
	ReplayTTL time.Duration
}

// Normalize 回落缺省值（装配期调用；Root 为空不在此兜底——它依赖 state
// 库路径，由 cmd/fleetlyd 装配点计算，此处仅在启用时校验非空）。
func (c Config) Normalize() Config {
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	if c.ReplayTTL <= 0 {
		c.ReplayTTL = DefaultReplayTTL
	}
	return c
}

// Validate 启用态配置完整性检查（装配期 fail-fast）。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Root == "" {
		return errors.New("gitserver: git.root is empty")
	}
	if c.HookEndpoint == "" {
		return errors.New("gitserver: hook endpoint is empty")
	}
	return nil
}
