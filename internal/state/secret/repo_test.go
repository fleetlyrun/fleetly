package secret_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

func newSecret(id, projectID, name string) *secret.Secret {
	return &secret.Secret{
		ID: id, ProjectID: projectID, Name: name,
		Ciphertext: []byte("age-envelope"), Fingerprint: "0123456789abcdef",
	}
}

// ADR-0026 List 分页（after_* + limit）：name 字典序升序（既有排序轴不变）、
// after_name 游标跳过、limit 截断与钳制（<=0 或 >200 回落/钳缺省 50）；
// 分页只动行集，每行仍是最新指纹面。
func TestSecretListFingerprintsPagination(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	secrets := secret.New(clock)

	const projA = "01JD0PROJ0000000000000000A"
	const projB = "01JD0PROJ0000000000000000B"
	// 字典序与落序交错落三行（projA）+ 异项目一行。
	names := []string{"api-token", "db-url", "api-token-json"}
	for i, n := range names {
		require.NoError(t, secrets.Upsert(ctx, db.Runner(), newSecret(
			"01JD0SECR0000000000000000"+string(rune('0'+i)), projA, n)))
	}
	require.NoError(t, secrets.Upsert(ctx, db.Runner(), newSecret(
		"01JD0SECR00000000000000009", projB, "other")))

	// 升序首页截断：api-token < api-token-json < db-url。
	page1, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "", 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.Equal(t, "api-token", page1[0].Name)
	assert.Equal(t, "api-token-json", page1[1].Name)

	// 游标跳过首页。
	page2, err := secrets.ListFingerprints(ctx, db.Runner(), projA, page1[len(page1)-1].Name, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	assert.Equal(t, "db-url", page2[0].Name)

	// 游标越过末条 → 空页；tombstone 不入指纹面。
	page3, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "zzz", 2)
	require.NoError(t, err)
	assert.Empty(t, page3)
	require.NoError(t, secrets.SoftDelete(ctx, db.Runner(), projA, "db-url"))
	got, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "", 0)
	require.NoError(t, err)
	assert.Len(t, got, 2, "deleted rows stay hidden")

	// limit 钳制：<=0 回落缺省 50（两行全回），>200 钳上界（不截断小夹具）。
	for _, limit := range []int{0, -2, 250} {
		got, err := secrets.ListFingerprints(ctx, db.Runner(), projA, "", limit)
		require.NoError(t, err)
		assert.Len(t, got, 2, "limit %d clamps into range and returns all rows", limit)
	}
}
