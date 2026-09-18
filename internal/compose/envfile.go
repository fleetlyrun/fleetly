package compose

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// parseEnvFile 按「字面值、无插值」纪律解析 env_file（compose 语义的最小
// 确定性子集）：
//   - 忽略空行与 # 注释行；
//   - 可选 export 前缀；
//   - KEY=VALUE 按首个 = 切分，KEY 与 VALUE 两侧空白剔除；
//   - 成对单/双引号剥离为字面值（不做转义展开、不做 ${} 插值——架构 §2.4
//     「变量插值关闭」，因此不使用 compose-go dotenv：其解析器会做
//     文件内 ${} 展开，违反字面值纪律）。
//
// 不支持的形态（行内注释、多行值、变量引用）显式报错，不静默吞掉。
func parseEnvFile(service, path string, content []byte) (map[string]string, error) {
	out := map[string]string{}
	for i, raw := range strings.Split(string(content), "\n") {
		lineCtx := fmt.Sprintf("services.%s.env_file(%s:%d)", service, path, i+1)
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, errCompose("服务 %q 的 env_file %s 行格式非法（要求 KEY=VALUE）", service, lineCtx).
				WithContext("path", lineCtx)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, errCompose("服务 %q 的 env_file %s 键名为空", service, lineCtx).
				WithContext("path", lineCtx)
		}
		if strings.ContainsAny(key, " \t") {
			return nil, errCompose("服务 %q 的 env_file %s 键名含空白", service, lineCtx).
				WithContext("path", lineCtx)
		}
		out[key] = unquoteLiteral(value)
	}
	return out, nil
}

// unquoteLiteral 剥离成对包裹的单/双引号（无转义处理——字面值纪律）。
func unquoteLiteral(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// sha256Hex 计算值的 sha256 hex（env 归一化表示 key:sha256(value)）。
func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return fmt.Sprintf("%x", sum)
}
