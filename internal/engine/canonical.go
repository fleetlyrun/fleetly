package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// canonicalJSON 输出确定性 JSON（键排序由 encoding/json 保证、HTML 转义
// 关闭、无尾部换行）——desired-hash 与快照的统一编码。
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// canonicalHash 是 canonicalJSON 的 sha256 hex（期望态哈希统一入口）。
func canonicalHash(v any) string {
	raw, err := canonicalJSON(v)
	if err != nil {
		// ServiceSpec 全为可序列化字段，理论不可达；防御性回落稳定值并
		// 保留差异线索（不 panic——引擎循环内的哈希失败不应炸进程）。
		return "sha-err-" + fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v", v))))[:8]
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
