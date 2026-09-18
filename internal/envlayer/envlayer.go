// Package envlayer 是变量三层合并链（architecture §2.4 变量合并行，
// 2026-09-17 审核裁决）：
//
//	env_file < environment（compose） < 平台 env_vars
//
// 同键平台层覆盖；合并结果（key + sha256 + 来源）进 desired-hash 与
// revision 快照；`fleetly env set` 创建 pending、随下次部署生效——合并
// 消费方（发布引擎 T2.10）只应传入 effective 平台层（state.EffectiveAppEnv），
// pending 不参与当前合并，生效语义由此结构性成立。
//
// source=system 平台 env（模板自动连接串，只读展示）在本层留位：词表支持、
// 平台层内部 system > platform，v0.1 无连接串生产者。
//
// 明文纪律：本包是发布引擎的值注入前站——明文只在返回的 Merged.Value 中
// 存活，不进日志/错误；脱敏形态（key+hash+source）经 MergedVar.Hash 参与
// 快照（与 internal/compose.EnvVar 同构）。
package envlayer

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/compose"
)

// Source 是合并结果条目的来源标注（词表：envfile|compose|platform|system）。
type Source string

const (
	// SourceEnvFile 来自服务 env_file（compose 文件层最低优先级）。
	SourceEnvFile Source = "envfile"
	// SourceCompose 来自服务 environment 段。
	SourceCompose Source = "compose"
	// SourcePlatform 来自平台 env_vars（status=effective）。
	SourcePlatform Source = "platform"
	// SourceSystem 来自平台 system 层（模板连接串留位，只读；v0.1 无生产者）。
	SourceSystem Source = "system"
)

// PlatformVar 是平台层输入（state.EnvVar 的值投影：Key/Value/Source）。
// Source 取 platform|system；同键时 system 覆盖 platform（平台层内部
// 优先级，保守裁决：连接串留位高于用户手设）。
type PlatformVar struct {
	Key    string
	Value  string
	Source string // platform | system
}

// Merged 是一条合并结果。Value 是明文（发布引擎注入用，不落任何持久层）；
// Hash 是 sha256(value) hex（快照/diff 的脱敏形态）。
type Merged struct {
	Key    string
	Value  string
	Source Source
	Hash   string
}

// MergeChain 执行三层合并：envFile < composeEnv < platform（平台层内部
// system > platform）。返回按 key 字典序的合并结果；同时返回被平台层覆盖
// 的文件层键清单（platform 或 system 键命中 envfile/compose 同名键），
// 调用方据此产出 W_ENV_PLATFORM_OVERRIDE 计划警告。
func MergeChain(envFile, composeEnv map[string]string, platform []PlatformVar) (merged []Merged, overridden []string) {
	out := map[string]Merged{}
	// 第一层：env_file。
	for _, k := range sortedKeys(envFile) {
		out[k] = entry(k, envFile[k], SourceEnvFile)
	}
	// 第二层：environment（compose）。
	for _, k := range sortedKeys(composeEnv) {
		out[k] = entry(k, composeEnv[k], SourceCompose)
	}
	// 第三层：平台 env_vars（system > platform；均覆盖文件层）。
	fileKeys := map[string]bool{}
	for k := range envFile {
		fileKeys[k] = true
	}
	for k := range composeEnv {
		fileKeys[k] = true
	}
	overriddenSeen := map[string]bool{}
	var sysOverwrites []PlatformVar
	for _, v := range platform {
		if v.Source == string(SourceSystem) {
			sysOverwrites = append(sysOverwrites, v)
			continue
		}
		applyPlatform(out, fileKeys, &overridden, overriddenSeen, v, SourcePlatform)
	}
	for _, v := range sysOverwrites {
		applyPlatform(out, fileKeys, &overridden, overriddenSeen, v, SourceSystem)
	}

	for _, k := range sortedKeys2(out) {
		merged = append(merged, out[k])
	}
	sort.Strings(overridden)
	return merged, overridden
}

// applyPlatform 覆盖单条平台变量并登记覆盖键（同键去重——覆盖清单是键集）。
func applyPlatform(out map[string]Merged, fileKeys map[string]bool, overridden *[]string, seen map[string]bool, v PlatformVar, src Source) {
	if fileKeys[v.Key] && !seen[v.Key] {
		seen[v.Key] = true
		*overridden = append(*overridden, v.Key)
	}
	out[v.Key] = entry(v.Key, v.Value, src)
}

// entry 构造合并条目（Hash = sha256 hex，快照脱敏形态）。
func entry(key, value string, src Source) Merged {
	return Merged{Key: key, Value: value, Source: src, Hash: hashHex(value)}
}

// hashHex 是 sha256 hex（与 compose.EnvVar.Hash 同构——合并结果快照按
// key:sha256+来源参与 desired-hash）。
func hashHex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// sortedKeys 是 map 键字典序（第一/二层遍历确定性）。
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedKeys2 是合并结果键字典序（输出确定性）。
func sortedKeys2(m map[string]Merged) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PlatformOverrideWarnings 由目标 Spec 与平台 env_vars 产出 W_ENV_PLATFORM_OVERRIDE
// 计划/校验警告（architecture §2.4 变量合并行「覆盖键在 plan/diff 告警」）。
// spec 内服务 env 键 = 文件层键（归一化形态只含 key+hash+source，键名可见）；
// 平台层 pending 条目同样计入——下次部署即其生效点（env set 创建 pending，
// 部署消费），警告语义面向「下一次发布会发生什么」。
func PlatformOverrideWarnings(spec *compose.Spec, platform []PlatformVar) []compose.Warning {
	if spec == nil || len(platform) == 0 {
		return nil
	}
	platformKeys := map[string]bool{}
	for _, v := range platform {
		platformKeys[v.Key] = true
	}
	var out []compose.Warning
	for i := range spec.Services {
		svc := &spec.Services[i]
		for _, e := range svc.Environment {
			if platformKeys[e.Key] {
				out = append(out, compose.Warning{
					Code:    "W_ENV_PLATFORM_OVERRIDE",
					Service: svc.Name,
					Message: "键 " + e.Key + " 在 compose 层（来源 " + e.Source + "）与平台 env_vars 同名：合并以平台层为准，compose 值被覆盖",
				})
			}
		}
	}
	return out
}
