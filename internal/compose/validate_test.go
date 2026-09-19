package compose

import (
	"context"
	"strings"
	"testing"
)

// TestValidationRejectMatrix 验收 2：受控子集校验矩阵——每个拒绝路径至少
// 一例反例（期望码 + 违规路径），并抽样正例对照（同码族不误伤合法形态）。
func TestValidationRejectMatrix(t *testing.T) {
	cases := []struct {
		name       string
		compose    string
		wantCode   string
		wantPath   string // 错误 context.path 前缀（空则不断言）
		negativeOf string // 对照正例名（PositiveCases 中的键）
	}{
		// ── v0.1 拒绝清单（E_COMPOSE_UNSUPPORTED）──
		{"reject_depends_on", `
name: my-api
services:
  web: { image: nginx }
  api:
    image: my/api
    depends_on: [web]
`, "E_COMPOSE_UNSUPPORTED", "services.api.depends_on", "pos_multi_service"},

		{"reject_extends", `
name: my-api
services:
  base: { image: nginx }
  web:
    image: my/api
    extends: { service: base }
`, "E_COMPOSE_UNSUPPORTED", "services.web.extends", "pos_multi_service"},

		{"reject_include", `
include: [other.compose.yaml]
name: my-api
services:
  web: { image: nginx }
`, "E_COMPOSE_UNSUPPORTED", "include", "pos_multi_service"},

		{"reject_profiles", `
name: my-api
services:
  web:
    image: nginx
    profiles: [debug]
`, "E_COMPOSE_UNSUPPORTED", "services.web.profiles", "pos_multi_service"},

		{"reject_configs", `
name: my-api
services:
  web:
    image: nginx
    configs: [app_conf]
`, "E_COMPOSE_UNSUPPORTED", "services.web.configs", "pos_multi_service"},

		{"reject_external_network", `
name: my-api
services:
  web:
    image: nginx
    networks: [backnet]
networks:
  backnet:
    external: true
`, "E_COMPOSE_UNSUPPORTED", "networks.backnet.external", "pos_stack_network"},

		{"reject_network_mode_host", `
name: my-api
services:
  web:
    image: nginx
    network_mode: host
`, "E_COMPOSE_UNSUPPORTED", "services.web.network_mode", "pos_stack_network"},

		// ── 危险字段（E_COMPOSE_UNSUPPORTED，默认拒绝）──
		{"reject_privileged_false_still_rejected", `
name: my-api
services:
  web:
    image: nginx
    privileged: false
`, "E_COMPOSE_UNSUPPORTED", "services.web.privileged", ""},

		{"reject_cap_add", `
name: my-api
services:
  web:
    image: nginx
    cap_add: [NET_ADMIN]
`, "E_COMPOSE_UNSUPPORTED", "services.web.cap_add", ""},

		{"reject_pid_host", `
name: my-api
services:
  web:
    image: nginx
    pid: host
`, "E_COMPOSE_UNSUPPORTED", "services.web.pid", ""},

		{"reject_devices", `
name: my-api
services:
  web:
    image: nginx
    devices: [/dev/dri:/dev/dri]
`, "E_COMPOSE_UNSUPPORTED", "services.web.devices", ""},

		{"reject_host_bind", `
name: my-api
services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: ./data
        target: /data
`, "E_COMPOSE_UNSUPPORTED", "services.web.volumes", "pos_named_volume"},

		{"reject_docker_sock_mount", `
name: my-api
services:
  web:
    image: nginx
    volumes:
      - docker_sock:/var/run/docker.sock
volumes:
  docker_sock:
`, "E_COMPOSE_UNSUPPORTED", "services.web.volumes", "pos_named_volume"},

		// ── 受管字段（E_COMPOSE_MANAGED_FIELD）──
		{"reject_failure_action_rollback", `
name: my-api
services:
  web:
    image: nginx
    deploy:
      update_config:
        failure_action: rollback
`, "E_COMPOSE_MANAGED_FIELD", "services.web.deploy.update_config.failure_action", "pos_managed_fields"},

		{"reject_monitor_10s", `
name: my-api
services:
  web:
    image: nginx
    deploy:
      update_config:
        monitor: 10s
`, "E_COMPOSE_MANAGED_FIELD", "services.web.deploy.update_config.monitor", "pos_managed_fields"},

		// ── 更新策略安全（E_COMPOSE_UNSAFE_STRATEGY）──
		{"reject_start_first_with_volume", `
name: my-api
services:
  web:
    image: nginx
    volumes: [data:/var/lib/data]
    deploy:
      update_config:
        order: start-first
volumes:
  data:
`, "E_COMPOSE_UNSAFE_STRATEGY", "services.web.deploy.update_config.order", "pos_stop_first_with_volume"},

		{"reject_start_first_global", `
name: my-api
services:
  agent:
    image: nginx
    deploy:
      mode: global
      update_config:
        order: start-first
`, "E_COMPOSE_UNSAFE_STRATEGY", "services.agent.deploy.update_config.order", "pos_global"},

		// ── 域名契约（E_DOMAIN_CONFLICT / E_DOMAIN_UNSUPPORTED）──
		{"reject_domain_wildcard", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "*.example.com"
`, "E_DOMAIN_UNSUPPORTED", "services.web.labels.fleetly.domains", "pos_domains"},

		{"reject_domain_per_service_limit", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "a.com, b.com, c.com, d.com, e.com, f.com"
`, "E_DOMAIN_UNSUPPORTED", "services.web.labels.fleetly.domains", "pos_domains"},

		{"reject_domain_conflict_across_services", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "api.example.com"
  admin:
    image: nginx
    labels:
      fleetly.domains: "API.Example.com"
`, "E_DOMAIN_CONFLICT", "", "pos_domains_two_services"},

		{"reject_domain_per_app_limit", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "a.com, b.com, c.com, d.com"
  extra:
    image: nginx
    labels:
      fleetly.domains: "e.com, f.com, g.com, h.com"
  third:
    image: nginx
    labels:
      fleetly.domains: "i.com, j.com, k.com, l.com"
`, "E_DOMAIN_UNSUPPORTED", "", "pos_domains_two_services"},

		// ── 保留 label 前缀（E_LABEL_RESERVED）──
		{"reject_reserved_label", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.custom: mine
`, "E_LABEL_RESERVED", "services.web.labels.fleetly.custom", "pos_domains"},

		// ── 放置契约（E_PLACEMENT_NODE_INVALID / E_PLACEMENT_LABEL_CONFLICT）──
		{"reject_placement_node_invalid_ulid", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.placement.node: "n_not-a-ulid"
`, "E_PLACEMENT_NODE_INVALID", "services.web.labels.fleetly.placement.node", "pos_placement"},

		{"reject_placement_label_conflict", `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.placement.node: srv-01
  worker:
    image: nginx
    labels:
      fleetly.placement.node: srv-02
`, "E_PLACEMENT_LABEL_CONFLICT", "", "pos_placement"},

		// ── 白名单缺省拒绝（E_COMPOSE_UNSUPPORTED）──
		// 注意：顶层未知键在 loader schema 校验即被拒（additionalProperties
		//=false），早于白名单——同一错误码，无 path 上下文。
		{"reject_unknown_top_level", `
name: my-api
x-custom: {}
services:
  web: { image: nginx }
runtime_config: {}
`, "E_COMPOSE_UNSUPPORTED", "", ""},

		{"reject_unknown_service_field", `
name: my-api
services:
  web:
    image: nginx
    container_name: my-web
`, "E_COMPOSE_UNSUPPORTED", "services.web.container_name", ""},

		{"reject_ports", `
name: my-api
services:
  web:
    image: nginx
    ports: ["8080:80"]
`, "E_COMPOSE_UNSUPPORTED", "services.web.ports", "pos_expose"},

		{"reject_build_unsupported_subkey", `
name: my-api
services:
  web:
    build:
      context: .
      args: { VER: "1" }
`, "E_COMPOSE_UNSUPPORTED", "services.web.build.args", "pos_build"},

		{"reject_healthcheck_disable", `
name: my-api
services:
  web:
    image: nginx
    healthcheck:
      disable: true
`, "E_COMPOSE_UNSUPPORTED", "services.web.healthcheck.disable", "pos_healthcheck"},

		{"reject_env_pass_through", `
name: my-api
services:
  web:
    image: nginx
    environment:
      - EMPTY_VAR
`, "E_COMPOSE_UNSUPPORTED", "services.web.environment", "pos_env_literal"},

		{"reject_env_null_value", `
name: my-api
services:
  web:
    image: nginx
    environment:
      EMPTY_VAR:
`, "E_COMPOSE_UNSUPPORTED", "services.web.environment.EMPTY_VAR", "pos_env_literal"},

		{"reject_no_image_no_build", `
name: my-api
services:
  web:
    expose: ["80"]
`, "E_COMPOSE_UNSUPPORTED", "services.web", "pos_build"},

		// ── secrets 显式拒绝（S16-C1：v0.1 平台密钥库未接入，Load 期即拒；
		// 顶层与服务级同拒，外部引用/本地定义等形态细分不再可达）──
		{"reject_secrets_top_level", `
name: my-api
services:
  web: { image: nginx }
secrets:
  db_url: { external: true }
`, "E_COMPOSE_UNSUPPORTED", "secrets", ""},

		{"reject_secrets_service_level", `
name: my-api
services:
  web:
    image: nginx
    secrets: [db_url]
`, "E_COMPOSE_UNSUPPORTED", "services.web.secrets", ""},

		{"reject_secrets_local_file_definition", `
name: my-api
services:
  web:
    image: nginx
    secrets: [db_url]
secrets:
  db_url:
    file: ./db_url.txt
`, "E_COMPOSE_UNSUPPORTED", "secrets", ""},

		{"reject_service_volume_tmpfs", `
name: my-api
services:
  web:
    image: nginx
    volumes:
      - type: tmpfs
        target: /cache
`, "E_COMPOSE_UNSUPPORTED", "services.web.volumes[0].type", "pos_named_volume"},

		{"reject_service_network_aliases", `
name: my-api
services:
  web:
    image: nginx
    networks:
      backnet:
        aliases: [alias-web]
networks:
  backnet:
`, "E_COMPOSE_UNSUPPORTED", "services.web.networks.backnet", "pos_stack_network"},

		{"reject_volume_name_override", `
name: my-api
services:
  web: { image: nginx }
volumes:
  data:
    name: pinned-name
`, "E_COMPOSE_UNSUPPORTED", "volumes.data.name", "pos_named_volume"},

		{"reject_replicas_with_volumes", `
name: my-api
services:
  web:
    image: nginx
    volumes: [data:/var/lib/data]
    deploy:
      replicas: 3
volumes:
  data:
`, "E_COMPOSE_UNSUPPORTED", "", "pos_named_volume"},

		{"reject_constraint_outside_namespace", `
name: my-api
services:
  web:
    image: nginx
    deploy:
      placement:
        constraints: [node.role == worker]
`, "E_COMPOSE_UNSUPPORTED", "services.web.deploy.placement.constraints[0]", "pos_placement_constraint"},

		{"reject_empty_services", `
name: my-api
services: {}
`, "E_COMPOSE_UNSUPPORTED", "services", ""},

		{"reject_bad_spec_name", `
name: My API!
services:
  web: { image: nginx }
`, "E_COMPOSE_UNSUPPORTED", "name", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ae := loadErr(t, tc.compose, tc.wantCode)
			if tc.wantPath != "" {
				if path := ae.Context()["path"]; !strings.HasPrefix(path, tc.wantPath) {
					t.Errorf("context.path = %q, 期望前缀 %q", path, tc.wantPath)
				}
			}
		})
	}
}

// PositiveCases 是拒绝矩阵的对照正例（同码族的合法形态必须通过——保证
// 校验没有把合法面一起拒掉）。
var PositiveCases = map[string]string{ //nolint:gosec // compose 夹具文本（含 secrets 字段名），非凭证
	"pos_multi_service": `
name: my-api
services:
  web: { image: nginx, expose: ["80"] }
  worker:
    image: my/worker
    command: run --queue
`,
	"pos_build": `
name: my-api
services:
  web:
    build: .
`,
	"pos_build_dockerfile": `
name: my-api
services:
  web:
    build: { context: ., dockerfile: deploy/Dockerfile }
`,
	"pos_expose": `
name: my-api
services:
  web: { image: nginx, expose: ["8080", "9090"] }
`,
	"pos_healthcheck": `
name: my-api
services:
  web:
    image: nginx
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost"]
      interval: 30s
      timeout: 5s
      retries: 5
      start_period: 15s
`,
	"pos_env_literal": `
name: my-api
services:
  web:
    image: nginx
    environment:
      FOO: bar
      BAZ: "quoted value"
`,
	"pos_managed_fields": `
name: my-api
services:
  web:
    image: nginx
    deploy:
      replicas: 2
      update_config:
        order: start-first
        failure_action: pause
        monitor: 5s
        parallelism: 2
        delay: 10s
`,
	"pos_stop_first_with_volume": `
name: my-api
services:
  web:
    image: nginx
    volumes: [data:/var/lib/data]
    deploy:
      update_config:
        order: stop-first
volumes:
  data:
`,
	"pos_named_volume": `
name: my-api
services:
  web:
    image: nginx
    volumes:
      - data:/var/lib/data
      - { type: volume, source: data, target: /other, read_only: true }
volumes:
  data: { driver: local }
`,
	"pos_stack_network": `
name: my-api
services:
  web:
    image: nginx
    networks: [frontnet, backnet]
  db:
    image: postgres
    networks: [backnet]
networks:
  frontnet:
  backnet:
    driver: overlay
`,
	"pos_domains": `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "Bücher.de, app.example.com"
`,
	"pos_domains_two_services": `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "api.example.com, web.example.com"
  admin:
    image: nginx
    labels:
      fleetly.domains: "admin.example.com"
`,
	"pos_placement": `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.placement.node: srv-01
  worker:
    image: nginx
    labels:
      fleetly.placement.node: srv-01
`,
	"pos_placement_constraint": `
name: my-api
services:
  web:
    image: nginx
    deploy:
      placement:
        constraints:
          - node.labels.fleetly.rack == r1
          - node.labels.fleetly.zone != z9
`,
	"pos_global": `
name: my-api
services:
  agent:
    image: nginx
    deploy:
      mode: global
      update_config:
        order: stop-first
`,
	"pos_stop_signal": `
name: my-api
services:
  web:
    image: nginx
    stop_signal: SIGINT
    stop_grace_period: 30s
`,
}

// TestValidationPositiveMatrix 跑全部对照正例（必须全通过）。
func TestValidationPositiveMatrix(t *testing.T) {
	for name, content := range PositiveCases {
		t.Run(name, func(t *testing.T) {
			loadOK(t, writeCompose(t, content))
		})
	}
}

// TestUserLabelNotPassedWarning S16-C2：非 fleetly.* 服务 label 产出 W 级
// 警告（Kind=user_label_not_passed，随 Load warnings 通道带出）；fleetly.*
// 平台约定 label 不触发。用户 label 仍被放行（不阻断），只是披露不透传。
func TestUserLabelNotPassedWarning(t *testing.T) {
	path := writeCompose(t, `
name: my-api
services:
  web:
    image: nginx
    labels:
      com.example.owner: platform-team
      com.example.version: "2"
      fleetly.domains: "api.example.com"
`)
	spec, warnings, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(spec.Services) != 1 {
		t.Fatalf("services = %d, 期望 1（用户 label 放行不阻断）", len(spec.Services))
	}
	var hit bool
	for _, w := range warnings {
		if w.Kind == WarningKindUserLabelNotPassed {
			hit = true
			if w.Service != "web" {
				t.Errorf("warning service = %q, 期望 web", w.Service)
			}
			if !strings.Contains(w.Message, "com.example.owner") || !strings.Contains(w.Message, "com.example.version") {
				t.Errorf("warning message 未列出用户 label 键: %s", w.Message)
			}
			if !strings.Contains(w.Message, "不透传") {
				t.Errorf("warning message 缺不透传说明: %s", w.Message)
			}
		}
	}
	if !hit {
		t.Errorf("warnings 缺 %s: %+v", WarningKindUserLabelNotPassed, warnings)
	}
	// 对照：纯平台 label 不触发该警告（无 healthcheck 的 W_DEPLOY_NO_HEALTHCHECK
	// 属另一通道，不在断言面）。
	_, cleanWarnings, err := Load(context.Background(), writeCompose(t, `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.domains: "api.example.com"
`))
	if err != nil {
		t.Fatalf("Load clean: %v", err)
	}
	for _, w := range cleanWarnings {
		if w.Kind == WarningKindUserLabelNotPassed {
			t.Errorf("纯平台 label 触发用户 label 警告: %+v", w)
		}
	}
}
