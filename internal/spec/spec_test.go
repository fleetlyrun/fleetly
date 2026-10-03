package spec

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func validAppSpec() *specv1.AppSpec {
	return &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: "app_01H", Project: "prj_01H"},
		Source: &specv1.Source{Kind: &specv1.Source_Image{
			Image: &specv1.ImageSource{Ref: "nginx:1.27"},
		}},
		Processes: []*specv1.ProcessSpec{{
			Name:        "web",
			ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"},
			Ports:       []*specv1.PortSpec{{Port: 8080, Protocol: specv1.Protocol_PROTOCOL_HTTP}},
		}},
	}
}

func TestValidateAppAccepts(t *testing.T) {
	assert.NoError(t, ValidateApp(validAppSpec()))
}

// 跨 Project 引用形态（ADR-0013 附录 A.2）：ULID 项目段 + 非空名段；
// 名字段/坏形态拒绝（叶子包只管形态——存在性与批准态在受理/投影面）。
func TestValidateAppCrossProjectNetworkRef(t *testing.T) {
	s := validAppSpec()
	s.Processes[0].Networks = []string{"project:01JD0PROJ00000000000000000/messaging"}
	assert.NoError(t, ValidateApp(s))

	s = validAppSpec()
	s.Processes[0].Networks = []string{"project:shop/messaging"}
	assert.ErrorContains(t, ValidateApp(s), "platform project id")

	s = validAppSpec()
	s.Processes[0].Networks = []string{"project:01JD0PROJ00000000000000000"}
	assert.ErrorContains(t, ValidateApp(s), "project:<project-id>/<network-name>")

	s = validAppSpec()
	s.Processes[0].Networks = []string{"project:01JD0PROJ00000000000000000/"}
	assert.ErrorContains(t, ValidateApp(s), "network name part must not be empty")

	// taskGroup 形态与跨 Project 形态互不干扰（各自前缀词面）。
	s = validAppSpec()
	s.Processes[0].Networks = []string{"taskGroup:dispatch"}
	assert.NoError(t, ValidateApp(s))
}

func TestValidateAppRejects(t *testing.T) {
	s := validAppSpec()
	s.SchemaVersion = 99
	assert.ErrorContains(t, ValidateApp(s), "schema_version")

	s = validAppSpec()
	s.App = nil
	assert.ErrorContains(t, ValidateApp(s), "app.id")

	s = validAppSpec()
	s.Processes = nil
	assert.ErrorContains(t, ValidateApp(s), "at least one process")

	s = validAppSpec()
	s.Processes = append(s.Processes, s.Processes[0])
	assert.ErrorContains(t, ValidateApp(s), "duplicate process name")

	s = validAppSpec()
	s.Processes[0].Ports[0].Port = 70000
	assert.ErrorContains(t, ValidateApp(s), "out of range")

	s = validAppSpec()
	s.Processes[0].ImageOrigin = nil
	assert.ErrorContains(t, ValidateApp(s), "image or from_build")

	s = validAppSpec()
	// oneof 使双探针不可构造；空探针（设了 Healthcheck 但无 kind）拒绝。
	s.Processes[0].Healthcheck = &specv1.HealthcheckSpec{
		Interval: durationpb.New(5 * time.Second),
	}
	assert.ErrorContains(t, ValidateApp(s), "http_path, tcp_port or exec")
}

// validJob 合法 job 模板（ValidateJob/ValidateApp 挂钩测试的基准）。
func validJob(name string) *specv1.JobSpec {
	return &specv1.JobSpec{
		Name: name,
		Ttl:  durationpb.New(10 * time.Minute),
		Process: &specv1.ProcessSpec{
			ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"},
			Command:     []string{"/migrate"},
		},
	}
}

// ValidateJob（ADR-0030 决策 8）：ttl 必填有界；禁面 fail-closed 且理由
// 精确；process.name 空 = 铸造时落 job.name，非空须相等。
func TestValidateJob(t *testing.T) {
	assert.NoError(t, ValidateJob("app.first_boot_jobs[0]", validJob("migrate")))

	s := validAppSpec()
	s.FirstBootJobs = []*specv1.JobSpec{validJob("migrate")}
	assert.NoError(t, ValidateApp(s), "a valid job passes the app-level hook")

	s.FirstBootJobs = []*specv1.JobSpec{validJob("migrate"), validJob("migrate")}
	assert.ErrorContains(t, ValidateApp(s), "duplicate first boot job name")

	noTTL := validJob("migrate")
	noTTL.Ttl = nil
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", noTTL), "ttl")

	zeroTTL := validJob("migrate")
	zeroTTL.Ttl = durationpb.New(0)
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", zeroTTL), "ttl")

	overTTL := validJob("migrate")
	overTTL.Ttl = durationpb.New(25 * time.Hour)
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", overTTL), "ttl")

	unnamed := validJob("")
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", unnamed), "name")

	nilProcess := validJob("migrate")
	nilProcess.Process = nil
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", nilProcess), "process template")

	mismatch := validJob("migrate")
	mismatch.Process.Name = "other"
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", mismatch), "equal to the job name")

	match := validJob("migrate")
	match.Process.Name = "migrate"
	assert.NoError(t, ValidateJob("app.first_boot_jobs[0]", match))

	volumes := validJob("migrate")
	volumes.Process.Volumes = []*specv1.VolumeAttachment{{VolumeId: "data", Target: "/data"}}
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", volumes), "volumes")

	configs := validJob("migrate")
	configs.Process.ConfigRefs = []string{"app.conf"}
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", configs), "config_refs")

	ports := validJob("migrate")
	ports.Process.Ports = []*specv1.PortSpec{{Port: 8080, Protocol: specv1.Protocol_PROTOCOL_HTTP}}
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", ports), "ports")

	probe := validJob("migrate")
	probe.Process.Healthcheck = &specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_TcpPort{TcpPort: 8080},
	}
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", probe), "healthcheck")

	place := validJob("migrate")
	place.Process.Placement = &specv1.PlacementSpec{NodeIds: []string{"01JD0NODE00000000000000000"}}
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", place), "placement")

	multi := validJob("migrate")
	multi.Process.Replicas = 2
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", multi), "single one-shot run")

	// 网络三形态与 App Process 同域（形态校验；存在性/批准态在受理与投影面）。
	nets := validJob("migrate")
	nets.Process.Networks = []string{"default", "taskGroup:dispatcher", "project:01JD0PROJ00000000000000000/main"}
	assert.NoError(t, ValidateJob("app.first_boot_jobs[0]", nets))

	badNet := validJob("migrate")
	badNet.Process.Networks = []string{"project:shop/main"}
	assert.ErrorContains(t, ValidateJob("app.first_boot_jobs[0]", badNet), "platform project id")

	fromBuild := validJob("migrate")
	fromBuild.Process.ImageOrigin = &specv1.ProcessSpec_FromBuild{FromBuild: "web"}
	assert.NoError(t, ValidateJob("app.first_boot_jobs[0]", fromBuild))
}

func TestValidateTaskTTLBound(t *testing.T) {
	base := &specv1.TaskSpec{
		SchemaVersion: SchemaVersion,
		Task:          &specv1.TaskRef{Id: "tsk_01H", Project: "prj_01H"},
		Process:       &specv1.ProcessSpec{Name: "runner", ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"}},
	}
	assert.NoError(t, ValidateTask(base))

	over := proto.Clone(base).(*specv1.TaskSpec)
	over.TtlSeconds = 86401
	assert.ErrorContains(t, ValidateTask(over), "86400")
}

// Task 双形态与网络组的校验面（F1.5，ADR-0012/0025）。
func TestValidateTaskFormAndNetworkGroup(t *testing.T) {
	base := func() *specv1.TaskSpec {
		return &specv1.TaskSpec{
			SchemaVersion: SchemaVersion,
			Task:          &specv1.TaskRef{Id: "tsk_01H", Project: "prj_01H"},
			Process:       &specv1.ProcessSpec{Name: "runner", ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.37"}},
		}
	}
	// 空声明推导：desired>1 → resident；否则 one-shot。
	assert.Equal(t, FormOneShot, TaskForm(base()))
	r := base()
	r.DesiredConcurrency = 4
	assert.Equal(t, FormResident, TaskForm(r))

	oneShot := base()
	oneShot.Form = FormOneShot
	oneShot.DesiredConcurrency = 2
	assert.ErrorContains(t, ValidateTask(oneShot), "one-shot")

	badForm := base()
	badForm.Form = "cron"
	assert.ErrorContains(t, ValidateTask(badForm), `must be one of "one-shot" or "resident"`)

	withGroup := base()
	withGroup.Form = FormResident
	withGroup.NetworkGroup = "dispatcher"
	assert.NoError(t, ValidateTask(withGroup))

	for _, bad := range []string{"Dispatcher", "-lead", "trail-", "a b", ""} {
		g := base()
		g.NetworkGroup = bad
		if bad == "" {
			continue // 空组 = 不挂网络组，合法
		}
		assert.ErrorContains(t, ValidateTask(g), "network_group", "group %q", bad)
	}

	// Task 的网络附件只有 network_group 一条轨。
	crossNet := base()
	crossNet.Process.Networks = []string{"default"}
	assert.ErrorContains(t, ValidateTask(crossNet), "network_group only")
}

func TestNetworkGroupRef(t *testing.T) {
	assert.True(t, IsNetworkGroupRef("taskGroup:dispatcher"))
	assert.Equal(t, "dispatcher", NetworkGroupName("taskGroup:dispatcher"))
	assert.False(t, IsNetworkGroupRef("project-net"))
}

// Secret 名字符集（N1 收尾批 A3）：防 /run/secrets/<名> 路径逃逸——白名单
// 字符集 + ".." 拒绝；冒号合法（平台数据库凭证名 database:<name> 既定
// 形态，ADR-0029）。
func TestValidSecretName(t *testing.T) {
	for _, ok := range []string{"deploy-key", "db.password", "database:pg", "A1_b-c", "x", strings.Repeat("a", 64)} {
		assert.True(t, ValidSecretName(ok), "name %q must be valid", ok)
	}
	for _, bad := range []string{"", "../etc/passwd", "a/b", `a\b`, " lead", "trail ", "..", "a..b", ".hidden", "-lead", "with space", "with\ttab", strings.Repeat("a", 65)} {
		assert.False(t, ValidSecretName(bad), "name %q must be rejected", bad)
	}
}

// SecretRefs 入口校验（task/compose 共用面）：逃逸形态拒并带精确字段名。
func TestValidateProcessSecretRefs(t *testing.T) {
	base := func(refs []string) *specv1.ProcessSpec {
		return &specv1.ProcessSpec{Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, SecretRefs: refs}
	}
	assert.NoError(t, ValidateProcess("p", base([]string{"deploy-key", "database:pg"})))
	assert.ErrorContains(t, ValidateProcess("p", base([]string{"../etc/passwd"})), "secret_refs[0]")
	assert.ErrorContains(t, ValidateProcess("p", base([]string{"ok", "a/b"})), "secret_refs[1]")
	assert.ErrorContains(t, ValidateProcess("p", base([]string{""})), "secret_refs[0]")
}

func TestValidateDatabase(t *testing.T) {
	base := &specv1.DatabaseSpec{
		SchemaVersion:  SchemaVersion,
		Database:       &specv1.DatabaseRef{Id: "db_01H", Project: "prj_01H"},
		Engine:         "postgres",
		CredentialsRef: "db-creds",
	}
	require.NoError(t, ValidateDatabase(base))
	base.CredentialsRef = ""
	assert.ErrorContains(t, ValidateDatabase(base), "credentials_ref")
}
