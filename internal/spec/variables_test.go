package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// F2.9（ADR-0043）两级变量合成的性质测试：合成算法、确定性（验收锚 1/2/3）
// 与新增面的键校验。

func TestImageDeployEnvPassThrough(t *testing.T) {
	s, err := ImageDeploy("a", "p", "nginx:1.27", "", nil, map[string]string{"MODE": "prod"}, nil)
	require.NoError(t, err)
	require.Len(t, s.GetProcesses(), 1)
	assert.Equal(t, map[string]string{"MODE": "prod"}, s.GetProcesses()[0].GetEnv())

	_, err = ImageDeploy("a", "p", "nginx:1.27", "", nil, map[string]string{"BAD-KEY": "x"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image.env")
	assert.Contains(t, err.Error(), "BAD-KEY")
}

func TestUploadDeployEnvPassThrough(t *testing.T) {
	s, err := UploadDeploy(UploadDeployInput{
		AppID: "a", Project: "p", UploadID: "u1", Env: map[string]string{"MODE": "prod"},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"MODE": "prod"}, s.GetProcesses()[0].GetEnv())

	_, err = UploadDeploy(UploadDeployInput{
		AppID: "a", Project: "p", UploadID: "u1", Env: map[string]string{"1BAD": "x"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload.env")
}

func TestValidEnvName(t *testing.T) {
	for _, ok := range []string{"A", "_X", "a_b", "DATABASE_URL", "X9"} {
		assert.True(t, ValidEnvName(ok), "%q should be valid", ok)
	}
	for _, bad := range []string{"", "9A", "A-B", "A.B", "a b", "A/B", "带汉", "too-long-name-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		assert.False(t, ValidEnvName(bad), "%q should be invalid", bad)
	}
}

func appSpecForMerge() *specv1.AppSpec {
	return &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: "a", Project: "p"},
		Processes: []*specv1.ProcessSpec{
			{Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, Env: map[string]string{"MODE": "app", "ONLY_APP": "1"}},
			{Name: "worker", ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"}},
		},
		FirstBootJobs: []*specv1.JobSpec{{
			Name: "migrate",
			Process: &specv1.ProcessSpec{Name: "migrate", ImageOrigin: &specv1.ProcessSpec_Image{Image: "migrate:V4.18.1"},
				Env: map[string]string{"MIGRATE_OVERRIDE": "app"}},
		}},
	}
}

func TestMergeSharedEnvLayers(t *testing.T) {
	s := appSpecForMerge()
	MergeSharedEnv(s, map[string]string{
		"MODE": "shared", "SHARED_ONLY": "yes", "MIGRATE_OVERRIDE": "shared-lost",
	})
	// App 层覆盖同键；共享层新增键注入（全部共享键进每个进程）；无 env
	// 的进程拿到完整共享层。
	assert.Equal(t, map[string]string{"MODE": "app", "ONLY_APP": "1", "SHARED_ONLY": "yes", "MIGRATE_OVERRIDE": "shared-lost"}, s.GetProcesses()[0].GetEnv())
	assert.Equal(t, map[string]string{"MODE": "shared", "SHARED_ONLY": "yes", "MIGRATE_OVERRIDE": "shared-lost"}, s.GetProcesses()[1].GetEnv())
	// firstBootJobs 的进程同样参与合成（ADR-0043 决策 3）。
	assert.Equal(t, map[string]string{"MIGRATE_OVERRIDE": "app", "MODE": "shared", "SHARED_ONLY": "yes"},
		s.GetFirstBootJobs()[0].GetProcess().GetEnv())
}

func TestMergeSharedEnvEmptyIsNoop(t *testing.T) {
	s := appSpecForMerge()
	before := s.GetProcesses()[0].GetEnv()
	MergeSharedEnv(s, nil)
	assert.Equal(t, before, s.GetProcesses()[0].GetEnv())
}

// TestMergeSharedEnvByteDeterminism 是 ADR-0043 验收锚 1：同 Source 同
// 变量状态两次归一化（含合成）逐字节相等——protojson 规范序列化 map 按
// 键排序，内容寻址复用直接命中。
func TestMergeSharedEnvByteDeterminism(t *testing.T) {
	shared := map[string]string{"Z_VAR": "z", "A_VAR": "a", "M_VAR": "m"}
	marshal := func() []byte {
		s := appSpecForMerge()
		MergeSharedEnv(s, shared)
		body, err := protojson.Marshal(s)
		require.NoError(t, err)
		return body
	}
	first, second := marshal(), marshal()
	require.Equal(t, first, second)
	// 与不同变量状态可区分（合成确实进冻结体）。
	other := map[string]string{"Z_VAR": "z", "A_VAR": "a", "M_VAR": "changed"}
	s := appSpecForMerge()
	MergeSharedEnv(s, other)
	otherBody, err := protojson.Marshal(s)
	require.NoError(t, err)
	require.NotEqual(t, first, otherBody)
}
