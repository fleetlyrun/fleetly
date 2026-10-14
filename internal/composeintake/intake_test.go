package composeintake_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/composeintake"
)

// fromYAML 是全部用例的短入口（app/project 锚恒定）。
func fromYAML(t *testing.T, body string) (*specv1.AppSpec, error) {
	t.Helper()
	return composeintake.FromYAML([]byte(body), "app-1", "prj-1", nil)
}

func vars(varmap map[string]string) composeintake.VariableLookup {
	return func(name string) (string, bool) {
		v, ok := varmap[name]
		return v, ok
	}
}

func procByName(t *testing.T, s *specv1.AppSpec, name string) *specv1.ProcessSpec {
	t.Helper()
	for _, p := range s.GetProcesses() {
		if p.GetName() == name {
			return p
		}
	}
	t.Fatalf("process %q not found", name)
	return nil
}

// 受控子集正例（normalize.go 旧面的全字段正面 + 官方解析升级面）。
func TestFromYAMLSubset(t *testing.T) {
	body := `
services:
  web:
    image: nginx:1.27
    command: nginx -g daemon off
    environment:
      MODE: prod
    ports:
      - "8080"
      - target: 5432
        app_protocol: tcp
      - target: 7070
        app_protocol: h2c
    deploy:
      replicas: 2
      resources:
        limits:
          cpus: "1.5"
          memory: 512m
    networks:
      - default
  worker:
    image: busybox:1.37
    command: ["sh", "-c", "while true; do sleep 1; done"]
networks:
  default:
    external: true
`
	s, err := fromYAML(t, body)
	require.NoError(t, err)
	require.Len(t, s.GetProcesses(), 2)
	// 进程序按服务名排序（源镜像取首个带 image 的服务——确定序）。
	assert.Equal(t, "web", s.GetProcesses()[0].GetName())
	assert.Equal(t, "worker", s.GetProcesses()[1].GetName())
	assert.Equal(t, "nginx:1.27", s.GetSource().GetImage().GetRef())

	web := procByName(t, s, "web")
	assert.Equal(t, int64(2), web.GetReplicas())
	assert.Equal(t, int64(1500), web.GetResources().GetCpuMillis())
	assert.Equal(t, int64(512), web.GetResources().GetMemoryMb())
	require.Len(t, web.GetPorts(), 3)
	assert.Equal(t, specv1.Protocol_PROTOCOL_HTTP, web.GetPorts()[0].GetProtocol(), "bare port = route-facing http default")
	assert.Equal(t, specv1.Protocol_PROTOCOL_TCP, web.GetPorts()[1].GetProtocol(), "route protocol is declared with app_protocol")
	assert.Equal(t, specv1.Protocol_PROTOCOL_H2C, web.GetPorts()[2].GetProtocol(), "app_protocol is the route protocol declaration")
	assert.Equal(t, map[string]string{"MODE": "prod"}, web.GetEnv())
	assert.Equal(t, []string{"nginx", "-g", "daemon", "off"}, web.GetCommand())

	worker := procByName(t, s, "worker")
	assert.Equal(t, []string{"sh", "-c", "while true; do sleep 1; done"}, worker.GetCommand(), "list-form command is literal argv")
	assert.Empty(t, worker.GetNetworks(), "no declared port keeps the no-network honesty semantics (ADR-0034)")
}

// external 声明律正例：secrets/volumes 名即平台实体（name 覆盖形态含
// 平台命名 database:<name>——冒号进不了 schema 键，经 key→name 表达）。
func TestFromYAMLExternalDeclarations(t *testing.T) {
	body := `
services:
  web:
    image: nginx:1.27
    volumes:
      - data:/var/www
      - assets:/srv/assets:ro
    secrets:
      - api-token
      - source: db-password
networks:
  back:
    external: true
volumes:
  data:
    external: true
  assets:
    external: true
    name: team-assets
secrets:
  api-token:
    external: true
  db-password:
    external: true
    name: "database:pg"
`
	s, err := fromYAML(t, body)
	require.NoError(t, err)
	web := procByName(t, s, "web")
	require.Len(t, web.GetVolumes(), 2)
	assert.Equal(t, "data", web.GetVolumes()[0].GetVolumeId())
	assert.Equal(t, "/var/www", web.GetVolumes()[0].GetTarget())
	assert.False(t, web.GetVolumes()[0].GetReadOnly())
	assert.Equal(t, "team-assets", web.GetVolumes()[1].GetVolumeId(), "name override resolves the platform volume name")
	assert.True(t, web.GetVolumes()[1].GetReadOnly())
	assert.Equal(t, []string{"api-token", "database:pg"}, web.GetSecretRefs(), "secret refs resolve name overrides (platform database naming)")
}

// 探针三面（exec/http/tcp）+ 节律 + 扩展键拼写。
func TestFromYAMLHealthchecks(t *testing.T) {
	body := `
services:
  web:
    image: nginx:1.27
    healthcheck:
      x-fleetly-http-path: /healthz
      interval: 10s
      timeout: 3s
      retries: 5
  db:
    image: postgres:16
    healthcheck:
      x-fleetly-tcp-port: 5432
      start_period: 20s
  minio:
    image: pgsty/silo:1
    healthcheck:
      test: ["CMD-SHELL", "bash -c 'exec 3<>/dev/tcp/127.0.0.1/9000'"]
`
	s, err := fromYAML(t, body)
	require.NoError(t, err)

	hc := procByName(t, s, "web").GetHealthcheck()
	require.NotNil(t, hc)
	assert.Equal(t, "/healthz", hc.GetHttpPath())
	assert.Equal(t, "10s", hc.GetInterval().AsDuration().String())
	assert.Equal(t, "3s", hc.GetTimeout().AsDuration().String())
	assert.Equal(t, int32(5), hc.GetRetries())

	dbhc := procByName(t, s, "db").GetHealthcheck()
	assert.Equal(t, int32(5432), dbhc.GetTcpPort())
	assert.Equal(t, "20s", dbhc.GetStartPeriod().AsDuration().String())

	// CMD-SHELL 载荷是单个 shell 字符串——整体经 sh -c 承载（引号保真，
	// staging 实证口径，ADR-0033）。
	exec := procByName(t, s, "minio").GetHealthcheck().GetExec()
	require.NotNil(t, exec)
	assert.Equal(t, []string{"sh", "-c", "bash -c 'exec 3<>/dev/tcp/127.0.0.1/9000'"}, exec.GetCommand())
}

// deploy.x-fleetly-strategy（官方 #extensions 通道）：blue-green 人类词形
// → 枚举；缺省不落字段（零漂移锚）；值域外拒绝指名键与合法值。
func TestFromYAMLDeployStrategy(t *testing.T) {
	bodyFor := func(strategy string) string {
		svc := "    image: nginx:1.27\n"
		if strategy != "" {
			svc += "    deploy:\n      x-fleetly-strategy: " + strategy + "\n"
		}
		return "services:\n  web:\n" + svc
	}
	s, err := fromYAML(t, bodyFor("blue-green"))
	require.NoError(t, err)
	assert.Equal(t, specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN, s.GetProcesses()[0].GetStrategy())

	s, err = fromYAML(t, bodyFor("rolling"))
	require.NoError(t, err)
	assert.Equal(t, specv1.DeployStrategy_DEPLOY_STRATEGY_ROLLING, s.GetProcesses()[0].GetStrategy())

	s, err = fromYAML(t, bodyFor(""))
	require.NoError(t, err)
	assert.Equal(t, specv1.DeployStrategy_DEPLOY_STRATEGY_UNSPECIFIED, s.GetProcesses()[0].GetStrategy(), "absent strategy freezes as zero value (rolling normalization at projection)")

	_, err = fromYAML(t, bodyFor("immediate"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "x-fleetly-strategy")
	assert.Contains(t, err.Error(), `strategy must be rolling or blue-green (got "immediate")`)
}

// 端口声明的 Route-facing 两半边：声明 ports 补挂 default 网；显式名单
// 原样保留；未声明端口零挂网。
func TestFromYAMLPortsAttachDefaultNetwork(t *testing.T) {
	s, err := fromYAML(t, "services:\n  web:\n    image: traefik/whoami:v1.10\n    ports: [\"80\"]\n")
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, s.GetProcesses()[0].GetNetworks())

	s, err = fromYAML(t, `
services:
  web:
    image: traefik/whoami:v1.10
    ports: ["80"]
    networks: [back]
networks:
  back:
    external: true
`)
	require.NoError(t, err)
	assert.Equal(t, []string{"back", "default"}, s.GetProcesses()[0].GetNetworks(), "declared list preserved; default appended for route reachability")

	s, err = fromYAML(t, "services:\n  web:\n    image: traefik/whoami:v1.10\n")
	require.NoError(t, err)
	assert.Empty(t, s.GetProcesses()[0].GetNetworks())
}

// firstBootJobs 翻译（ADR-0033 形态）：command 两形态、environment 两形态、
// secrets 引用（经 external 声明解析平台名）、ttl。
func TestFromYAMLFirstBootJobs(t *testing.T) {
	body := `
services:
  web:
    image: nginx:1.27
x-fleetly-first-boot-jobs:
  - name: migrate
    image: migrate/migrate:v4.18.1
    command: ["sh", "-c", "migrate -database \"$(cat /run/secrets/database:pg)\" up"]
    secrets: [dbcred]
    ttl: 300s
  - name: roles-sig
    image: ghcr.io/example/app:sha-1
    command: app admin sync-roles-sig
    environment:
      - MODE=deploy
      - BATCH=16
    ttl: 1m
secrets:
  dbcred:
    external: true
    name: "database:pg"
`
	s, err := fromYAML(t, body)
	require.NoError(t, err)
	require.Len(t, s.GetFirstBootJobs(), 2)

	migrate := s.GetFirstBootJobs()[0]
	assert.Equal(t, "migrate", migrate.GetName())
	assert.Equal(t, "5m0s", migrate.GetTtl().AsDuration().String())
	p := migrate.GetProcess()
	assert.Equal(t, "migrate/migrate:v4.18.1", p.GetImage())
	assert.Equal(t, []string{"sh", "-c", "migrate -database \"$(cat /run/secrets/database:pg)\" up"}, p.GetCommand())
	assert.Equal(t, []string{"database:pg"}, p.GetSecretRefs(), "job secret refs resolve external name overrides")
	assert.Equal(t, "migrate", p.GetName())

	p2 := s.GetFirstBootJobs()[1].GetProcess()
	assert.Equal(t, []string{"app", "admin", "sync-roles-sig"}, p2.GetCommand())
	assert.Equal(t, map[string]string{"MODE": "deploy", "BATCH": "16"}, p2.GetEnv())
}

// 拒绝面矩阵：官方面未开放（精确理由）/ schema 拒非 x- 键 / external 律 /
// 端口 host 映射 / 插值未消解。每条断言即 ADR-0074 验收锚。
func TestFromYAMLRejections(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"deploy.strategy old spelling dies at the official schema",
			"services:\n  web:\n    image: nginx\n    deploy:\n      strategy: blue-green\n",
			"additional properties 'strategy' not allowed",
		},
		{
			"healthcheck.http_path old spelling dies at the official schema",
			"services:\n  web:\n    image: nginx\n    healthcheck:\n      http_path: /hz\n",
			"additional properties 'http_path' not allowed",
		},
		{
			"managed restart_policy rejected",
			"services:\n  web:\n    image: nginx\n    deploy:\n      restart_policy: {condition: any}\n",
			"official compose field \"restart_policy\" is not enabled",
		},
		{
			"managed update_config rejected",
			"services:\n  web:\n    image: nginx\n    deploy:\n      update_config: {order: start-first}\n",
			"official compose field \"update_config\" is not enabled",
		},
		{
			"managed placement rejected",
			"services:\n  web:\n    image: nginx\n    deploy:\n      placement: {constraints: [node.role == manager]}\n",
			"official compose field \"placement\" is not enabled",
		},
		{
			"build source rejected",
			"services:\n  web:\n    build: .\n",
			"official compose field \"build\" is not enabled",
		},
		{
			"privileged rejected",
			"services:\n  web:\n    image: nginx\n    privileged: true\n",
			"official compose field \"privileged\" is not enabled",
		},
		{
			"depends_on rejected",
			"services:\n  web:\n    image: nginx\n    depends_on: [db]\n  db:\n    image: redis\n",
			"official compose field \"depends_on\" is not enabled",
		},
		{
			"env_file rejected",
			"services:\n  web:\n    image: nginx\n    env_file: .env\n",
			"official compose field \"env_file\" is not enabled",
		},
		{
			"top-level include rejected",
			"services:\n  web:\n    image: nginx\ninclude:\n  - other.yaml\n",
			"official compose field \"include\" is not enabled",
		},
		{
			"top-level configs rejected",
			"services:\n  web:\n    image: nginx\nconfigs:\n  c:\n    external: true\n",
			"official compose field \"configs\" is not enabled",
		},
		{
			"top-level project name rejected",
			"name: my-project\nservices:\n  web:\n    image: nginx\n",
			"project name is managed by the platform",
		},
		{
			"unknown extension namespace rejected",
			"services:\n  web:\n    image: nginx\nx-vendor-thing: 1\n",
			"unknown extension namespace",
		},
		{
			"service-level extension key rejected",
			"services:\n  web:\n    image: nginx\n    x-fleetly-strategy: blue-green\n",
			"not enabled at the service level",
		},
		{
			"host port mapping rejected (Sscanf truncation hole is dead)",
			"services:\n  web:\n    image: nginx\n    ports: [\"9090:80\"]\n",
			"host port publishing is not supported",
		},
		{
			"host ip mapping rejected",
			"services:\n  web:\n    image: nginx\n    ports: [\"127.0.0.1:8080:80\"]\n",
			"host port publishing is not supported",
		},
		{
			"published long syntax rejected",
			"services:\n  web:\n    image: nginx\n    ports:\n      - target: 80\n        published: 9090\n",
			"host port publishing is not supported",
		},
		{
			"udp port rejected",
			"services:\n  web:\n    image: nginx\n    ports: [\"5353/udp\"]\n",
			"udp is not a routeable protocol",
		},
		{
			"network aliases rejected (map-form silent drop hole is dead)",
			"services:\n  web:\n    image: nginx\n    networks:\n      back:\n        aliases: [web1]\nnetworks:\n  back:\n    external: true\n",
			"network attachments are by name only",
		},
		{
			"undeclared network reference rejected by the official consistency check",
			"services:\n  web:\n    image: nginx\n    networks: [back]\n",
			"undefined network",
		},
		{
			"non-external volume declaration rejected",
			"services:\n  web:\n    image: nginx\nvolumes:\n  data: {}\n",
			"must be declared external: true",
		},
		{
			"volume with driver rejected",
			"services:\n  web:\n    image: nginx\nvolumes:\n  data:\n    external: true\n    driver: local\n",
			"conflicting parameters",
		},
		{
			"non-external secret declaration rejected",
			"services:\n  web:\n    image: nginx\nsecrets:\n  s:\n    file: ./s.txt\n",
			"must be declared external: true",
		},
		{
			"secret with file value rejected",
			"services:\n  web:\n    image: nginx\n    secrets: [s]\nsecrets:\n  s:\n    external: true\n    file: ./s.txt\n",
			"official compose field \"file\" is not enabled",
		},
		{
			"bind mount rejected",
			"services:\n  web:\n    image: nginx\n    volumes: [\"./src:/app\"]\n",
			"is not supported",
		},
		{
			"secret uid rejected",
			"services:\n  web:\n    image: nginx\n    secrets:\n      - source: s\n        uid: \"1000\"\nsecrets:\n  s:\n    external: true\n",
			"uid/gid/mode are not translated",
		},
		{
			"secret target remap rejected",
			"services:\n  web:\n    image: nginx\n    secrets:\n      - source: s\n        target: renamed\nsecrets:\n  s:\n    external: true\n",
			"target remapping",
		},
		{
			"probe exclusivity across sources",
			"services:\n  web:\n    image: nginx\n    healthcheck:\n      x-fleetly-http-path: /h\n      x-fleetly-tcp-port: 80\n",
			"one of test, x-fleetly-http-path or x-fleetly-tcp-port",
		},
		{
			"relative http path rejected",
			"services:\n  web:\n    image: nginx\n    healthcheck:\n      x-fleetly-http-path: healthz\n",
			"starting with '/'",
		},
		{
			"bare env entry rejected",
			"services:\n  web:\n    image: nginx\n    environment: [MODE]\n",
			"carries no value",
		},
		{
			"profile-gated service rejected",
			"services:\n  web:\n    image: nginx\n    profiles: [extra]\n",
			"profiles are not enabled",
		},
		{
			"unknown job key rejected",
			"services:\n  web:\n    image: nginx\nx-fleetly-first-boot-jobs:\n  - name: j\n    image: i\n    ttl: 1m\n    bad: 1\n",
			"unsupported field \"bad\" for a first boot job",
		},
		{
			"job volumes banned face",
			"services:\n  web:\n    image: nginx\nx-fleetly-first-boot-jobs:\n  - name: j\n    image: i\n    ttl: 1m\n    volumes: [d:/d]\n",
			"cannot mount volumes",
		},
		{
			"job ttl missing",
			"services:\n  web:\n    image: nginx\nx-fleetly-first-boot-jobs:\n  - name: j\n    image: i\n",
			"hard timeout",
		},
		{
			"job secret not declared",
			"services:\n  web:\n    image: nginx\nx-fleetly-first-boot-jobs:\n  - name: j\n    image: i\n    ttl: 1m\n    secrets: [nope]\n",
			"is not declared",
		},
		{
			"unresolved interpolation hole fails closed",
			"services:\n  web:\n    image: nginx\n    environment:\n      URL: ${MISSING_VAR}\n",
			"\"MISSING_VAR\" is not set",
		},
		{
			"unresolved short-form hole in environment fails closed",
			"services:\n  web:\n    image: nginx\n    environment:\n      URL: $MISSING_CMD\n",
			"\"MISSING_CMD\" is not set",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := fromYAML(t, tc.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// 官方插值正例：平台变量消解进值；带缺省修饰符的孔自回退；转义 $$ 保字面。
func TestFromYAMLInterpolation(t *testing.T) {
	body := `
services:
  web:
    image: nginx:1.27
    environment:
      URL: https://${HOST}/api
      FALLBACK: ${MISSING:-none}
      LITERAL: $${ESCAPED}
`
	s, err := composeintake.FromYAML([]byte(body), "app-1", "prj-1", vars(map[string]string{"HOST": "example.com"}))
	require.NoError(t, err)
	env := procByName(t, s, "web").GetEnv()
	assert.Equal(t, "https://example.com/api", env["URL"])
	assert.Equal(t, "none", env["FALLBACK"])
	assert.Equal(t, "${ESCAPED}", env["LITERAL"])
}

// command 面的 $ 透传（预逃逸）：shell 语境原样保留——脚本变量与平台
// 变量（进程 env 携带）都在运行时展开；env 值面维持 fail-closed 插值。
func TestFromYAMLCommandDollarPassthrough(t *testing.T) {
	body := `
services:
  web:
    image: nginx:1.27
    command:
      - sh
      - -c
      - 'n=$((n+1)); echo "retry $n"; migrate -database "${FLEETLY_DB_TORCHWOOD_PG_URL}?sslmode=disable"'
`
	s, err := composeintake.FromYAML([]byte(body), "app-1", "prj-1", nil)
	require.NoError(t, err)
	cmd := procByName(t, s, "web").GetCommand()
	require.NotEmpty(t, cmd)
	joined := strings.Join(cmd, " ")
	assert.Contains(t, joined, "$n", "shell loop variables pass through untouched")
	assert.Contains(t, joined, "${FLEETLY_DB_TORCHWOOD_PG_URL}", "platform-injected variables pass through for runtime expansion")
}

// 进程名/secret 名的叶子校验收尾（spec.ValidateApp 单源）。
func TestFromYAMLLeafValidation(t *testing.T) {
	_, err := fromYAML(t, "services:\n  Web.App:\n    image: nginx\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "process name")

	_, err = fromYAML(t, "services:\n  web:\n    image: nginx\n    secrets: [\"../evil\"]\nsecrets:\n  \"../evil\":\n    external: true\n")
	require.Error(t, err)
}

// 幂等锚：同输入两次归一化产物逐字段相等（服务排序 + map 落点排序）。
func TestFromYAMLDeterministic(t *testing.T) {
	body := `
services:
  web:
    image: nginx
    ports: ["80"]
  api:
    image: busybox
    networks: [back]
networks:
  back:
    external: true
`
	first, err := fromYAML(t, body)
	require.NoError(t, err)
	for i := 0; i < 8; i++ {
		again, err := fromYAML(t, body)
		require.NoError(t, err)
		assert.Equal(t, first.String(), again.String())
	}
	assert.Equal(t, "api", first.GetProcesses()[0].GetName(), "processes are sorted by service name")
}

// 服务 label fleetly.databases → database_refs（托管库引用——Ensure 期注入
// FLEETLY_DB_*）；FLEETLY_DB_* 插值孔透传原形态（运行时 shell/容器 env 展
// 开——烤空即冻死活引用，真机咬出）。
func TestFromYAMLDatabasesLabel(t *testing.T) {
	body := `
services:
  server:
    image: ghcr.io/acme/server:1
    labels:
      fleetly.databases: "torchwood-pg"
    command:
      - sh
      - -c
      - 'migrate -database "${FLEETLY_DB_TORCHWOOD_PG_URL}?sslmode=disable" up'
  sidecar:
    image: busybox:1.36
`
	s, err := composeintake.FromYAML([]byte(body), "app-1", "prj-1", nil)
	require.NoError(t, err)
	server := procByName(t, s, "server")
	assert.Equal(t, []string{"torchwood-pg"}, server.GetDatabaseRefs())
	sidecar := procByName(t, s, "sidecar")
	assert.Empty(t, sidecar.GetDatabaseRefs(), "unlabeled services carry no refs")

	// 多名 + 去重 + 空段容忍。
	multi, err := composeintake.FromYAML([]byte(`
services:
  web:
    image: nginx:1.27
    labels:
      fleetly.databases: "alpha, beta ,alpha,,gamma"
`), "app-1", "prj-1", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, multi.GetProcesses()[0].GetDatabaseRefs())
}

// 顶层 name 剥除容错（决策 3）：项目名平台管辖、compose 侧字段无消费者
// ——模板仓/dokploy 导出普遍携带，两面（git 批/控制台粘贴）剥除而非拒收。
func TestFromYAMLTopLevelNameTolerated(t *testing.T) {
	s, err := composeintake.FromYAML([]byte("name: torchwood\nservices:\n  web:\n    image: nginx:1.27\n"),
		"app-1", "prj-1", nil)
	require.NoError(t, err)
	assert.NotNil(t, procByName(t, s, "web"))
}
