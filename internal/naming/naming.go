// Package naming 是 fleetly 平台对象命名与最小 label 集的唯一定义点
// （state-model §2.4 对象标记契约 + architecture §2.4 服务命名与网络行）。
//
// 纪律：
//   - 命名公式逐字对齐设计文档，任何字符串改动都意味着契约破坏（表驱动
//     测试对照文档原文钉死，naming_test.go）；
//   - 平台命名只存在于适配器——归一化 compose 不出现平台名（§2.4「平台
//     命名不进归一化 compose」）；调用方 = internal/substrate / 发布引擎；
//   - label 密钥/payload 永不进 label（state-model §2.4）。
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// 平台命名公式（文档原文逐字；state-model §2.4 / architecture §2.4）：
//
//	Swarm 服务名   fleetly-<app>-<service>
//	Secret 名      fleetly-<app>-<name>-<hash8>   （hash8 = 内容 sha256 前 8）
//	卷名           fleetly-<app>-<key>-<appid8>   （appid8 = app ID 前 8）
//	网络名         fleetly-<app>-net              （每 app 专属 overlay；
//	                                                 文档未钉字符串，保守补全，
//	                                                 见包内注释与遗留记录）
const (
	// namePrefix 是全部平台对象名的公共前缀（防集群全局命名空间撞名）。
	namePrefix = "fleetly-"
)

// ServiceName 返回 Swarm 服务名 `fleetly-<app>-<service>`：两个 app 各有
// 同名服务（web/db）不冲突；服务别名 = compose 服务名（app 内短名互访与
// compose 语义一致，见 NetworkAlias）。
func ServiceName(app, service string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("service", service); err != nil {
		return "", err
	}
	return joinName(app, service), nil
}

// SecretName 返回 Swarm secret 名 `fleetly-<app>-<name>-<hash8>`。hash8
// 由调用方经 Hash8(内容) 计算——**值轮换即换名换引用**（architecture §2.4
// 密钥行；desired-hash 以 secret 引用参与，轮换天然触发重部署）。
func SecretName(app, name, hash8 string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("name", name); err != nil {
		return "", err
	}
	if err := validateHash8(hash8); err != nil {
		return "", err
	}
	return joinName(app, name, hash8), nil
}

// VolumeName 返回平台卷名 `fleetly-<app>-<key>-<appid8>`：卷无 label，
// 用命名约定承载归属（state-model §2.4；VolumeOptions.Labels 生效前的
// 收敛路径，stateful-placement §6）。appid8 = app 平台 ID 前 8 位。
func VolumeName(app, key, appID string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("key", key); err != nil {
		return "", err
	}
	id8, err := AppID8(appID)
	if err != nil {
		return "", err
	}
	return joinName(app, key, id8), nil
}

// NetworkName 返回 app 专属 overlay 网络名（保守补全 `fleetly-<app>-net`；
// 文档钉死的是「每 app 专属 overlay 网络 + 服务别名 = compose 服务名」语义，
// 未钉网络名字符串——命名与前缀纪律保持一致，跨 app 网络隔离由专属网络
// 承载，architecture §2.4 服务命名与网络行）。
func NetworkName(app string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	return namePrefix + app + "-net", nil
}

// NetworkAlias 返回服务在 app 网络内的别名 = compose 服务名（app 内短名
// 互访与 compose 语义一致；跨 app 不可见——隔离由每 app 专属网络保证）。
func NetworkAlias(service string) (string, error) {
	if err := validateComponent("service", service); err != nil {
		return "", err
	}
	return service, nil
}

// AppID8 返回 app 平台 ID 的前 8 位（卷命名防撞尾缀）。ULID 为 26 位，
// 前 8 位已含高精度时间戳成分。
func AppID8(appID string) (string, error) {
	if len(appID) < 8 {
		return "", fmt.Errorf("naming: app id %q shorter than 8 chars", appID)
	}
	return appID[:8], nil
}

// Hash8 返回内容 sha256 hex 前 8 位（secret 命名尾缀：值轮换即换名）。
func Hash8(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:8]
}

// joinName 以 '-' 连接命名成分。
func joinName(parts ...string) string {
	return namePrefix + strings.Join(parts, "-")
}

// validateComponent 校验命名成分：非空、不含命名分隔符 '-' 之外的非法字符
// 以外交界字符（compose 标识符字符集 [A-Za-z0-9._-]），防止注入分隔层级或
// 产生非法 DNS 名。
func validateComponent(field, v string) error {
	if v == "" {
		return fmt.Errorf("naming: %s is empty", field)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("naming: %s %q contains character %q outside [A-Za-z0-9._-]", field, v, r)
		}
	}
	return nil
}

// validateHash8 校验 hash8 形态（8 位小写 hex）。
func validateHash8(h string) error {
	if len(h) != 8 {
		return fmt.Errorf("naming: hash8 %q must be 8 hex chars", h)
	}
	for _, r := range h {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return fmt.Errorf("naming: hash8 %q must be lowercase hex", h)
		}
	}
	return nil
}
