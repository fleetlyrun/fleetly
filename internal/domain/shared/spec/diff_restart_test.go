package spec

// ADR-0083 步骤 6 的纯函数钉测：RestartSeqOnly 的判定面（seq-only 真/
// 混入他字段假/空 diff 假/缺席↔0 无差异）。与 providers 内容等价四用例
// 共享法源：restart_seq 是意图字段，收敛面（内容等价）执法、漂移面
// （compareSpecs）豁免——本函数是收敛面判定的唯一纯函数真源。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func tAppSpec(processes ...*specv1.ProcessSpec) *specv1.AppSpec {
	return &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: "app-1", Project: "proj-1"},
		Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: "nginx:1.27"}}},
		Processes:     processes,
	}
}

func tWebProc(seq uint32) *specv1.ProcessSpec {
	return &specv1.ProcessSpec{Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, Replicas: 1, RestartSeq: seq}
}

func TestRestartSeqOnly(t *testing.T) {
	// seq 0→2（缺席→显式）：唯一差异路径落在 restart_seq → 真。
	only, err := RestartSeqOnly(tAppSpec(tWebProc(0)), tAppSpec(tWebProc(2)))
	require.NoError(t, err)
	assert.True(t, only, "a pure restart_seq bump is seq-only")

	// seq 1→5：同样真（值无关，路径判定）。
	only, err = RestartSeqOnly(tAppSpec(tWebProc(1)), tAppSpec(tWebProc(5)))
	require.NoError(t, err)
	assert.True(t, only)

	// 混入镜像变更：假。
	mixed := tWebProc(1)
	mixed.ImageOrigin = &specv1.ProcessSpec_Image{Image: "nginx:1.29"}
	only, err = RestartSeqOnly(tAppSpec(tWebProc(1)), tAppSpec(mixed))
	require.NoError(t, err)
	assert.False(t, only, "an image change alongside the seq bump is not seq-only")

	// 空差异（同内容重放）：恒假——同 Revision 再部署重跑 preDeployJobs
	// 的既有纪律不被重启语义吞并。
	only, err = RestartSeqOnly(tAppSpec(tWebProc(3)), tAppSpec(tWebProc(3)))
	require.NoError(t, err)
	assert.False(t, only, "an empty diff is a same-content replay, not a restart")

	// 缺席↔0（EmitUnpopulated:false 冻结参数）：零差异条目 → 假（空 diff）。
	only, err = RestartSeqOnly(tAppSpec(tWebProc(0)), tAppSpec(tWebProc(0)))
	require.NoError(t, err)
	assert.False(t, only)

	// 多进程其一变更：假。
	only, err = RestartSeqOnly(
		tAppSpec(tWebProc(1), &specv1.ProcessSpec{Name: "worker", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, Replicas: 1}),
		tAppSpec(tWebProc(2), &specv1.ProcessSpec{Name: "worker", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.29"}, Replicas: 1}))
	require.NoError(t, err)
	assert.False(t, only, "any non-seq path in the diff disqualifies the deployment")

	// R16-M1 反例（后缀判定的欺骗面）：env 键名恰为 restart_seq——diff 路径
	// 是 processes[0].env.restart_seq，后缀判定会把它误判成纯 seq bump，让
	// 普通配置变更静默跳过 preDeployJobs。精确 proto 路径匹配必须拒绝。
	envA, envB := tWebProc(1), tWebProc(1)
	envA.Env = map[string]string{"restart_seq": "v1"}
	envB.Env = map[string]string{"restart_seq": "v2"}
	only, err = RestartSeqOnly(tAppSpec(envA), tAppSpec(envB))
	require.NoError(t, err)
	assert.False(t, only, "an env key literally named restart_seq is not the restart_seq field")

	// 同族反例：仅后缀形似的 env 键（my_restart_seq）同样不得命中。
	envC, envD := tWebProc(1), tWebProc(1)
	envC.Env = map[string]string{"my_restart_seq": "v1"}
	envD.Env = map[string]string{"my_restart_seq": "v2"}
	only, err = RestartSeqOnly(tAppSpec(envC), tAppSpec(envD))
	require.NoError(t, err)
	assert.False(t, only, "a suffix-like env key must not be accepted as the restart_seq field")
}
