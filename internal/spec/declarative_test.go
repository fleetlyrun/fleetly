package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// B2 回归（N0 修复批）：compose 声明面补齐——卷挂载、secret 引用、
// http/tcp 探针扩展键、探针节律、顶层 volumes 空声明。
func TestComposeDeclarativeSurface(t *testing.T) {
	doc := ComposeDoc{
		"volumes": map[string]any{"data": nil},
		"services": map[string]any{
			"web": map[string]any{
				"image":   "nginx:1.27",
				"volumes": []any{"data:/var/www"},
				"secrets": []any{"api-token", map[string]any{"source": "db-password"}},
				"healthcheck": map[string]any{
					"http_path": "/healthz",
					"interval":  "10s",
					"timeout":   "3s",
					"retries":   5,
				},
			},
			"db": map[string]any{
				"image":       "postgres:16",
				"healthcheck": map[string]any{"tcp_port": 5432, "start_period": "20s"},
			},
		},
	}
	spec, err := NormalizeCompose(doc, "app-1", "prj-1")
	require.NoError(t, err)

	var web, db *specv1.ProcessSpec
	for _, p := range spec.GetProcesses() {
		switch p.GetName() {
		case "web":
			web = p
		case "db":
			db = p
		}
	}
	require.NotNil(t, web)
	require.NotNil(t, db)

	// 卷挂载 → VolumeAttachment（无 mode → 可写缺省）。
	require.Len(t, web.GetVolumes(), 1)
	assert.Equal(t, "data", web.GetVolumes()[0].GetVolumeId())
	assert.Equal(t, "/var/www", web.GetVolumes()[0].GetTarget())
	assert.False(t, web.GetVolumes()[0].GetReadOnly())

	// secret 引用 → secret_refs（短语法与 {source:} 同型）。
	assert.Equal(t, []string{"api-token", "db-password"}, web.GetSecretRefs())

	// http 探针（fleetly 扩展键）+ 节律。
	hc := web.GetHealthcheck()
	require.NotNil(t, hc)
	assert.Equal(t, "/healthz", hc.GetHttpPath())
	assert.Equal(t, "10s", hc.GetInterval().AsDuration().String())
	assert.Equal(t, "3s", hc.GetTimeout().AsDuration().String())
	assert.Equal(t, int32(5), hc.GetRetries())

	// tcp 探针 + start_period。
	dbhc := db.GetHealthcheck()
	require.NotNil(t, dbhc)
	assert.Equal(t, int32(5432), dbhc.GetTcpPort())
	assert.Equal(t, "20s", dbhc.GetStartPeriod().AsDuration().String())
}

// firstBootJobs 扩展键翻译（ADR-0033）：job 白名单最小面 → JobSpec
// （ProcessSpec 嵌套形态）；执行语义是 ADR-0030 引擎链，本面只喂食。
func TestComposeFirstBootJobs(t *testing.T) {
	doc := ComposeDoc{
		"services": map[string]any{
			"web": map[string]any{"image": "nginx:1.27"},
		},
		"x-fleetly-first-boot-jobs": []any{
			map[string]any{
				"name":    "migrate",
				"image":   "migrate/migrate:v4.18.1",
				"command": []any{"sh", "-c", "migrate -database \"$(cat /run/secrets/database:pg)\" up"},
				"secrets": []any{"database:pg"},
				"ttl":     "300s",
			},
			map[string]any{
				"name":        "roles-sig",
				"image":       "ghcr.io/example/app:sha-1",
				"command":     "app admin sync-roles-sig",
				"environment": []any{"MODE=deploy", "BATCH=16"},
				"ttl":         "1m",
			},
		},
	}
	spec, err := NormalizeCompose(doc, "app-1", "prj-1")
	require.NoError(t, err)
	require.Len(t, spec.GetFirstBootJobs(), 2)

	migrate := spec.GetFirstBootJobs()[0]
	assert.Equal(t, "migrate", migrate.GetName())
	assert.Equal(t, "5m0s", migrate.GetTtl().AsDuration().String())
	p := migrate.GetProcess()
	require.NotNil(t, p)
	assert.Equal(t, "migrate/migrate:v4.18.1", p.GetImage())
	// command list 形态 → 字面 argv（引号/空格保真）。
	assert.Equal(t, []string{"sh", "-c", "migrate -database \"$(cat /run/secrets/database:pg)\" up"}, p.GetCommand())
	assert.Equal(t, []string{"database:pg"}, p.GetSecretRefs())
	assert.Equal(t, "migrate", p.GetName(), "process name must default to the job name")

	sig := spec.GetFirstBootJobs()[1]
	p2 := sig.GetProcess()
	// command string 形态 → Fields 切分（与服务面同语义）；environment list
	// 形态 → map。
	assert.Equal(t, []string{"app", "admin", "sync-roles-sig"}, p2.GetCommand())
	assert.Equal(t, map[string]string{"MODE": "deploy", "BATCH": "16"}, p2.GetEnv())
	assert.Empty(t, p2.GetSecretRefs())
}

// 服务面伴随扩宽（ADR-0033）：command list 形态 → 字面 argv；environment
// list 形态 → map（此前 map 断言失败被静默丢弃）；string/map 形态语义不变。
func TestComposeCommandEnvListForms(t *testing.T) {
	doc := ComposeDoc{
		"services": map[string]any{
			"web": map[string]any{
				"image":       "nginx:1.27",
				"command":     []any{"sh", "-c", "exec nginx -g 'daemon off;'"},
				"environment": []any{"MODE=prod", "B=2"},
			},
			"worker": map[string]any{
				"image":       "busybox:1.37",
				"command":     "sh -c while true",
				"environment": map[string]any{"K": "v"},
			},
		},
	}
	spec, err := NormalizeCompose(doc, "a", "p")
	require.NoError(t, err)
	var web, worker *specv1.ProcessSpec
	for _, p := range spec.GetProcesses() {
		switch p.GetName() {
		case "web":
			web = p
		case "worker":
			worker = p
		}
	}
	require.NotNil(t, web)
	require.NotNil(t, worker)
	assert.Equal(t, []string{"sh", "-c", "exec nginx -g 'daemon off;'"}, web.GetCommand())
	assert.Equal(t, map[string]string{"MODE": "prod", "B": "2"}, web.GetEnv())
	assert.Equal(t, []string{"sh", "-c", "while", "true"}, worker.GetCommand())
	assert.Equal(t, map[string]string{"K": "v"}, worker.GetEnv())
}

// firstBootJobs 拒绝面：禁面键键名层即拒（ADR-0030 决策 8 同口径）；
// 未知键/缺必填/非法形态精确拒绝。
func TestComposeFirstBootJobRejections(t *testing.T) {
	base := func(job map[string]any) ComposeDoc {
		return ComposeDoc{
			"services":                  map[string]any{"web": map[string]any{"image": "nginx"}},
			"x-fleetly-first-boot-jobs": []any{job},
		}
	}
	cases := []struct {
		name string
		doc  ComposeDoc
		want string
	}{
		{"unknown key", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "bad": 1}), "unsupported field \"bad\" for a first boot job"},
		{"no name", base(map[string]any{"image": "i", "ttl": "1m"}), "name"},
		{"no image", base(map[string]any{"name": "j", "ttl": "1m"}), "image"},
		{"no ttl", base(map[string]any{"name": "j", "image": "i"}), "hard timeout"},
		{"volumes", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "volumes": []any{"d:/d"}}), "cannot mount volumes"},
		{"configs", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "configs": []any{"c"}}), "cannot mount configs"},
		{"ports", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "ports": []any{"8080"}}), "not meaningful"},
		{"healthcheck", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "healthcheck": map[string]any{"tcp_port": 80}}), "not meaningful"},
		{"placement", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "placement": map[string]any{}}), "not supported"},
		{"replicas", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "replicas": 2}), "single one-shot run"},
		{"networks", base(map[string]any{"name": "j", "image": "i", "ttl": "1m", "networks": []any{"default"}}), "every active project network"},
		{"ttl over cap", base(map[string]any{"name": "j", "image": "i", "ttl": "86401s"}), "86400s"},
		{"ttl not duration", base(map[string]any{"name": "j", "image": "i", "ttl": 300}), "duration must be a string"},
		{"job not mapping", ComposeDoc{
			"services":                  map[string]any{"web": map[string]any{"image": "nginx"}},
			"x-fleetly-first-boot-jobs": []any{"migrate"},
		}, "job must be a mapping"},
		{"key not list", ComposeDoc{
			"services":                  map[string]any{"web": map[string]any{"image": "nginx"}},
			"x-fleetly-first-boot-jobs": map[string]any{"name": "j"},
		}, "must be a list of job mappings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeCompose(tc.doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// command/environment/secrets 非法形态精确拒绝（伴随扩宽的负面）。
func TestComposeCommandEnvSecretRejections(t *testing.T) {
	cases := []struct {
		name string
		svc  map[string]any
		want string
	}{
		{"command number", map[string]any{"image": "i", "command": 42}, "command must be a string or a list"},
		{"command list int entry", map[string]any{"image": "i", "command": []any{"sh", 1}}, "list-form command entries"},
		{"env list no eq", map[string]any{"image": "i", "environment": []any{"MODE"}}, "must be \"KEY=value\""},
		{"env list empty key", map[string]any{"image": "i", "environment": []any{"=v"}}, "non-empty key"},
		{"env number", map[string]any{"image": "i", "environment": 42}, "environment must be a mapping"},
		{"secrets map form", map[string]any{"image": "i", "secrets": map[string]any{"s": map[string]any{"file": "x"}}}, "secrets must be a list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := ComposeDoc{"services": map[string]any{"web": tc.svc}}
			_, err := NormalizeCompose(doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// 探针三选一 + 受管/未知键精确拒绝。
func TestComposeProbeRejections(t *testing.T) {
	cases := []struct {
		name string
		hc   map[string]any
		want string
	}{
		{"both probes", map[string]any{"http_path": "/h", "tcp_port": 80}, "one of test, http_path or tcp_port"},
		{"probe and test", map[string]any{"http_path": "/h", "test": []any{"CMD", "true"}}, "one of test, http_path or tcp_port"},
		{"relative http path", map[string]any{"http_path": "healthz"}, "starting with '/'"},
		{"disable managed", map[string]any{"tcp_port": 80, "disable": true}, "managed by the platform"},
		{"unknown key", map[string]any{"tcp_port": 80, "bad_key": 1}, "unknown healthcheck field"},
		{"no probe", map[string]any{"interval": "5s"}, "requires one of test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := ComposeDoc{"services": map[string]any{"web": map[string]any{
				"image": "nginx", "healthcheck": tc.hc,
			}}}
			_, err := NormalizeCompose(doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// 卷/secret 声明的非法形态精确拒绝。
func TestComposeVolumeSecretRejections(t *testing.T) {
	cases := []struct {
		name string
		doc  ComposeDoc
		want string
	}{
		{"relative target", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "volumes": []any{"data:var/www"},
		}}}, "absolute target"},
		{"unknown mode", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "volumes": []any{"data:/var/www:z"},
		}}}, "unknown mode \"z\" (expected ro or rw)"},
		{"long syntax", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "volumes": []any{map[string]any{"source": "data"}},
		}}}, "short syntax"},
		{"secret target remap", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "secrets": []any{map[string]any{"source": "s", "target": "renamed"}},
		}}}, "target remapping"},
		{"secret uid subkey", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "secrets": []any{map[string]any{"source": "s", "uid": "1000"}},
		}}}, "uid/gid/mode are not translated"},
		{"secret mode subkey", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "secrets": []any{map[string]any{"source": "s", "mode": 0o400}},
		}}}, "uid/gid/mode are not translated"},
		{"top-level volumes list", ComposeDoc{
			"volumes":  []any{"data"},
			"services": map[string]any{"web": map[string]any{"image": "nginx"}},
		}, "must be a mapping of volume names"},
		{"top-level driver opts", ComposeDoc{
			"volumes":  map[string]any{"data": map[string]any{"driver": "local"}},
			"services": map[string]any{"web": map[string]any{"image": "nginx"}},
		}, "driver options"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeCompose(tc.doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// 卷短语法 mode 解析（N0.1 P2-3：:ro 此前被吞进 target）。
func TestComposeVolumeModeParsing(t *testing.T) {
	for _, tc := range []struct {
		entry    string
		target   string
		readOnly bool
	}{
		{"data:/var/www", "/var/www", false},
		{"data:/var/www:ro", "/var/www", true},
		{"data:/var/www:rw", "/var/www", false},
	} {
		doc := ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "volumes": []any{tc.entry},
		}}}
		spec, err := NormalizeCompose(doc, "a", "p")
		require.NoError(t, err, "entry %q", tc.entry)
		vol := spec.GetProcesses()[0].GetVolumes()[0]
		assert.Equal(t, tc.target, vol.GetTarget(), "entry %q: mode must not leak into target", tc.entry)
		assert.Equal(t, tc.readOnly, vol.GetReadOnly(), "entry %q", tc.entry)
	}
}

// 直投形态探针声明（B2 API 面）：http/tcp 各一 + 互斥在校验层（API 面测试）。
func TestImageDeployProbeDeclaration(t *testing.T) {
	s, err := ImageDeploy("a", "p", "nginx:1.27", "", &specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_HttpPath{HttpPath: "/healthz"}, Retries: 3,
	}, nil, nil)
	require.NoError(t, err)
	require.Len(t, s.GetProcesses(), 1)
	assert.Equal(t, "/healthz", s.GetProcesses()[0].GetHealthcheck().GetHttpPath())

	s, err = ImageDeploy("a", "p", "nginx:1.27", "worker", &specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_TcpPort{TcpPort: 5432},
	}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(5432), s.GetProcesses()[0].GetHealthcheck().GetTcpPort())
}

// 端口声明（F3.5）：声明端口 = Route-facing——进程携带 ports 且挂靠项目
// default 网（Proxy 可达性）；未声明维持"无网络即无 DNS 面"（ADR-0034）。
func TestImageDeployPortDeclaration(t *testing.T) {
	ports := []*specv1.PortSpec{{Port: 8080, Protocol: specv1.Protocol_PROTOCOL_H2C}}
	s, err := ImageDeploy("a", "p", "nginx:1.27", "", nil, nil, ports)
	require.NoError(t, err)
	p := s.GetProcesses()[0]
	require.Len(t, p.GetPorts(), 1)
	assert.Equal(t, int32(8080), p.GetPorts()[0].GetPort())
	assert.Equal(t, specv1.Protocol_PROTOCOL_H2C, p.GetPorts()[0].GetProtocol())
	assert.Equal(t, []string{"default"}, p.GetNetworks())

	s, err = ImageDeploy("a", "p", "nginx:1.27", "", nil, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, s.GetProcesses()[0].GetPorts())
	assert.Empty(t, s.GetProcesses()[0].GetNetworks())
}

// CMD-SHELL 探针的引号结构保真（staging 真机实证修复，2026-10-02）：载荷
// 是单个 shell 字符串，按空白切分会撕碎引号（bash -c 'exec 3<>/dev/tcp/...'
// → 碎 argv → 探针恒败 unexpected EOF）——整体经 sh -c 承载。
func TestComposeCMDShellProbeQuoting(t *testing.T) {
	doc := ComposeDoc{"services": map[string]any{
		"minio": map[string]any{
			"image": "pgsty/silo:1",
			"healthcheck": map[string]any{
				"test": []any{"CMD-SHELL", "bash -c 'exec 3<>/dev/tcp/127.0.0.1/9000'"},
			},
		},
	}}
	spec, err := NormalizeCompose(doc, "a", "p")
	require.NoError(t, err)
	hc := spec.GetProcesses()[0].GetHealthcheck()
	require.NotNil(t, hc.GetExec())
	assert.Equal(t, []string{"sh", "-c", "bash -c 'exec 3<>/dev/tcp/127.0.0.1/9000'"}, hc.GetExec().GetCommand())
}
