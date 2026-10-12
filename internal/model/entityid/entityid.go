// Package entityid 是平台实体 id 的铸造与派生单源（ADR-0070）：id 形如
// "<base>-<4位>"（web-m2x8），suffix 取小写 Crockford base32（去 i/l/o/u）。
// id 只管身份、不承载顺序语义——创建序唯一真源是各行 seq 列。铸造发生在
// 行装配处（repo Create / minttask / node anchor / 会话装配点），exists
// 查重含 tombstone——never-reuse 承诺（ADR-0023/0029）由此保持。
package entityid

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

// SuffixLen 是随机后缀位宽。32^4 ≈ 105 万/base：同名重建防撞空间充足，
// 有界重试下铸造耗尽概率可忽略。
const SuffixLen = 4

// MaxLen 是 id 总长上限（TEXT 主键无实际约束，此界只为派生名/DNS 链条
// 留出可推理的边界；载体名超长由各 Provider 既有截断+哈希兜底）。
const MaxLen = 128

// alphabet 是小写 Crockford base32：0-9 + 去除易混字母 i/l/o/u 的 22 个
// 小写字母，恰 32 符号（= 5 bit，采样无偏）。
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// pattern 是平台实体 id 全形：小写字母数字段以单连字符相连（无首尾
// 连字符、无连续连字符）。id 是不透明字符串，全仓只做此形态校验。
var pattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)+$`)

// namePattern 是 id base 的形态（= 有名实体 name 的 dns 标签形态，
// ADR-0070 决策 4）：字符集安全是 fleetly-<projectID> 命名空间、
// db-<id> DNS 名等派生名的前提。
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// MaxNameLen 是 base（= 有名实体 name）长度上限：dns 标签惯例 63。
const MaxNameLen = 63

// ErrMintExhausted 是铸造有界重试耗尽的哨兵：同一 base 连续撞满重试
// 次数（概率可忽略，出现即是 exists 查重面有缺陷的信号）。
var ErrMintExhausted = errors.New("entityid: mint retries exhausted")

// mintRetries 是单次铸造的最大随机尝试数。
const mintRetries = 8

// Valid 报告 s 是否为合法平台实体 id 形态。
func Valid(s string) bool {
	return len(s) <= MaxLen && pattern.MatchString(s)
}

// ValidName 报告 s 是否可作为 id base（= 有名实体 name 的合法形态）。
func ValidName(s string) bool {
	return len(s) <= MaxNameLen && namePattern.MatchString(s)
}

// Mint 铸造 "<base>-<4位>"：suffix 逐位取 crypto/rand（256/32=8 整除，
// 掩码采样无偏）；exists 报告候选是否已占用（查询须含 tombstone 行）。
// base 须先过 ValidName（派生链的 base 是父 id，天然满足）。
func Mint(base string, exists func(id string) (bool, error)) (string, error) {
	if !ValidName(base) || len(base)+1+SuffixLen > MaxLen {
		return "", fmt.Errorf("entityid: invalid base %q", base)
	}
	for range mintRetries {
		id, err := mintOnce(base)
		if err != nil {
			return "", err
		}
		taken, err := exists(id)
		if err != nil {
			return "", err
		}
		if !taken {
			return id, nil
		}
	}
	return "", ErrMintExhausted
}

func mintOnce(base string) (string, error) {
	buf := make([]byte, SuffixLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("entityid: rand: %w", err)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])&31]
	}
	return base + "-" + string(buf), nil
}

// Revision 由 app id 与 revision seq 派生确定性 revision id
// （web-m2x8-r42）：seq 每 app 单调，id 唯一且永不复用。
func Revision(appID string, seq int64) string {
	return fmt.Sprintf("%s-r%d", appID, seq)
}

// Audit 由 audit 行 seq 派生确定性审计 id（aud-42）：审计只增，seq 全局
// 单调即 id 唯一。
func Audit(seq int64) string {
	return fmt.Sprintf("aud-%d", seq)
}
