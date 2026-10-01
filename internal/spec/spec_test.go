package spec

import (
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
