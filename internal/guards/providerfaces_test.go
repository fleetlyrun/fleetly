package guards

// Provider 可选子面的可发现性守卫（R06-L1）：
//
//   - 双 Provider 的编译期"全契约"断言清单补齐 RuntimeExec 与
//     RuntimeRestart——接口签名漂移/实现丢失即编译红（此前清单缺项，
//     双 Provider 会静默退化为"不支持重启/执行"）；
//   - FacesOf 必须把两子面探测出来（ProviderFaces.Restart 的单点收拢：
//     装配期 Offered/doctor 可见，engine 侧不再各自 duck-type）。
//
// 断言放 guards 而非 providers 本体：providers 的实现文件不在 S17-c1 的
// 改动面内，守卫层用同一编译面钉死契约（providers 包已是元层可 import
// 的对象——guards allowAll）。

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/providers/k3s"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/providers/swarm"
)

// 编译期契约锚：两 Provider 必须实现 Exec 与 Restart 子面。
var (
	_ capability.RuntimeExec    = (*swarm.Provider)(nil)
	_ capability.RuntimeRestart = (*swarm.Provider)(nil)
	_ capability.RuntimeExec    = (*k3s.Provider)(nil)
	_ capability.RuntimeRestart = (*k3s.Provider)(nil)
)

// TestProviderRestartFaceDiscoverable：FacesOf 必须探测到 Exec/Restart
// 子面并出现在 Offered 枚举里（R06-L1 的单点可发现承诺）。
func TestProviderRestartFaceDiscoverable(t *testing.T) {
	for name, p := range map[string]capability.Provider{
		"swarm": &swarm.Provider{},
		"k3s":   &k3s.Provider{},
	} {
		f := capability.FacesOf(p)
		assert.NotNil(t, f.Exec, "%s must expose RuntimeExec via FacesOf (R06-L1)", name)
		assert.NotNil(t, f.Restart, "%s must expose RuntimeRestart via FacesOf (R06-L1)", name)
		assert.Contains(t, f.Offered(), "exec", "%s Offered() must enumerate the exec face", name)
		assert.Contains(t, f.Offered(), "restart", "%s Offered() must enumerate the restart face", name)
	}
}
