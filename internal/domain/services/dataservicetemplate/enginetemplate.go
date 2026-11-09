// Package dataservicetemplate 是托管数据服务三种数据服务类型模板包的共享内核（B2a 下沉，
// ADR-0094 决策 2 + design docs/design/2026-11-04-managed-data-services.md
// §1）：版本矩阵形状（VersionSpec）、digest 钉定拼装（PinnedRef）、转储/
// 注入/轮换 spec 形状、平台凭证铸造与渲染闸、卷实例标记契约（ADR-0097
// 决策 4 升格——实体中性字段，三种数据服务类型通吃）。来源：全部符号自 databasetemplate
// 下沉（2026-11-05，B2a；engine/contract facts.go 的 P5 下沉先例同款）——
// databasetemplate 经类型别名/var 别名零漂移消费，pubsubtemplate/bucketstoretemplate 随
// B4b/B5 直接消费。
//
// 本包只承载跨类型共享的机制形状，不含任何 per-engine 知识（镜像/端口/
// 数据目录/方言住各类型模板包的 adapter——引擎知识单源纪律，engine 名
// 反扫契约测试执法）；stdlib-only 近叶子（不 import internal/，与 databasetemplate
// 同口径）。
package dataservicetemplate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// VersionSpec 是一个引擎版本的在册条目（ADR-0094 决策 1 三层解耦的注册表
// 载体）：ID 是用户面 major 短名（行值/回显/API 值域，创建时选定、存续期
// 不可变——如 "17"、"0.8-pg18"）；Tag 浮动住 suite/minor 级（如
// "17-bookworm"）；Digest 钉 patch 住 index sha256（ADR-0045 律延伸至全
// 版本矩阵）。digest bump 不动行值——追 patch 是纯平台行为；major 换值只
// 经升级路径。
type VersionSpec struct {
	ID string
	// Tag 是镜像 tag（不含仓库名——repo 由各引擎 adapter 自持）。
	Tag string
	// Digest 是 index digest（ADR-0045 律：tag@sha256 双段，multi-arch
	// 真源）。
	Digest string
	// Default 标记缺省版本（每引擎恰一真，门禁钉死；version 空 = 取此）。
	Default bool
	// Deprecated 拒全新建（存量全语义续跑；同版本带 restore 的同名重建
	// 放行——灾备义，ADR-0094 决策 6；口径由受理位承载）。
	Deprecated bool
	// Note 是弃用理由/移除窗口（目录回显；建议 = 转弃用起两个发布周期）。
	Note string
}

// PinnedRef 组装 digest 钉定引用（ADR-0045；B2a 自 databasetemplate.pinnedRef
// 下沉）：ref = repo:tag 与 digest 拼成 tag@digest 双段形态的唯一拼装点
// ——digest 主导（编排器按内容寻址拉取），tag 是可读性面（载体/事件流里
// 版本可见）。钉的是 OCI index（manifest-list）digest——multi-arch 真源，
// registry API Docker-Content-Digest 头原值。
func PinnedRef(ref, digest string) string { return ref + "@" + digest }

// BackupSpec 是一次备份的引擎渲染产物（argv 纯数组、零 shell 拼串——args
// 数组文化，shellguard 射程不变）。执行器把 SecretFiles 落为工具容器的
// /run/secrets/<名>（与 DB Workload 材料同语义），转储产物流走工具容器
// stdout（digest 由 Put 流式铸造）。
type BackupSpec struct {
	Argv []string
	// Env 只承载路径引用类值；唯一登记例外 = redis REDISCLI_AUTH（redis-cli
	// 无文件面，ADR-0039 决策 5 例外注）。
	Env         map[string]string
	SecretFiles map[string][]byte
}

// RestoreMode 是恢复执行形态。
type RestoreMode string

const (
	// RestoreStream：转储流经工具容器 stdin 注入运行中的目标库
	// （pg_restore / mysql / mongorestore）。
	RestoreStream RestoreMode = "stream"
	// RestorePreseed：转储流经工具容器 stdin 落为数据卷内文件（redis RDB
	// 仅启动装载——恢复发生在库首启前；执行器把库的平台卷挂到
	// SeedMountPoint 后执行 Argv）。
	RestorePreseed RestoreMode = "preseed"
)

// SeedMountPoint 是预置卷形态下工具容器挂载数据卷的容器内固定挂点
// （模板 argv 与执行器共用的单源常量；也是卷实例标记读/铸脚本的挂载面，
// ADR-0097 决策 4）。
const SeedMountPoint = "/seed"

// BackupInputPath 是恢复流的输入文件容器内固定挂载路径（ADR-0039 落地
// 实录：hijack attach 的 CloseWrite 不向容器 stdin 送 EOF——dind 实证
// mysql 客户端读流永挂；恢复流经文件挂载承载，模板 argv 与执行器共用
// 本单源）。落在 /run/secrets 命名空间（与材料凭证同域——备份档与凭证
// 同保密级）：swarm 侧文件级 bind 任意路径成立；k3s 侧与材料同 projected
// 卷投影（实证通道——fuse 深层根上新顶层目录的挂载点创建不达，ADR-0052
// e2e 批实证矩阵）。
const BackupInputPath = "/run/secrets/backup-input"

// RestoreSpec 是一次恢复的引擎渲染产物。
type RestoreSpec struct {
	Mode RestoreMode
	// Argv：stream 形态 = 恢复客户端（stdin = 转储流）；preseed 形态 =
	// 落盘命令（stdin = 转储流，写进 SeedMountPoint 下的卷内路径）。
	Argv        []string
	Env         map[string]string
	SecretFiles map[string][]byte
}

// RotateSpec 是一次凭证轮换的引擎渲染产物。Argv 空 = 声明式轮换（无
// utility；Secret 重写后由收敛循环重下发载体生效——旧值在新载体上线前保持
// 有效，无破窗）；Argv 非空 = 数据面改密命令（工具容器以 current 认证
// 执行，Secret 重写在成功后落）。
type RotateSpec struct {
	Argv        []string
	Env         map[string]string
	SecretFiles map[string][]byte
}

// ToolUserDefault 是各类型 Template 的 ToolUser 声明面缺省实现（嵌入即得
// 「镜像自带用户」语义——B8-fix，2026-11-07 用户裁定主案）：工具容器
// （标记门/热应用/轮换/子资源 admin 面）缺省按镜像自带用户跑；非 root
// 引擎镜像（kafka appuser/redpanda uid 101）对 root 属主的新卷根与
// 0600/0400 凭证材料均无权读写——此类引擎在 adapter 显式覆盖 ToolUser
// 返回 "root"（工具面是平台渲染的 argv+材料，root 运行不引入用户不可信
// 载荷；实例载体本身仍按镜像用户跑，不受本面影响）。声明值经
// capability.UtilityRequest.User 通道落地（swarm ContainerSpec.User /
// k3s container securityContext.runAsUser）；凭证文件权限维持 0600/0400
// 不动（root 可读——ADR-0014 纪律与 libpq passfile 约束零触碰）。
type ToolUserDefault struct{}

// ToolUser 报告工具容器运行用户（缺省空 = 镜像自带用户）。
func (ToolUserDefault) ToolUser() string { return "" }

// MintPassword 铸平台随机密码（hex 48 = crypto/rand 24 字节；创建面与
// 轮换面共用的公式单源——用户面无覆写通道，ValidatePassword 是渲染面
// 纵深闸）。熵不可用即 panic：退化为弱密码不可接受。
func MintPassword() string {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		panic("dataservicetemplate: entropy source unavailable for data service credentials: " + err.Error())
	}
	return hex.EncodeToString(raw)
}

// ValidatePassword 是渲染面的共享安全闸：密码只准 [0-9A-Za-z]。动机是
// 引擎渲染面的并集约束——pgpass 行格式冒号不可转义、redis.conf 行语法
// 空白即断、mongosh init 脚本单引号串、mysql INI 换行即注入；平台铸造
// 公式 = hex 48（crypto/rand），用户面无覆写通道，本闸是纵深防御而非
// 受理位（hostile 值到不了这里也照样拒）。
func ValidatePassword(password string) error {
	if password == "" {
		return fmt.Errorf("dataservicetemplate: password must not be empty")
	}
	for i := 0; i < len(password); i++ {
		c := password[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		return fmt.Errorf("dataservicetemplate: password carries characters outside the render-safe set [0-9A-Za-z]")
	}
	return nil
}
