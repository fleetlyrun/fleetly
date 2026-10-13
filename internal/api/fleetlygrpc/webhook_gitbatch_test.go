package fleetlygrpc

// git 批 compose 摄取的纯函数面：顶层 name 剥除（平台管辖项目名——
// composeintake 对 name 显式拒收，模板仓按上游 compose 惯例常携带，
// 剥除而非拒收）；非 YAML 原样交还（错误措辞由 composeintake 统一给出）。
// 克隆与解析的集成面由现役真仓走查覆盖（受理面 https 白名单使离线假件
// 不可达——防线本身即离线边界）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripComposeProjectName(t *testing.T) {
	withName := []byte("name: torchwood\nservices:\n  web:\n    image: nginx:1.27\n")
	out := stripComposeProjectName(withName)
	assert.NotContains(t, string(out), "name:", "top-level name must be stripped")
	assert.Contains(t, string(out), "services:")

	without := []byte("services:\n  web:\n    image: nginx:1.27\n")
	assert.Equal(t, string(without), string(stripComposeProjectName(without)), "absent name = unchanged")

	broken := []byte("services:\n  web:\n  - broken: [")
	assert.Equal(t, string(broken), string(stripComposeProjectName(broken)), "non-YAML passes through for composeintake's own error")
	assert.False(t, strings.Contains(string(stripComposeProjectName(nil)), "name"))
}
