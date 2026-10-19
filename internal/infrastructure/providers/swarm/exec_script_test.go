package swarm

// nodeRelayScript 纯函数钉板（F3.2，ADR-0049 → H10 改个体凭证）：形态
// 约束是消费面契约——纯单引号（JSON 转义恒等，e2e/runbook 可原样 sed
// 抽取执行）、幂等锚点（rm -f 旧中继）、curl/wget 双通道、busybox 钉版
// 载体与 relay 旗标面。脚本只携节点个体凭证，不含 join token。

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

func TestNodeRelayScriptShape(t *testing.T) {
	script := nodeRelayScript("http://10.0.0.3:9081", "fltrelay_testcredential")

	// 纯单引号形态：JSON 转义恒等（无双引号/反斜杠字符面）。
	assert.NotContains(t, script, `"`, "script must be single-quote-only (JSON-escape identity)")
	assert.NotContains(t, script, `\`, "script must carry no backslashes (JSON-escape identity)")

	// 幂等锚：重建前先清旧中继（重跑 = 升级/修复通道）。
	assert.Contains(t, script, "docker rm -f fleetly-relay")

	// 载体与中继形态：busybox 钉版 + docker.sock 挂载 + 自愈重启策略。
	assert.Contains(t, script, "busybox:1.37")
	assert.Contains(t, script, "-v /var/run/docker.sock:/var/run/docker.sock")
	assert.Contains(t, script, "--restart unless-stopped")

	// 双通道下载 + 二进制入口端点与个体凭证头。
	assert.Contains(t, script, "curl -sfL -H 'Authorization: Bearer fltrelay_testcredential' -o $t http://10.0.0.3:9081/v1/platform/binary")
	assert.Contains(t, script, "wget -q --header 'Authorization: Bearer fltrelay_testcredential' -O $t http://10.0.0.3:9081/v1/platform/binary")

	// relay 旗标面：manager 基址与个体凭证注入容器 argv（H10：不是
	// --join-token，脚本不含集群 join token）。
	assert.Contains(t, script, "exec /tmp/fleetlyd relay --manager http://10.0.0.3:9081 --relay-credential fltrelay_testcredential")
	assert.NotContains(t, script, "--join-token")
	assert.NotContains(t, script, "SWMTKN")

	// 二进制经 docker cp 注入（chmod +x 后 exec——busybox sh 面承载）。
	assert.Contains(t, script, "docker cp $t $c:/tmp/fleetlyd")
	assert.Contains(t, script, "chmod +x /tmp/fleetlyd")

	// 确定性：同输入同输出（golden 夹具友好）。凭证形态受控（fltrelay_
	// + base64url，无引号字符）——注入面在生成端闭合。
	require.Equal(t, script, nodeRelayScript("http://10.0.0.3:9081", "fltrelay_testcredential"))
}

// TestNodeRelayCommand：RuntimeRelayMaterial 的 swarm 实现（H10 §4.7）
// ——manager 广播地址 + gateway 端口拼 base，脚本携个体凭证、不含
// SWMTKN join token；缺 gateway 端口 / 无 manager 地址诚实失败。
func TestNodeRelayCommand(t *testing.T) {
	p := &Provider{}
	p.nodeList = func(context.Context) (client.NodeListResult, error) {
		return client.NodeListResult{Items: []swarm.Node{
			{ID: "worker-1"},
			{ID: "manager-1", ManagerStatus: &swarm.ManagerStatus{Addr: "10.0.0.3:2377", Leader: true}},
		}}, nil
	}
	script, err := p.NodeRelayCommand(context.Background(), "fltrelay_individual",
		capability.EnrollmentOptions{GatewayPort: "9081"})
	require.NoError(t, err)
	assert.Contains(t, script, "http://10.0.0.3:9081/v1/platform/binary",
		"the script must download from the manager's advertise host and the gateway port")
	assert.Contains(t, script, "--relay-credential fltrelay_individual")
	assert.NotContains(t, script, "--join-token")
	assert.NotContains(t, script, "SWMTKN")

	_, err = p.NodeRelayCommand(context.Background(), "fltrelay_individual", capability.EnrollmentOptions{})
	assert.Error(t, err, "relay material needs the gateway port")

	noManager := &Provider{}
	noManager.nodeList = func(context.Context) (client.NodeListResult, error) {
		return client.NodeListResult{Items: []swarm.Node{{ID: "worker-1"}}}, nil
	}
	_, err = noManager.NodeRelayCommand(context.Background(), "fltrelay_individual",
		capability.EnrollmentOptions{GatewayPort: "9081"})
	assert.Error(t, err, "no reachable manager is an honest failure")
}
