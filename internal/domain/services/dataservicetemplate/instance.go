package dataservicetemplate

// 卷实例标记契约（ADR-0097 决策 4；B2a 升格自 databasetemplate，字段实体中性化
// ——entity_id/kind/minted_at（wire 键 family 保持），三种数据服务类型通吃，design §3.3 实例重建判据的
// 内核承载）：数据服务自动卷在数据目录根携带实例标记文件，使卷自述身份。
// 平台侧（各类型 ensure 链）在 Ensure 前经 RuntimeUtility 挂卷读/铸标记：
// 标记在场且身份不等 = 异源（同名陈旧卷）fail-closed；缺席 = 铸记收编（含
// 预建卷与无标记存量卷）。标记契约（文件名/JSON 形态/读写脚本）住本包
// （模板注入属材料面）；检测与拒绝语义住平台侧。databasetemplate 是首个消费者
// （kind 词面 "database"）；无兼容层前提下 database_id 旧字段名不复存
// （ADR-0094 开发期无向后兼容义务裁定，staging 可全量重建）。
//
// 读写脚本是平台常量 sh -c 串（redisRestoreScript 先例：载荷是平台铸造
// 值——行 id、类型词面与 RFC3339 时间戳，字符集天然单引号安全，零用户
// 数据插值）；执行容器 = 模板镜像（sh 必带）。执行用户经模板 ToolUser
// 声明面（B8-fix：非 root 引擎镜像声明 root，见 dataservicetemplate.go
// ToolUserDefault 注——root 既写 root 属主卷根，也读 0600/0400 材料）。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InstanceMarkerFile 是实例标记文件名（数据目录根）。
const InstanceMarkerFile = "fleetly-instance.json"

// InstanceMarker 是卷内实例标记的自述身份（实体中性：类型由 kind 承载，
// 身份由 entity_id 承载——databasetemplate 消费面注入 "database"，pubsub/
// bucketstore 随 B4b/B5 各自带词面）。
type InstanceMarker struct {
	// EntityID 是铸记时的实体行 id（数据库行 ULID / 类型对应聚合行 id）。
	EntityID string `json:"entity_id"`
	// Kind 是类型词面（"database"/"pubsub"/"bucketstore"——ADR-0093 词汇；
	// 卷被跨类型复用时标记身份比对的一轴）。wire 键保持 family。
	Kind string `json:"family"`
	// MintedAt 是铸记时间（RFC3339，由调用方时钟注入——域不持墙钟，
	// ADR-0087 决策 2 同判据）。
	MintedAt string `json:"minted_at"`
}

// MintInstanceMarker 铸一枚标记（时间经参数入域）。
func MintInstanceMarker(entityID, kind string, now time.Time) InstanceMarker {
	return InstanceMarker{EntityID: entityID, Kind: kind, MintedAt: now.UTC().Format(time.RFC3339)}
}

// renderInstanceMarker 序列化标记（严格字符集闸：entity_id 必须是合法平台
// id 形态（新形 26 位小写 ULID / 旧名基形——ADR-0092 Valid 双形，快照恢复
// 共存面）、kind 必须是类型词面形态、minted_at 必须是 RFC3339——载荷进
// 单引号 shell 串前的单一防线，与 ValidatePassword 的纵深口径同款）。
func renderInstanceMarker(m InstanceMarker) (string, error) {
	if !ValidInstanceID(m.EntityID) {
		return "", fmt.Errorf("dataservicetemplate: instance marker entity_id %q is not a valid entity id form", m.EntityID)
	}
	if !validKind(m.Kind) {
		return "", fmt.Errorf("dataservicetemplate: instance marker kind %q is not a valid kind word", m.Kind)
	}
	if _, err := time.Parse(time.RFC3339, m.MintedAt); err != nil {
		return "", fmt.Errorf("dataservicetemplate: instance marker minted_at %q is not RFC3339", m.MintedAt)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ParseInstanceMarker 解析标记（读径：stdout 非空才调用；坏形态返回错误
// ——卷里有非标记内容占据该文件名 = 异源事实，交调用方 fail-closed）。
func ParseInstanceMarker(data []byte) (InstanceMarker, error) {
	var m InstanceMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return InstanceMarker{}, fmt.Errorf("dataservicetemplate: parse instance marker: %w", err)
	}
	if !ValidInstanceID(m.EntityID) {
		return InstanceMarker{}, fmt.Errorf("dataservicetemplate: instance marker carries no valid entity_id (got %q)", m.EntityID)
	}
	if !validKind(m.Kind) {
		return InstanceMarker{}, fmt.Errorf("dataservicetemplate: instance marker carries no valid kind word (got %q)", m.Kind)
	}
	if _, err := time.Parse(time.RFC3339, m.MintedAt); err != nil {
		return InstanceMarker{}, fmt.Errorf("dataservicetemplate: instance marker minted_at %q is not RFC3339", m.MintedAt)
	}
	return m, nil
}

// InstanceMarkerClaimArgv 渲染「在场即读、缺席即铸」脚本（ensure 链消费）：
// 标记在场 → cat（stdout 回传比对）；缺席 → 先以镜像内 DataTarget 目录为
// 属主参照播卷根属主（B8-fix 卷属主真相：标记门是卷的首个挂载者，挂点
// /seed 在镜像内无对应目录——docker copy-up 不发生，卷根保持 root 属主，
// 非 root 引擎用户〔kafka appuser/redpanda 101〕随后无法写数据目录；
// `chown $(stat ...)` 从镜像自身取属主 = 属主知识单源在镜像，平台零
// per-engine uid 声明。参照目录缺席〔nats 无 /data〕或非 root 执行时
// chown 静默跳过——镜像自带 root 用户的引擎本就不需要播属主）→ 铸记
// 落卷再 cat。幂等：同源重放恒 cat 既有标记（在场分支零变更——稳态
// ensure 不重复 chown）。
func InstanceMarkerClaimArgv(mountPoint, dataTarget string, m InstanceMarker) ([]string, error) {
	body, err := renderInstanceMarker(m)
	if err != nil {
		return nil, err
	}
	if err := validMarkerPath(mountPoint); err != nil {
		return nil, err
	}
	if err := validMarkerPath(dataTarget); err != nil {
		return nil, err
	}
	path := mountPoint + "/" + InstanceMarkerFile
	script := "if [ -e " + path + " ]; then cat " + path + "; " +
		"else chown \"$(stat -c '%u:%g' " + dataTarget + " 2>/dev/null)\" " + mountPoint + " 2>/dev/null || true; " +
		"printf %s '" + body + "' > " + path + " && cat " + path + "; fi"
	return []string{"sh", "-c", script}, nil
}

// validMarkerPath 钳制进 sh -c 串的挂点/参照路径（平台常量面——模板
// DataTarget 与 SeedMountPoint；绝对路径、[A-Za-z0-9._-]/ 字符集、无空白
// 与引号——载荷进 shell 串前的单一防线，renderInstanceMarker 同口径）。
func validMarkerPath(p string) error {
	if !strings.HasPrefix(p, "/") || p == "/" || strings.ContainsAny(p, " \t\n'\";$&|<>`(){}*?\\") {
		return fmt.Errorf("dataservicetemplate: instance marker path %q is not a plain absolute path", p)
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c == '/', c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("dataservicetemplate: instance marker path %q carries characters outside the shell-safe set", p)
		}
	}
	return nil
}

// InstanceMarkerReadArgv 渲染只读脚本（受理面前置读消费）：标记在场即
// cat，缺席空输出退出 0（受理位只判「有无异源标记」，铸记归 ensure 链）。
func InstanceMarkerReadArgv(mountPoint string) []string {
	path := mountPoint + "/" + InstanceMarkerFile
	script := "cat " + path + " 2>/dev/null || true"
	return []string{"sh", "-c", script}
}

// ValidInstanceID 是合法平台 id 形态判据（ADR-0092 Valid 双形：新形
// 26 位小写 crockford ULID / 旧名基形 <name>-<seq>——快照恢复共存面；
// entityid.Valid 的本地等价实现——本包 stdlib-only 近叶子，不 import
// internal/）。两形态字符集天然单引号 shell 安全。
func ValidInstanceID(s string) bool {
	if len(s) == 26 && isCrockfordLower(s) {
		return true
	}
	// 旧名基形：[a-z0-9]+(-[a-z0-9]+)+（至少一连字符段）。
	if len(s) < 3 || len(s) > 128 {
		return false
	}
	dashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c == '-':
			if i == 0 || i == len(s)-1 || s[i-1] == '-' {
				return false // 首尾/连续连字符不合法
			}
			dashes++
		default:
			return false
		}
	}
	return dashes > 0
}

// validKind 是类型词面判据（[a-z] 起、[a-z0-9-] 体、无尾/连续连字符，
// 界长 32——"database"/"pubsub"/"bucketstore" 词面；载荷进单引号 shell
// 串前的防线之一）。
func validKind(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c == '-':
			if s[i-1] == '-' || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isCrockfordLower 报告是否全落 26 位小写 crockford base32（排除 i/l/o/u）。
func isCrockfordLower(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'h':
		case c >= 'j' && c <= 'k':
		case c >= 'm' && c <= 'n':
		case c >= 'p' && c <= 't':
		case c >= 'v' && c <= 'z':
		default:
			return false
		}
	}
	return true
}
