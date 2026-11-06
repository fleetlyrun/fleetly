package dataservices

// 凭证轮换测试（IA v3 二期⑤b 验收锚）：数据面方言（postgres——utility
// 改密 + Secret 重写 + 事件）、声明式方言（redis——零 utility，Secret
// 重写即生效通道）、拒绝面（utility 失败零状态变更 + 未知行 404）。P5
// wave-4 随域拆包自 engine 包迁入，断言面零漂移。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// rotateCapturedPassword 从 postgres 改密 argv 尾段提取目标密码（argv 是
// 平台自铸契约——单引号字面值段，字符集闸保证可安全切分）。
func rotateCapturedPassword(t *testing.T, reqs []capability.UtilityRequest) string {
	t.Helper()
	require.Len(t, reqs, 1)
	argv := reqs[0].Argv
	require.NotEmpty(t, argv)
	sqlText := argv[len(argv)-1]
	const marker = "WITH PASSWORD '"
	i := strings.Index(sqlText, marker)
	require.GreaterOrEqual(t, i, 0, "argv tail must carry the ALTER USER statement: %q", sqlText)
	rest := sqlText[i+len(marker):]
	j := strings.Index(rest, "'")
	require.GreaterOrEqual(t, j, 0)
	return rest[:j]
}

// secretPassword 解密凭证 Secret 回读密码（轮换后的单真源读面）。
func secretPassword(t *testing.T, fx *fixture, secName string) string {
	t.Helper()
	sec, err := fx.stores.Secrets.GetByName(context.Background(), tProjectID, secName)
	require.NoError(t, err)
	plain, err := fx.cipher.Open(sec.Ciphertext)
	require.NoError(t, err)
	password, err := dbtemplate.PasswordFromURL(string(plain))
	require.NoError(t, err)
	return password
}

// utilityOf 返回夹具的 utility 假面（newFixture(withUtility) 装配面）。
func utilityOf(t *testing.T, fx *fixture) *fakeUtility {
	t.Helper()
	rt, ok := fx.d.runtime.(runtimeWithUtility)
	require.True(t, ok, "fixture must carry the utility face")
	return rt.fakeUtility
}

func TestRotateDatabasePasswordDataPlane(t *testing.T) {
	oldURL := fmt.Sprintf("postgresql://fleetly:%s@db-%s:5432/fleetly", strings.Repeat("a", 48), strings.ToLower(tDatabaseID))
	fx := newFixture(t, "postgres", oldURL, true)
	ut := utilityOf(t, fx)
	ctx := context.Background()

	require.NoError(t, fx.d.RotateDatabasePassword(ctx, tDatabaseID))

	// utility 恰一次：ALTER USER 数据面 + pgpass 认证材料（current）。
	reqs := ut.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "rotate-"+tDatabaseID, reqs[0].ID)
	assert.Equal(t, tDatabaseID, reqs[0].Namespace.Database)
	tail := reqs[0].Argv[len(reqs[0].Argv)-1]
	assert.Contains(t, tail, "ALTER USER fleetly WITH PASSWORD '")
	assert.Contains(t, reqs[0].Env, "PGPASSFILE")
	next := rotateCapturedPassword(t, reqs)
	assert.NotEmpty(t, reqs[0].SecretFiles)

	// Secret 已重写为 next（单真源换值；旧值不再可读）。
	assert.Equal(t, next, secretPassword(t, fx, engineports.DBCredentialSecretName(tDatabaseNm)))
	assert.NotEqual(t, strings.Repeat("a", 48), next)
	// 事件与审计由 API 受理位落账（DeleteDatabase 同款序）——engine 侧
	// 只负责变更本体与 Kick。
}

func TestRotateDatabasePasswordDeclarativeRedis(t *testing.T) {
	oldURL := fmt.Sprintf("redis://:%s@db-%s:6379/0", strings.Repeat("b", 48), strings.ToLower(tDatabaseID))
	fx := newFixture(t, "redis", oldURL, true)
	ut := utilityOf(t, fx)

	require.NoError(t, fx.d.RotateDatabasePassword(context.Background(), tDatabaseID))

	// 声明式：零 utility（重下发通道生效），Secret 已换新值。
	assert.Empty(t, ut.requests())
	next := secretPassword(t, fx, engineports.DBCredentialSecretName(tDatabaseNm))
	assert.Len(t, next, 48)
	assert.NotEqual(t, strings.Repeat("b", 48), next)
}

func TestRotateDatabasePasswordRejectedLeavesState(t *testing.T) {
	oldURL := fmt.Sprintf("postgresql://fleetly:%s@db-%s:5432/fleetly", strings.Repeat("c", 48), strings.ToLower(tDatabaseID))
	fx := newFixture(t, "postgres", oldURL, true)
	ut := utilityOf(t, fx)
	ut.execErr = errors.New("psql: FATAL: password authentication failed")

	err := fx.d.RotateDatabasePassword(context.Background(), tDatabaseID)

	// 拒绝面：哨兵错误可辨（API map 精确码）；零状态变更（Secret 保持旧值
	// ——utility 先行序的诚实语义，重试安全）。
	require.ErrorIs(t, err, ErrRotateRejected)
	assert.Contains(t, err.Error(), "boom-detail")
	assert.Len(t, ut.requests(), 1)
	sec, serr := fx.stores.Secrets.GetByName(context.Background(), tProjectID, engineports.DBCredentialSecretName(tDatabaseNm))
	require.NoError(t, serr)
	plain, perr := fx.cipher.Open(sec.Ciphertext)
	require.NoError(t, perr)
	assert.Equal(t, oldURL, string(plain), "a rejected rotation must not touch the credential secret")
}

func TestRotateDatabasePasswordUnknownRow(t *testing.T) {
	fx := newFixture(t, "postgres", "postgresql://fleetly:x@db-x:5432/fleetly", true) //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	err := fx.d.RotateDatabasePassword(context.Background(), "01JD0BKPMISSING000000000001")
	require.ErrorIs(t, err, state.ErrNotFound)
}

func TestRotateDatabasePasswordSerializes(t *testing.T) {
	// 单飞语义冒烟：并发轮换不 panic、终态 Secret 可解密（全域互斥的
	// 存在性验证——时序断言交给锁本身）。
	oldURL := "postgresql://fleetly:x@db-x:5432/fleetly" //nolint:gosec // G101 误报：测试夹具占位串
	fx := newFixture(t, "postgres", oldURL, true)
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- fx.d.RotateDatabasePassword(context.Background(), tDatabaseID) }()
	}
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	assert.Len(t, secretPassword(t, fx, engineports.DBCredentialSecretName(tDatabaseNm)), 48)
}
