package capability

// registry 身份规则的唯一真源（2026-10-03 架构评审候选 5）：镜像引用的
// registry 主机提取与 X-Registry-Auth 凭证编码此前在 engine（材料解析）/
// swarm（拉取凭证）/ builders（推送凭证）三面各持一份（注释互认同口径，
// 编码副本已出现错误包装级分歧）——规则漂移不在编译期失败，而是在三个
// 凭证面上以运行期降级失败（推/拉/build-push 认证不一致）。capability
// 是 import 守卫认可的共享面（providers 只消费 capability；engine 同）。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ImageRegistryHost 提取镜像引用的 registry 主机（含默认 docker.io 归一）：
// 首个路径段含 "." 或 ":"、或等于 "localhost" 即主机；否则视为默认仓库的
// 官方镜像命名空间。
func ImageRegistryHost(image string) string {
	if i := strings.IndexByte(image, '/'); i >= 0 {
		first := image[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first
		}
	}
	return "docker.io"
}

// EncodeRegistryAuth 把平台凭证编码为 X-Registry-Auth 头值（base64 JSON；
// map 形态同各历史副本——map 键不触发 gosec G117 的结构体字段模式）。
func EncodeRegistryAuth(c RegistryCredential) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"username":      c.Username,
		"password":      c.Secret,
		"serveraddress": c.Server,
	})
	if err != nil {
		return "", fmt.Errorf("encode registry auth: %w", err)
	}
	return base64.StdEncoding.EncodeToString(payload), nil
}
