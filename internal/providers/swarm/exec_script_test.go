package swarm

// nodeRelayScript 纯函数钉板（F3.2，ADR-0049）：形态约束是消费面契约——
// 纯单引号（JSON 转义恒等，e2e/runbook 可原样 sed 抽取执行）、幂等锚点
// （rm -f 旧中继）、curl/wget 双通道、busybox 钉版载体与 relay 旗标面。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeRelayScriptShape(t *testing.T) {
	script := nodeRelayScript("http://10.0.0.3:9081", "SWMTKN-1-abc")

	// 纯单引号形态：JSON 转义恒等（无双引号/反斜杠字符面）。
	assert.NotContains(t, script, `"`, "script must be single-quote-only (JSON-escape identity)")
	assert.NotContains(t, script, `\`, "script must carry no backslashes (JSON-escape identity)")

	// 幂等锚：重建前先清旧中继（重跑 = 升级/修复通道）。
	assert.Contains(t, script, "docker rm -f fleetly-relay")

	// 载体与中继形态：busybox 钉版 + docker.sock 挂载 + 自愈重启策略。
	assert.Contains(t, script, "busybox:1.37")
	assert.Contains(t, script, "-v /var/run/docker.sock:/var/run/docker.sock")
	assert.Contains(t, script, "--restart unless-stopped")

	// 双通道下载 + 二进制入口端点与凭证头。
	assert.Contains(t, script, "curl -sfL -H 'Authorization: Bearer SWMTKN-1-abc' -o $t http://10.0.0.3:9081/v1/platform/binary")
	assert.Contains(t, script, "wget -q --header 'Authorization: Bearer SWMTKN-1-abc' -O $t http://10.0.0.3:9081/v1/platform/binary")

	// relay 旗标面：manager 基址与 join token 注入容器 argv。
	assert.Contains(t, script, "exec /tmp/fleetlyd relay --manager http://10.0.0.3:9081 --join-token SWMTKN-1-abc")

	// 二进制经 docker cp 注入（chmod +x 后 exec——busybox sh 面承载）。
	assert.Contains(t, script, "docker cp $t $c:/tmp/fleetlyd")
	assert.Contains(t, script, "chmod +x /tmp/fleetlyd")

	// 确定性：同输入同输出（golden 夹具友好）。token 形态受控（swarm
	// SWMTKN-* 字符集，无引号字符）——注入面在生成端闭合。
	require.Equal(t, script, nodeRelayScript("http://10.0.0.3:9081", "SWMTKN-1-abc"))
}
