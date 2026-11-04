package dbtemplate

// 库卷实例标记（ADR-0097 决策 4）：数据库自动卷在数据目录根携带实例标记
// 文件——{"database_id","minted_at"}——使卷自述身份。平台侧（databases 域
// ensure 链）在 Ensure 前经 RuntimeUtility 挂卷读/铸标记：标记在场且
// database_id 不等 = 异源（同名陈旧卷）fail-closed；缺席 = 铸记收编（含
// 预建卷与无标记存量卷——「已制实例启动不受影响」）。标记契约（文件名/
// JSON 形态/读写脚本）住本包（模板注入属材料面）；检测与拒绝语义住
// 平台侧。
//
// 读写脚本是平台常量 sh -c 串（redisRestoreScript 先例：载荷是平台铸造
// 值——ULID 行 id 与 RFC3339 时间戳，字符集天然单引号安全，零用户数据
// 插值）；执行容器 = 模板镜像（sh 必带）。

import (
	"encoding/json"
	"fmt"
	"time"
)

// InstanceMarkerFile 是实例标记文件名（数据目录根）。
const InstanceMarkerFile = "fleetly-instance.json"

// InstanceMarker 是卷内实例标记的自述身份。
type InstanceMarker struct {
	// DatabaseID 是铸记时的数据库行 id（ULID）。
	DatabaseID string `json:"database_id"`
	// MintedAt 是铸记时间（RFC3339，由调用方时钟注入——域不持墙钟，
	// ADR-0087 决策 2 同判据）。
	MintedAt string `json:"minted_at"`
}

// MintInstanceMarker 铸一枚标记（时间经参数入域）。
func MintInstanceMarker(databaseID string, now time.Time) InstanceMarker {
	return InstanceMarker{DatabaseID: databaseID, MintedAt: now.UTC().Format(time.RFC3339)}
}

// renderInstanceMarker 序列化标记（严格字符集闸：database_id 必须是合法
// 平台 id 形态（新形 26 位小写 ULID / 旧名基形——ADR-0092 Valid 双形，
// 快照恢复共存面）、minted_at 必须是 RFC3339——载荷进单引号 shell 串前
// 的单一防线，与 validatePassword 的纵深口径同款）。
func renderInstanceMarker(m InstanceMarker) (string, error) {
	if !validInstanceID(m.DatabaseID) {
		return "", fmt.Errorf("dbtemplate: instance marker database_id %q is not a valid entity id form", m.DatabaseID)
	}
	if _, err := time.Parse(time.RFC3339, m.MintedAt); err != nil {
		return "", fmt.Errorf("dbtemplate: instance marker minted_at %q is not RFC3339", m.MintedAt)
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
		return InstanceMarker{}, fmt.Errorf("dbtemplate: parse instance marker: %w", err)
	}
	if !validInstanceID(m.DatabaseID) {
		return InstanceMarker{}, fmt.Errorf("dbtemplate: instance marker carries no valid database_id (got %q)", m.DatabaseID)
	}
	if _, err := time.Parse(time.RFC3339, m.MintedAt); err != nil {
		return InstanceMarker{}, fmt.Errorf("dbtemplate: instance marker minted_at %q is not RFC3339", m.MintedAt)
	}
	return m, nil
}

// InstanceMarkerClaimArgv 渲染「在场即读、缺席即铸」脚本（ensure 链消费）：
// 标记在场 → cat（stdout 回传比对）；缺席 → 铸记落卷再 cat。幂等：同源
// 重放恒 cat 既有标记。
func InstanceMarkerClaimArgv(mountPoint string, m InstanceMarker) ([]string, error) {
	body, err := renderInstanceMarker(m)
	if err != nil {
		return nil, err
	}
	path := mountPoint + "/" + InstanceMarkerFile
	script := "if [ -e " + path + " ]; then cat " + path + "; " +
		"else printf %s '" + body + "' > " + path + " && cat " + path + "; fi"
	return []string{"sh", "-c", script}, nil
}

// InstanceMarkerReadArgv 渲染只读脚本（受理面前置读消费）：标记在场即
// cat，缺席空输出退出 0（受理位只判「有无异源标记」，铸记归 ensure 链）。
func InstanceMarkerReadArgv(mountPoint string) []string {
	path := mountPoint + "/" + InstanceMarkerFile
	script := "cat " + path + " 2>/dev/null || true"
	return []string{"sh", "-c", script}
}

// validInstanceID 是合法平台 id 形态判据（ADR-0092 Valid 双形：新形
// 26 位小写 crockford ULID / 旧名基形 <name>-<seq>——快照恢复共存面；
// entityid.Valid 的本地等价实现——本包 stdlib-only 近叶子，不 import
// internal/，ADR-0029 包注）。两形态字符集天然单引号 shell 安全。
func validInstanceID(s string) bool {
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
