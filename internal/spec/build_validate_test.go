package spec

// ValidateBuild 叶子校验测试（F1.14，ADR-0032 验收锚）：builder 名值域与
// strategy 配对、railpack pinned_version bare semver、static output_dir
// 路径逃逸 fail-closed、UploadDeploy 三 strategy 归一化产物。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func TestValidateBuild(t *testing.T) {
	dockerfile := func(builder, path string) *specv1.BuildSpec {
		return &specv1.BuildSpec{Builder: builder, Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: path}}
	}
	railpack := func(builder, version string) *specv1.BuildSpec {
		return &specv1.BuildSpec{Builder: builder, Strategy: &specv1.BuildSpec_Railpack{Railpack: &specv1.RailpackBuilder{PinnedVersion: version}}}
	}
	static := func(builder, dir string) *specv1.BuildSpec {
		return &specv1.BuildSpec{Builder: builder, Strategy: &specv1.BuildSpec_Static{Static: &specv1.StaticBuilder{OutputDir: dir}}}
	}

	t.Run("nil build is legal (image deploys)", func(t *testing.T) {
		assert.NoError(t, ValidateBuild("app.build", nil))
	})
	t.Run("strategy required", func(t *testing.T) {
		err := ValidateBuild("app.build", &specv1.BuildSpec{Builder: BuilderDockerfile})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a strategy is required")
	})
	t.Run("builder name must pair with strategy", func(t *testing.T) {
		for _, b := range []*specv1.BuildSpec{
			railpack(BuilderDockerfile, "0.39.0"),
			railpack(BuilderStatic, "0.39.0"),
			static(BuilderDockerfile, "dist"),
			static(BuilderRailpack, "dist"),
			dockerfile(BuilderRailpack, "Dockerfile"),
			dockerfile(BuilderStatic, "Dockerfile"),
			dockerfile("makeimg", "Dockerfile"), // 词条外名
			dockerfile("", "Dockerfile"),        // 空名
		} {
			assert.Error(t, ValidateBuild("app.build", b), "builder=%s", b.GetBuilder())
		}
	})
	t.Run("valid forms", func(t *testing.T) {
		for _, b := range []*specv1.BuildSpec{
			dockerfile(BuilderDockerfile, "Dockerfile"),
			dockerfile(BuilderDockerfile, ""), // 空 = 平台缺省
			dockerfile(BuilderDockerfile, "deploy/Dockerfile"),
			railpack(BuilderRailpack, "0.39.0"),
			static(BuilderStatic, "."),
			static(BuilderStatic, "dist"),
			static(BuilderStatic, "client/build"),
		} {
			assert.NoError(t, ValidateBuild("app.build", b), "builder=%s strategy=%T", b.GetBuilder(), b.GetStrategy())
		}
	})
	t.Run("pinned version is bare semver", func(t *testing.T) {
		for _, bad := range []string{"", "latest", "v0.39.0", "0.39", "0.39.0.1", ">=0.39"} {
			err := ValidateBuild("app.build", railpack(BuilderRailpack, bad))
			require.Error(t, err, "version %q", bad)
			assert.Contains(t, err.Error(), "bare semver", "version %q", bad)
		}
	})
	t.Run("static output_dir stays inside the context", func(t *testing.T) {
		for _, bad := range []string{"", "..", "../x", "a/../../x", "/abs", "a\\b", "C:\\x"} {
			err := ValidateBuild("app.build", static(BuilderStatic, bad))
			require.Error(t, err, "dir %q", bad)
		}
	})
	t.Run("dockerfile path escapes reject", func(t *testing.T) {
		for _, bad := range []string{"../Dockerfile", "/etc/passwd", "a\\Dockerfile"} {
			assert.Error(t, ValidateBuild("app.build", dockerfile(BuilderDockerfile, bad)), "path %q", bad)
		}
	})
	t.Run("cache_from entries must be non-empty", func(t *testing.T) {
		b := dockerfile(BuilderDockerfile, "Dockerfile")
		b.CacheFrom = []string{"reg/app:r1", " "}
		err := ValidateBuild("app.build", b)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cache_from[1]")
	})
}

// uploadSpecBody 反序列化 UploadDeploy 产物（spec 是 protojson 冻结体面）。
func uploadSpecBody(t *testing.T, in UploadDeployInput) *specv1.AppSpec {
	t.Helper()
	s, err := UploadDeploy(in)
	require.NoError(t, err)
	return s
}

func TestUploadDeployStrategies(t *testing.T) {
	base := UploadDeployInput{AppID: "01JD0APP0000000000000000A1", Project: "01JD0PROJ000000000000000001",
		UploadID: "01JD0UPL0000000000000000X1"}

	t.Run("default is the dockerfile track", func(t *testing.T) {
		s := uploadSpecBody(t, base)
		require.NotNil(t, s.GetBuild())
		assert.Equal(t, BuilderDockerfile, s.GetBuild().GetBuilder())
		assert.Equal(t, "Dockerfile", s.GetBuild().GetDockerfile())
	})
	t.Run("dockerfile path default and override", func(t *testing.T) {
		s := uploadSpecBody(t, base)
		assert.Equal(t, "Dockerfile", s.GetBuild().GetDockerfile())
		in := base
		in.Dockerfile = "deploy/Dockerfile"
		s = uploadSpecBody(t, in)
		assert.Equal(t, "deploy/Dockerfile", s.GetBuild().GetDockerfile())
	})
	t.Run("railpack carries the pin", func(t *testing.T) {
		in := base
		in.Builder = BuilderRailpack
		in.RailpackVersion = "0.39.0"
		s := uploadSpecBody(t, in)
		assert.Equal(t, BuilderRailpack, s.GetBuild().GetBuilder())
		require.NotNil(t, s.GetBuild().GetRailpack())
		assert.Equal(t, "0.39.0", s.GetBuild().GetRailpack().GetPinnedVersion())
		assert.Empty(t, s.GetBuild().GetDockerfile())
	})
	t.Run("railpack without a version is rejected at the leaf", func(t *testing.T) {
		in := base
		in.Builder = BuilderRailpack
		_, err := UploadDeploy(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pinned_version")
	})
	t.Run("static defaults output_dir to the context root", func(t *testing.T) {
		in := base
		in.Builder = BuilderStatic
		s := uploadSpecBody(t, in)
		assert.Equal(t, BuilderStatic, s.GetBuild().GetBuilder())
		require.NotNil(t, s.GetBuild().GetStatic())
		assert.Equal(t, ".", s.GetBuild().GetStatic().GetOutputDir())
		in.OutputDir = "dist"
		s = uploadSpecBody(t, in)
		assert.Equal(t, "dist", s.GetBuild().GetStatic().GetOutputDir())
	})
	t.Run("unknown builder is rejected", func(t *testing.T) {
		in := base
		in.Builder = "makeimg"
		_, err := UploadDeploy(in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown builder "makeimg"`)
	})
	t.Run("all strategies freeze a single from_build process", func(t *testing.T) {
		for _, builder := range []string{BuilderDockerfile, BuilderRailpack, BuilderStatic} {
			in := base
			in.Builder = builder
			switch builder {
			case BuilderRailpack:
				in.RailpackVersion = "0.39.0"
			}
			s, err := UploadDeploy(in)
			require.NoError(t, err)
			require.Len(t, s.GetProcesses(), 1)
			assert.Equal(t, "web", s.GetProcesses()[0].GetFromBuild())
			assert.Equal(t, "01JD0UPL0000000000000000X1", s.GetSource().GetUpload().GetId(), "builder %s", builder)
		}
	})
}
