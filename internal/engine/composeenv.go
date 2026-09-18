package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// compose 文件的 env 明文提取（发布引擎的值注入前站）：归一化 Spec 的
// Environment 只含 key:sha256+来源（脱敏结构性成立，internal/compose 契约），
// 而 Swarm 服务注入需要明文 KEY=VALUE。本文件按同一文件的 services.* 段
// 提取两层明文（env_file < environment——与归一化合并链同序），键集与
// 归一化形态交叉核对（防文件在入队/准备之间被改写后静默漂移）。
//
// 纪律：本文件不做子集校验（compose.Load 已做）——提取失败按
// E_COMPOSE_UNSUPPORTED 显式失败（防御路径，理论不可达）；明文只进
// envlayer.MergeChain 与密文快照，不进日志/事件/审计。

// serviceEnv 是单个服务的文件层明文（env_file 与 environment 分源——
// 三层合并的来源标注需要）。
type serviceEnv struct {
	File    map[string]string
	Compose map[string]string
}

// extractServiceEnvs 解析 compose 文件，返回 {服务名 → 文件层明文}。
func extractServiceEnvs(composePath string) (map[string]serviceEnv, error) {
	raw, err := os.ReadFile(composePath) //nolint:gosec // G304：路径为部署记录里的 compose 绝对路径（入队时受控子集校验过）
	if err != nil {
		return nil, apperr.New("E_COMPOSE_UNSUPPORTED", "compose 文件不可读 %s: %v", composePath, err).WithCause(err)
	}
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, apperr.New("E_COMPOSE_UNSUPPORTED", "compose 解析失败（%s）：%v", filepath.Base(composePath), err).WithCause(err)
	}
	out := map[string]serviceEnv{}
	workDir := filepath.Dir(composePath)
	for name, svcAny := range doc.Services {
		svc, ok := svcAny.(map[string]any)
		if !ok {
			continue // 校验层已拒绝非映射服务；此处防御性跳过
		}
		env := serviceEnv{File: map[string]string{}, Compose: map[string]string{}}
		// env_file：字符串 / 列表（含长语法 {path, required}）。
		for _, ef := range envFileEntries(svc["env_file"]) {
			path := ef.path
			if !filepath.IsAbs(path) {
				path = filepath.Join(workDir, path)
			}
			kvs, err := parseEnvFileKV(path)
			if err != nil {
				if ef.required && os.IsNotExist(err) {
					return nil, apperr.New("E_COMPOSE_UNSUPPORTED", "服务 %q 的 env_file %s 不可读: %v", name, ef.path, err).WithCause(err)
				}
				if ef.required {
					return nil, apperr.New("E_COMPOSE_UNSUPPORTED", "服务 %q 的 env_file %s 解析失败: %v", name, ef.path, err).WithCause(err)
				}
				continue // required: false 显式缺省容忍（与归一化层一致）
			}
			for k, v := range kvs {
				env.File[k] = v // 多个 env_file：后者覆盖前者（compose 语义）
			}
		}
		// environment：dict 或 "K=V" 列表（字面值；插值已全局关闭）。
		switch v := svc["environment"].(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				val, ok := stringish(v[k])
				if !ok {
					return nil, apperr.New("E_COMPOSE_UNSUPPORTED", "服务 %q 的 environment 条目 %q 未给字面值", name, k).
						WithContext("path", "services."+name+".environment."+k)
				}
				env.Compose[k] = val
			}
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok || !strings.Contains(s, "=") {
					return nil, apperr.New("E_COMPOSE_UNSUPPORTED", "服务 %q 的 environment 条目 %q 未给字面值", name, s).
						WithContext("path", "services."+name+".environment")
				}
				k, val, _ := strings.Cut(s, "=")
				env.Compose[k] = val
			}
		}
		out[name] = env
	}
	return out, nil
}

// envFileEntry 是 env_file 的解析项。
type envFileEntry struct {
	path     string
	required bool
}

// envFileEntries 归一 env_file 书写形态（字符串/字符串列表/长语法列表）。
func envFileEntries(v any) []envFileEntry {
	switch t := v.(type) {
	case string:
		return []envFileEntry{{path: t, required: true}}
	case []any:
		var out []envFileEntry
		for _, item := range t {
			switch e := item.(type) {
			case string:
				out = append(out, envFileEntry{path: e, required: true})
			case map[string]any:
				p, _ := e["path"].(string)
				req := true
				if r, ok := e["required"].(bool); ok {
					req = r
				}
				if p != "" {
					out = append(out, envFileEntry{path: p, required: req})
				}
			}
		}
		return out
	}
	return nil
}

// parseEnvFileKV 解析 KEY=VAL 行（# 注释与空行跳过；首尾引号剥离——与
// gotenv 惯例一致的窄实现，受控子集的 env_file 只承载非密钥字面值）。
func parseEnvFileKV(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304：compose env_file 显式声明路径
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue // 裸键行无法取值；归一化层已按契约拒绝语义冲突
		}
		out[strings.TrimSpace(k)] = trimEnvValue(v)
	}
	return out, nil
}

// trimEnvValue 剥离成对首尾引号。
func trimEnvValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// stringish 把 YAML 标量读为字符串（数值/布尔字面量同样按字面值注入）。
func stringish(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int:
		return fmt.Sprintf("%d", t), true
	case int64:
		return fmt.Sprintf("%d", t), true
	case uint64:
		return fmt.Sprintf("%d", t), true
	case float64:
		return fmt.Sprintf("%v", t), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case nil:
		return "", false
	default:
		return "", false
	}
}
