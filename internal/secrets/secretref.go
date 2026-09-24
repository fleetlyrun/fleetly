package secrets

import (
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/naming"
)

// SwarmSecretRef 是一个 Swarm secret 的平台引用：名字 = naming.SecretName
// （值轮换即换名；v0.3 三段形 `fleetly-<team>-<prj>-<app>-<name>-<hash8>`），
// hash8 随值绑定——引用参与 desired-hash（state-model §2.5 平台覆盖层
// 「secret 引用」），轮换天然触发重部署。
type SwarmSecretRef struct {
	// Name 是 Swarm secret 名。
	Name string
	// Hash8 是内容 sha256 前 8（hex）。
	Hash8 string
}

// SecretRef 由平台密钥库条目构造 Swarm secret 引用（team/prj = 归属 slug，
// v0.3 三段命名参数）。value 是明文——本函数只取其哈希，明文不落到任何
// 返回值/错误（负面测试钉死）。
func SecretRef(team, prj, app, name, value string) (SwarmSecretRef, error) {
	hash8 := naming.Hash8(value)
	secretName, err := naming.SecretName(team, prj, app, name, hash8)
	if err != nil {
		return SwarmSecretRef{}, fmt.Errorf("secrets: build secret ref: %w", err)
	}
	return SwarmSecretRef{Name: secretName, Hash8: hash8}, nil
}
