package apivalidate_test

// 客户端 descriptor 校验的文案契约（P4-1 批 1）：试点族
// CreateProjectRequest.name 的 required/dns 标签双规则在服务端由 validate
// 拦截器执法、客户端经本包 Validator 预检——两面同一 descriptor 法源，
// 文案逐字节一致由本测试钉死（对齐真源见 entityid.NamePatternSource 的
// descriptor 对齐测试）。Run B1 起负例矩阵扩展为 descriptor 形态主承载：
// 手写校验族退役的每个字段形状规则在本表各有一个坏实例，断言退役前手写
// 文案逐字节（组合必填拆 per-field 文案——有意文案契约变化，逐案列注）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apivalidate"
)

// TestValidatePilotFamily 钉试点族三态：合法通过、空名报 required 文案、
// 坏形态报 dns label 文案（均逐字节，与服务端拦截器同构）。
func TestValidatePilotFamily(t *testing.T) {
	v, err := apivalidate.NewValidator()
	require.NoError(t, err, "protovalidate 构造常态不可达（CEL 环境配置级故障才失败）")

	assert.NoError(t, v.Validate(&structurev1.CreateProjectRequest{Name: "web"}),
		"合法 dns 标签必须通过")
	assert.NoError(t, v.Validate(&structurev1.CreateProjectRequest{Name: "nightly-backup-42"}),
		"含连字符的合法 dns 标签必须通过")

	t.Run("empty name reports required message", func(t *testing.T) {
		err := v.Validate(&structurev1.CreateProjectRequest{Name: ""})
		require.Error(t, err)
		// 未注册码不可能（注册表只增，E_INVALID_ARGUMENT 首发在册），
		// 直接以类型断言还原。
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
		assert.Equal(t, "name: must not be empty", e.Message())
	})

	t.Run("bad shape reports dns label message", func(t *testing.T) {
		err := v.Validate(&structurev1.CreateProjectRequest{Name: "Bad_Name"})
		require.Error(t, err)
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
		assert.Equal(t,
			`name: must be a dns label (lowercase letters, digits, hyphens; start and end alphanumeric, e.g. "web" or "nightly-backup")`,
			e.Message())
	})
}

// retired族注册文案（与 proto 注解逐字节同源；此处整段钉死防漂移）。
const (
	dnsLabelText   = `must be a dns label (lowercase letters, digits, hyphens; start and end alphanumeric, e.g. "web" or "nightly-backup")`
	secretNameText = `secret names must match "[A-Za-z0-9][A-Za-z0-9:._-]{0,63}", start with a letter or digit, and must not contain ".." (secret names become /run/secrets/<name> paths)`
	envNameText    = `variable names must match "[A-Za-z_][A-Za-z0-9_]{0,63}" (variable names become environment keys)`
)

// TestValidateRetiredFamilyNegativeMatrix 是「负例矩阵从手写形态转
// descriptor 形态」的主承载（P4-1 批 1 Run B1）：对本批每个带注解的请求
// 消息构造坏实例，断言 Validator 返回的 message 与退役前手写文案逐字节
// （组合必填拆 per-field，多字段违规按字段序 "; " 连接——与服务端拦截器
// 同构）。文案变化的案子在注释里列明拆分前形态。
func TestValidateRetiredFamilyNegativeMatrix(t *testing.T) {
	v, err := apivalidate.NewValidator()
	require.NoError(t, err, "protovalidate 构造常态不可达（CEL 环境配置级故障才失败）")

	perField := func(pairs ...string) string { return strings.Join(pairs, "; ") }
	const reqMsg = "must not be empty"

	cases := []struct {
		name string
		msg  proto.Message
		want string // 期望的完整违规串（Violation 按 "; " 连接）
	}{
		// ---- A 组 structure ----
		{"CreateAppRequest empty name reports dns label", &structurev1.CreateAppRequest{ProjectId: "p"}, "name: " + dnsLabelText},
		{"CreateAppRequest empty project_id reports required", &structurev1.CreateAppRequest{Name: "web"}, "project_id: " + reqMsg},
		{"ListAppsRequest empty project_id", &structurev1.ListAppsRequest{}, "project_id: " + reqMsg},
		{"DeleteAppRequest empty id", &structurev1.DeleteAppRequest{}, "id: " + reqMsg},
		{"PutSecretRequest empty fields", &structurev1.PutSecretRequest{},
			perField("project_id: "+reqMsg, "name: "+reqMsg)}, // 原 "project_id and name: must not be empty" 拆分
		{"PutSecretRequest dot-dot name", &structurev1.PutSecretRequest{ProjectId: "p", Name: "a..b"}, "name: " + secretNameText},
		{"PutConfigRequest empty fields", &structurev1.PutConfigRequest{},
			perField("project_id: "+reqMsg, "name: "+reqMsg)}, // 原 "project_id and name: must not be empty" 拆分
		{"PutConfigRequest oversized content", &structurev1.PutConfigRequest{ProjectId: "p", Name: "n", Content: strings.Repeat("a", 262145)},
			"content: exceeds the 262144-byte per-config limit"},
		{"CreateVolumeRequest empty name", &structurev1.CreateVolumeRequest{ProjectId: "p"}, "name: " + dnsLabelText},
		{"CreateVolumeRequest empty project_id", &structurev1.CreateVolumeRequest{Name: "data"}, "project_id: " + reqMsg},
		{"DeleteVolumeRequest empty id", &structurev1.DeleteVolumeRequest{}, "id: " + reqMsg},

		// ---- B 组 variables ----
		{"PutSharedVariableRequest empty fields", &structurev1.PutSharedVariableRequest{},
			perField("project_id: "+reqMsg, "name: "+reqMsg)}, // 原 "project_id and name: must not be empty" 拆分
		{"PutSharedVariableRequest bad env key", &structurev1.PutSharedVariableRequest{ProjectId: "p", Name: "BAD-KEY"}, "name: " + envNameText},
		{"PutSharedVariableRequest oversized value", &structurev1.PutSharedVariableRequest{ProjectId: "p", Name: "OK", Value: strings.Repeat("a", 16385)},
			"value: exceeds the 16384-byte per-variable limit"},
		{"DeleteSharedVariableRequest empty fields", &structurev1.DeleteSharedVariableRequest{},
			perField("project_id: "+reqMsg, "name: "+reqMsg)}, // 原 "project_id and name: must not be empty" 拆分
		{"PutAppVariableRequest empty fields", &structurev1.PutAppVariableRequest{},
			perField("app_id: "+reqMsg, "name: "+reqMsg)}, // 原 "app_id and name: must not be empty" 拆分
		{"PutAppVariableRequest bad env key", &structurev1.PutAppVariableRequest{AppId: "a", Name: "bad-name"}, "name: " + envNameText},
		{"PutAppVariableRequest oversized value", &structurev1.PutAppVariableRequest{AppId: "a", Name: "OK", Value: strings.Repeat("a", 16385)},
			"value: exceeds the 16384-byte per-variable limit"},
		{"ListAppVariablesRequest empty app_id", &structurev1.ListAppVariablesRequest{}, "app_id: " + reqMsg},
		{"DeleteAppVariableRequest empty fields", &structurev1.DeleteAppVariableRequest{},
			perField("app_id: "+reqMsg, "name: "+reqMsg)}, // 原 "app_id and name: must not be empty" 拆分

		// ---- C 组 identity ----
		{"CreateUserRequest empty name reports dns label", &identityv1.CreateUserRequest{RoleId: "r"}, "name: " + dnsLabelText},
		{"CreateUserRequest empty role_id", &identityv1.CreateUserRequest{Name: "web"},
			"role_id: must not be empty (grant a role within the team)"},
		{"LoginRequest empty fields", &identityv1.LoginRequest{},
			perField("name: "+reqMsg, "password: "+reqMsg)}, // 原 "name and password: must not be empty" 拆分
		{"SetUserPasswordRequest empty password", &identityv1.SetUserPasswordRequest{UserId: "u"}, "password: " + reqMsg},
		{"CreateTeamRequest empty name reports dns label", &identityv1.CreateTeamRequest{}, "name: " + dnsLabelText},
		{"CreateRoleRequest empty scopes", &identityv1.CreateRoleRequest{Name: "deployer"},
			"scopes: must not be empty (a role is a named scope set)"},
		{"CreateRoleRequest empty name and scopes", &identityv1.CreateRoleRequest{},
			perField("name: "+dnsLabelText, "scopes: must not be empty (a role is a named scope set)")},
		{"CreateTokenRequest empty name", &identityv1.CreateTokenRequest{RoleId: "r"}, "name: " + reqMsg},
		{"CreateTokenRequest empty role_id", &identityv1.CreateTokenRequest{Name: "ci"},
			"role_id: must not be empty (tokens get scopes via a team role)"},

		// ---- D 组 invitations ----
		{"CreateInvitationRequest empty role_id", &identityv1.CreateInvitationRequest{}, "role_id: " + reqMsg},
		{"AcceptInvitationRequest empty fields", &identityv1.AcceptInvitationRequest{},
			perField("secret: "+reqMsg, "user_name: "+reqMsg)}, // 原两条独立检查合并为一串（语义同拆分）

		// ---- E 组 governance ----
		{"SetChangeFreezeRequest empty reason", &systemv1.SetChangeFreezeRequest{},
			"reason: must not be empty (the refusal carries it to callers)"},

		// ---- F 组 templates ----
		{"GetTemplateRequest empty name", &deliveryv1.GetTemplateRequest{}, "name: " + reqMsg},
		{"InstantiateTemplateRequest empty fields", &deliveryv1.InstantiateTemplateRequest{},
			perField("project_id: "+reqMsg, "template: "+reqMsg, "app_name: "+reqMsg)}, // 原 "project_id, template and app_name: must not be empty" 拆分
		{"InstantiateTemplateRequest bad app_name", &deliveryv1.InstantiateTemplateRequest{ProjectId: "p", Template: "nginx", AppName: "Bad_Name"},
			"app_name: " + dnsLabelText},

		// ---- G 组 registry ----
		{"ListRegistryCatalogRequest empty project_id", &deliveryv1.ListRegistryCatalogRequest{}, "project_id: " + reqMsg},
		{"ListRegistryTagsRequest empty fields", &deliveryv1.ListRegistryTagsRequest{},
			perField("project_id: "+reqMsg, "repository: "+reqMsg)}, // 原 "project_id and repository: must not be empty" 拆分

		// ---- H 组 networks ----
		{"CreateNetworkRequest empty name", &structurev1.CreateNetworkRequest{ProjectId: "p"}, "name: " + dnsLabelText},
		{"CreateNetworkRequest empty project_id", &structurev1.CreateNetworkRequest{Name: "bus"}, "project_id: " + reqMsg},
		{"RebuildNetworkRequest empty fields", &structurev1.RebuildNetworkRequest{},
			perField("project_id: "+reqMsg, "name: "+reqMsg)}, // 原 "project_id and name: must not be empty" 拆分
		{"DeclareNetworkPeerRequest empty fields", &structurev1.DeclareNetworkPeerRequest{},
			perField("network_id: "+reqMsg, "peer_project_id: "+reqMsg)}, // 原 "network_id and peer_project_id: must not be empty" 拆分

		// ---- I 组 databases ----
		{"CreateDatabaseRequest empty project_id", &structurev1.CreateDatabaseRequest{Name: "pg", Engine: "postgres"}, "project_id: " + reqMsg},
		{"CreateDatabaseRequest empty name reports dns label", &structurev1.CreateDatabaseRequest{ProjectId: "p", Engine: "postgres"},
			"name: " + dnsLabelText},
		{"CreateDatabaseRequest empty engine", &structurev1.CreateDatabaseRequest{ProjectId: "p", Name: "pg"}, "engine: " + reqMsg},
		{"ListDatabasesRequest empty project_id", &structurev1.ListDatabasesRequest{}, "project_id: " + reqMsg},
		{"DeleteDatabaseRequest empty id", &structurev1.DeleteDatabaseRequest{}, "id: " + reqMsg},
		{"RotateDatabasePasswordRequest empty database_id", &structurev1.RotateDatabasePasswordRequest{}, "database_id: " + reqMsg},
		{"TriggerBackupRequest empty database_id", &structurev1.TriggerBackupRequest{}, "database_id: " + reqMsg},
		{"ListBackupsRequest empty database_id", &structurev1.ListBackupsRequest{}, "database_id: " + reqMsg},
		{"VerifyBackupRequest empty backup_id", &structurev1.VerifyBackupRequest{}, "backup_id: " + reqMsg},
		{"BrowseDatabaseRequest empty database_id", &structurev1.BrowseDatabaseRequest{}, "database_id: " + reqMsg},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(tc.msg)
			require.Error(t, err, "坏实例必须被 descriptor 注解拒绝")
			// 未注册码不可能（注册表只增，E_INVALID_ARGUMENT 在册），
			// 直接以类型断言还原。
			var e *apperr.Error
			require.ErrorAs(t, err, &e)
			assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
			assert.Equal(t, tc.want, e.Message())
		})
	}
}

// TestValidateRetiredFamilyPositiveMatrix 钉合法输入不受注解误伤：边界值
// （上限恰好达界、字符集边缘字符）必须放行。
func TestValidateRetiredFamilyPositiveMatrix(t *testing.T) {
	v, err := apivalidate.NewValidator()
	require.NoError(t, err)

	assert.NoError(t, v.Validate(&structurev1.CreateAppRequest{ProjectId: "p", Name: "nightly-backup-42"}))
	assert.NoError(t, v.Validate(&structurev1.PutSecretRequest{ProjectId: "p", Name: "api.key-1", Value: "v"}))
	assert.NoError(t, v.Validate(&structurev1.PutConfigRequest{ProjectId: "p", Name: "n", Content: strings.Repeat("a", 262144)}))
	assert.NoError(t, v.Validate(&structurev1.PutSharedVariableRequest{ProjectId: "p", Name: "OK_KEY", Value: strings.Repeat("a", 16384)}))
	assert.NoError(t, v.Validate(&structurev1.PutAppVariableRequest{AppId: "a", Name: "_OK", Value: "v"}))
	assert.NoError(t, v.Validate(&identityv1.CreateUserRequest{Name: "web", RoleId: "r"}))
	assert.NoError(t, v.Validate(&identityv1.CreateRoleRequest{Name: "deployer", Scopes: []string{"projects:read"}}))
	assert.NoError(t, v.Validate(&deliveryv1.InstantiateTemplateRequest{ProjectId: "p", Template: "nginx", AppName: "web"}))
	assert.NoError(t, v.Validate(&structurev1.CreateDatabaseRequest{ProjectId: "p", Name: "pg", Engine: "postgres"}))
	assert.NoError(t, v.Validate(&structurev1.CreateNetworkRequest{ProjectId: "p", Name: "bus"}))
}
