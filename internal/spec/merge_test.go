package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// MergeHookOwned 所有权字段表（ADR-0072 决策 1/2）——表驱动钉死两半边
// 的逐字段归属：source/build 取 material，其余全部取 runtime。
func TestMergeHookOwnedOwnershipTable(t *testing.T) {
	material := &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: "hook-app", Project: "p1"},
		Source:        &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{Repo: "https://git.example/x", Ref: "sha-new"}}},
		Build:         &specv1.BuildSpec{Builder: "dockerfile", Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: "Dockerfile.hook"}},
		Processes:     []*specv1.ProcessSpec{{Name: "web", ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "default"}, Replicas: 1}},
	}
	runtime := &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: "hook-app", Project: "p1"},
		Source:        &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{Repo: "https://git.example/x", Ref: "sha-OLD"}}},
		Build:         &specv1.BuildSpec{Builder: "dockerfile", Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: "Dockerfile.OLD"}},
		Processes: []*specv1.ProcessSpec{
			{Name: "web", ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "default"}, Replicas: 4, Env: map[string]string{"A": "1"}, Ports: []*specv1.PortSpec{{Port: 8080}}},
			{Name: "worker", ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"}, Command: []string{"sleep", "3600"}}, // 额外进程（image→git 迁移保留面）
		},
		FirstBootJobs: []*specv1.JobSpec{{Name: "migrate", Process: &specv1.ProcessSpec{Name: "migrate", ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "default"}}}},
	}

	merged := MergeHookOwned(material, runtime)

	// 材料半边取 material（push 场景 = hook 新材料；ref 更新防旧 commit 构建）。
	assert.Equal(t, "sha-new", merged.GetSource().GetGit().GetRef())
	assert.Equal(t, "Dockerfile.hook", merged.GetBuild().GetDockerfile())
	// 运行时半边取 runtime：console 声明的形状/额外进程/迁移 job 跨 push 存活。
	require.Len(t, merged.GetProcesses(), 2)
	assert.Equal(t, int64(4), merged.GetProcesses()[0].GetReplicas())
	assert.Equal(t, "1", merged.GetProcesses()[0].GetEnv()["A"])
	assert.Equal(t, "busybox:1.37", merged.GetProcesses()[1].GetImage())
	require.Len(t, merged.GetFirstBootJobs(), 1)
	// material 的合成进程（单 web from_build）不进结果——运行时半边才是形状真源。
	assert.NotContains(t, merged.GetProcesses(), material.GetProcesses()[0])
}

// overlay 场景（决策 3）：material = 冻结 Spec、runtime = 提交件——提交件
// 的 source/build 被合并天然丢弃（防旧 ref 构建旧 commit）。
func TestMergeHookOwnedOverlayDropsSubmittedSource(t *testing.T) {
	frozen := &specv1.AppSpec{
		Source: &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{Repo: "https://git.example/x", Ref: "sha-now"}}},
		Build:  &specv1.BuildSpec{Builder: "dockerfile", Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: "Dockerfile"}},
	}
	submitted := &specv1.AppSpec{
		// 提交件携带过期 source（Redeploy 回读旧冻结体再编辑的形态）。
		Source:    &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{Repo: "https://git.example/x", Ref: "sha-STALE"}}},
		Build:     &specv1.BuildSpec{Builder: "dockerfile", Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: "STALE"}},
		Processes: []*specv1.ProcessSpec{{Name: "web", ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "default"}, Replicas: 7}},
	}

	merged := MergeHookOwned(frozen, submitted)

	assert.Equal(t, "sha-now", merged.GetSource().GetGit().GetRef(), "overlay must keep the frozen material")
	assert.Equal(t, "Dockerfile", merged.GetBuild().GetDockerfile())
	assert.Equal(t, int64(7), merged.GetProcesses()[0].GetReplicas(), "overlay must take the submitted runtime half")
}

// 入参不可变（webhook 路径与受理路径共用同一冻结体对象）。
func TestMergeHookOwnedDoesNotMutateInputs(t *testing.T) {
	material := &specv1.AppSpec{Source: &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: "nginx:1.27"}}}}
	runtime := &specv1.AppSpec{Processes: []*specv1.ProcessSpec{{Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.28"}}}}

	merged := MergeHookOwned(material, runtime)

	assert.Equal(t, "nginx:1.28", runtime.GetProcesses()[0].GetImage())
	assert.Nil(t, runtime.GetSource(), "runtime input must not gain a material half")
	assert.Nil(t, material.GetProcesses(), "material input must not gain processes")
	assert.Equal(t, "nginx:1.27", merged.GetSource().GetImage().GetRef())
}
