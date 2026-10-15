// 归一化（F0.8）：镜像直投与上传产物归一成 AppSpec（架构 §4：归一化结果
// 即 Revision 冻结体，部署基线永远以 Revision 为准，不回读源文件）。
// compose 受理语言的官方解析住 internal/composeintake（ADR-0074）；裸
// AppSpec（spec_file）受理见 ParseSpecFile。
package spec

import (
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// ImageDeploy 归一化镜像直投：单 process web（名可指定，默认 web）。
// probe 是可选探针声明（B2：http path 或 tcp port——二选一，nil = 无探针）。
// env 是 App 级变量直传（ADR-0043 决策 2：键按 EnvNamePattern 受理面即拒；
// Project 层 SharedVariable 的合成在冻结咽喉，此处只落 App 层）。
// ports 是可选端口声明（F3.5）：Route 后端解析的期望集供给面——声明即
// Route-facing（挂靠项目 default 网，Proxy 可达），见 portDeclNetworks。
func ImageDeploy(appID, projectID, image string, processName string, probe *specv1.HealthcheckSpec, env map[string]string, ports []*specv1.PortSpec) (*specv1.AppSpec, error) {
	if err := ValidateEnvKeys("image.env", env); err != nil {
		return nil, err
	}
	if processName == "" {
		processName = "web"
	}
	spec := &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: appID, Project: projectID},
		Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: image}}},
		Processes: []*specv1.ProcessSpec{{
			Name: processName,
			ImageOrigin: &specv1.ProcessSpec_Image{
				Image: image,
			},
			Replicas:    1,
			Healthcheck: probe,
			Env:         env,
			Ports:       ports,
			Networks:    portDeclNetworks(ports),
		}},
	}
	if err := ValidateApp(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// portDeclNetworks 是单进程形态（image 直投/upload）端口声明的挂网裁决
// （F3.5）：声明端口 = Route-facing 意图——进程挂靠项目 default 网（项目
// 出生必建、ensureNetworks create-or-get 兜底），受管 Proxy 挂全部项目网
// 即可达后端（static Route 404 的可达性半边）。未声明端口维持"无网络即
// 无 DNS 面"的既有诚实语义（ADR-0034 决策 2），行为零变化。
func portDeclNetworks(ports []*specv1.PortSpec) []string {
	if len(ports) == 0 {
		return nil
	}
	return []string{"default"}
}

// ParseSpecFile 解析裸 AppSpec 部署面（第四 intake 源，F3.5）：protojson
// 解码（未知字段拒绝——fail-closed，AppSpec 消息 schema 即可写面边界，
// ADR-0033"校验爆炸半径"担忧的解法：字段白名单不存在、全部字段合法，
// 约束由 ValidateApp 叶子单源执法）+ AppRef 权威覆写（归属锚来自
// DeployRequest.app_id 行，文件内声明不信任、恒覆写）+ schema_version
// 缺省当前版（0 = 未声明；>当前版由 ValidateApp 拒——读入 check-strategy
// 的"拒绝跳代"半边）。归一化产物与三形态同喉——受理咽喉
// engine.SubmitSpec 的共享变量合成与内容寻址复用天然覆盖。
func ParseSpecFile(body []byte, appID, projectID string) (*specv1.AppSpec, error) {
	s := &specv1.AppSpec{}
	if err := protojson.Unmarshal(body, s); err != nil {
		return nil, invalidf("spec_file", "invalid AppSpec JSON: %v", err)
	}
	if s.GetSchemaVersion() == 0 {
		s.SchemaVersion = SchemaVersion
	}
	s.App = &specv1.AppRef{Id: appID, Project: projectID}
	if err := ValidateApp(s); err != nil {
		return nil, err
	}
	return s, nil
}

// UploadDeployInput 是上传产物形态的部署输入（三 strategy 旗标经此组装
// BuildSpec，ADR-0032；Builder 空值按 Dockerfile 缺省轨处理）。
type UploadDeployInput struct {
	AppID    string
	Project  string
	UploadID string
	// ProcessName 是进程名（缺省 web）。
	ProcessName string
	// Builder 是构建器名（dockerfile|railpack|static；空 = dockerfile）。
	Builder string
	// Dockerfile 是 dockerfile 形态的构建文件路径（缺省 Dockerfile）。
	Dockerfile string
	// RailpackVersion 是 railpack 形态的钉版（必填，bare semver）。
	RailpackVersion string
	// OutputDir 是 static 形态的产物目录（缺省 "."）。
	OutputDir string
	// Probe 是可选探针声明（http path 或 tcp port）。
	Probe *specv1.HealthcheckSpec
	// Env 是 App 级变量直传（ADR-0043 决策 2；键按 EnvNamePattern 受理面
	// 即拒——与 image 直投同口径）。
	Env map[string]string
	// Ports 是可选端口声明（F3.5）：Route 后端解析的期望集供给面——声明
	// 即 Route-facing（挂靠项目 default 网，Proxy 可达），见 portDeclNetworks。
	Ports []*specv1.PortSpec
}

// UploadDeploy 归一化上传产物形态（F1.10，ADR-0019 附录 A；strategy 面
// ADR-0032）：Source.upload + BuildSpec（三 strategy）+ 单进程 from_build
// （与 git 源 webhook 路径同构；from_build 的 digest 解析按进程名，值本身
// 只要求非空）。
func UploadDeploy(in UploadDeployInput) (*specv1.AppSpec, error) {
	if err := ValidateEnvKeys("upload.env", in.Env); err != nil {
		return nil, err
	}
	builder := in.Builder
	if builder == "" {
		builder = BuilderDockerfile
	}
	var build *specv1.BuildSpec
	switch builder {
	case BuilderDockerfile:
		dockerfile := in.Dockerfile
		if dockerfile == "" {
			dockerfile = "Dockerfile"
		}
		build = &specv1.BuildSpec{Builder: builder, Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: dockerfile}}
	case BuilderRailpack:
		build = &specv1.BuildSpec{
			Builder:  builder,
			Strategy: &specv1.BuildSpec_Railpack{Railpack: &specv1.RailpackBuilder{PinnedVersion: in.RailpackVersion}},
		}
	case BuilderStatic:
		outputDir := in.OutputDir
		if outputDir == "" {
			outputDir = "."
		}
		build = &specv1.BuildSpec{
			Builder:  builder,
			Strategy: &specv1.BuildSpec_Static{Static: &specv1.StaticBuilder{OutputDir: outputDir}},
		}
	default:
		return nil, invalidf("build.builder",
			"unknown builder %q (supported: dockerfile, railpack, static)", builder)
	}
	processName := in.ProcessName
	if processName == "" {
		processName = "web"
	}
	s := &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: in.AppID, Project: in.Project},
		Source:        &specv1.Source{Kind: &specv1.Source_Upload{Upload: &specv1.UploadSource{Id: in.UploadID}}},
		Build:         build,
		Processes: []*specv1.ProcessSpec{{
			Name:        processName,
			ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: processName},
			Replicas:    1,
			Healthcheck: in.Probe,
			Env:         in.Env,
			Ports:       in.Ports,
			Networks:    portDeclNetworks(in.Ports),
		}},
	}
	if err := ValidateApp(s); err != nil {
		return nil, err
	}
	return s, nil
}
