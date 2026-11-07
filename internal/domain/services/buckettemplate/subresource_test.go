package buckettemplate

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bucket 子资源方言面钉测（B8，ADR-0096 决策 10）：解析器夹具 = 2026-11-07
// live 实测原文（pgsty/silo RELEASE.2026-10-26 的 mcli quota info / du --json
// / 缺桶报文），argv 钉 MC_HOST_<alias> env 凭证面（argv 不载 SK）。

func siloFace(t *testing.T) BucketAdminFace {
	t.Helper()
	tpl, err := For("silo", "")
	require.NoError(t, err)
	face := BucketAdminFaceOf(tpl)
	require.NotNil(t, face)
	return face
}

func TestBucketInspectParse(t *testing.T) {
	face := siloFace(t)

	live, err := face.ParseBucketInspect("Bucket `probe-bucket` has hard quota of 954 MiB\n", "", nil)
	require.NoError(t, err)
	assert.True(t, live.Exists)
	require.NotNil(t, live.QuotaBytes)
	assert.Equal(t, int64(954*1024*1024), *live.QuotaBytes)

	live, err = face.ParseBucketInspect("Bucket `du-probe` has hard quota of 1.0 KiB\n", "", nil)
	require.NoError(t, err)
	require.NotNil(t, live.QuotaBytes)
	assert.Equal(t, int64(1024), *live.QuotaBytes)

	// 无配额形（live 原文双空格）。
	live, err = face.ParseBucketInspect("Bucket `noquota` has  quota of 0 B\n", "", nil)
	require.NoError(t, err)
	assert.True(t, live.Exists)
	assert.Nil(t, live.QuotaBytes)

	// 缺桶 = 缺席判据（live 原文）。
	live, err = face.ParseBucketInspect("", "mcli: <ERROR> Unable to get bucket quota. The specified bucket does not exist.", errors.New("exit 1"))
	require.NoError(t, err)
	assert.False(t, live.Exists)

	// 其余错误带报文首行。
	_, err = face.ParseBucketInspect("", "mcli: <ERROR> Access Denied.", errors.New("exit 1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Access Denied")
}

func TestBucketUsageParse(t *testing.T) {
	face := siloFace(t)
	live, err := face.ParseBucketUsage(`{"prefix":"du-probe","size":2048,"objects":3,"status":"success","isVersions":false}`+"\n", "", nil)
	require.NoError(t, err)
	assert.True(t, live.Exists)
	assert.Equal(t, int64(2048), live.Bytes)
	assert.Equal(t, int64(3), live.Objects)
}

func TestBucketArgvAndCredentialFace(t *testing.T) {
	face := siloFace(t)
	quota := int64(1073741824)
	set, err := face.SetBucketQuota("bkt-x", "fleetly", "sk0123456789", "media", &quota)
	require.NoError(t, err)
	assert.Equal(t, []string{"mcli", "quota", "set", "fleetlylocal/media", "--size", "1073741824"}, set.Argv)
	assert.Equal(t, "http://fleetly:sk0123456789@bkt-x:9000", set.Env["MC_HOST_fleetlylocal"], "root AK/SK ride the mcli alias env, never argv")
	assert.NotContains(t, strings.Join(set.Argv, " "), "sk0123456789")

	clear, err := face.SetBucketQuota("bkt-x", "fleetly", "sk0123456789", "media", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"mcli", "quota", "clear", "fleetlylocal/media"}, clear.Argv)

	rb, err := face.DeleteBucket("bkt-x", "fleetly", "sk0123456789", "media")
	require.NoError(t, err)
	assert.Equal(t, []string{"mcli", "rb", "--force", "fleetlylocal/media"}, rb.Argv, "deletion force-removes objects (wide-tier execution face)")

	mb, err := face.CreateBucket("bkt-x", "fleetly", "sk0123456789", "media")
	require.NoError(t, err)
	assert.Equal(t, []string{"mcli", "mb", "fleetlylocal/media"}, mb.Argv)
}

func TestValidateBucketName(t *testing.T) {
	assert.NoError(t, ValidateBucketName("media"))
	assert.NoError(t, ValidateBucketName("my.bucket-1"))
	assert.Error(t, ValidateBucketName("ab"), "S3 names are at least 3 characters (live: mcli refuses shorter)")
	assert.Error(t, ValidateBucketName("UPPER"))
	assert.Error(t, ValidateBucketName("with space"))
}
