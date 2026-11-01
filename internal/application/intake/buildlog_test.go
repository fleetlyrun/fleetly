package intake

// build 日志脱敏链白盒（P5 wave-5 拆包随域迁入，通则⑥——断言面零漂移；
// 原 engine logging_test 的 build 脱敏面。P11 验收锚 ADR-0040 决策 3）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

// fakeLogging 是 Logging 端口假底座（engine 侧同源隔离副本）：记录 Ingest
// 批次。
type fakeLogging struct {
	mu       sync.Mutex
	ingested []capability.LogFrame
	fail     bool
}

func (f *fakeLogging) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fakelogs", Capability: capability.KindLogging, Version: "test"}
}
func (f *fakeLogging) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeLogging) Ingest(_ context.Context, frames []capability.LogFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return assert.AnError
	}
	f.ingested = append(f.ingested, frames...)
	return nil
}
func (f *fakeLogging) Query(context.Context, capability.LogQuery, capability.LogWriter) error {
	return nil
}
func (f *fakeLogging) snapshot() []capability.LogFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.LogFrame, len(f.ingested))
	copy(out, f.ingested)
	return out
}

// frameLinesJoined 是帧行拼接（断言辅助）。
func frameLinesJoined(frames []capability.LogFrame) string {
	var b strings.Builder
	for _, f := range frames {
		b.Write(f.Line)
		b.WriteByte('\n')
	}
	return b.String()
}

// fingerprint8 是脱敏指纹短形态的独立复算（测试侧独立实现避免恒真断言）。
func fingerprint8(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "secret:" + hex.EncodeToString(sum[:])[:8]
}

// newBuildLogFixture 建可执行构建的白盒夹具（builder/logs/logging +
// project/app 行供域盖戳）。
func newBuildLogFixture(t *testing.T, fb *fakeBuilder, fl *fakeLogging) *fixture {
	t.Helper()
	fx := newFixture(t, func(d *Deps) {
		d.Builders = map[string]capability.Builder{specir.BuilderDockerfile: fb}
		if fl != nil {
			d.Logging = fl
		}
	})
	fx.seedProjectApp(t)
	return fx
}

// TestBuildLogRedactionSecretZeroOccurrence 是 P11 验收锚（ADR-0040 决策 3）：
// 注入已知 Secret（推送凭证 + 材料文件值）→ build log 全量帧断言原始值
// 零出现 + secret:<fp8> 指纹形态在场；VL 持久化批同样零出现。
func TestBuildLogRedactionSecretZeroOccurrence(t *testing.T) {
	const pushSecret = "push-secret-0123456789abcdef"
	const fileSecret = "file-secret-value-9876543210"
	fb := &fakeBuilder{digest: "sha256:built", logs: []string{
		"pushing with authorization " + pushSecret,
		"material reads " + fileSecret + " ok",
		"clean line stays untouched",
		pushSecret, // 整行即密值
	}}
	fl := &fakeLogging{}
	fx := newBuildLogFixture(t, fb, fl)
	ctx := context.Background()
	b := &build.Build{ID: "01JD0BUILD000000000000000C1", AppID: tAppID, RevisionID: "01JD0REV0000000000000000C1", State: build.StateBuilding}
	require.NoError(t, fx.repos.Builds.Create(ctx, b))
	fx.in.build.inputs[b.ID] = capability.BuildRequest{
		Builder:  specir.BuilderDockerfile,
		PushCred: &capability.RegistryCredential{Server: "reg:5000", Username: "u", Secret: pushSecret},
		SecretFiles: map[string][]byte{
			"env": []byte(fileSecret),
		},
	}

	fx.in.executeBuild(b)

	got, err := fx.repos.Builds.Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateSucceeded, got.State, "build must succeed with redaction in place")

	frames := fx.in.RecentBuildLogs(b.ID)
	require.NotEmpty(t, frames)
	fpPush := fingerprint8(pushSecret)
	fpFile := fingerprint8(fileSecret)
	for _, f := range frames {
		line := string(f.Line)
		assert.NotContains(t, line, pushSecret, "P11 anchor: raw push secret must never appear in build logs")
		assert.NotContains(t, line, fileSecret, "P11 anchor: raw file secret must never appear in build logs")
	}
	joined := frameLinesJoined(frames)
	assert.Contains(t, joined, fpPush)
	assert.Contains(t, joined, fpFile)
	assert.Contains(t, joined, "clean line stays untouched")

	// VL 持久化批：同脱敏 + 域盖戳（kind=build、Source=BuildID、team/project/app）。
	ingested := fl.snapshot()
	require.NotEmpty(t, ingested)
	for _, f := range ingested {
		assert.NotContains(t, string(f.Line), pushSecret)
		assert.NotContains(t, string(f.Line), fileSecret)
		assert.Equal(t, capability.LogKindBuild, f.Kind)
		assert.Equal(t, b.ID, f.Source)
		assert.Equal(t, tProjectID, f.Project)
		assert.Equal(t, tAppID, f.App)
		assert.NotEmpty(t, f.Team)
	}
}

// TestBuildLogRedactionWithoutLogging：Logging 面停用（nil）时出口链
// 仍脱敏（P11 与持久化解耦——脱敏是出口纪律不是存储特性）。
func TestBuildLogRedactionWithoutLogging(t *testing.T) {
	const pushSecret = "push-only-secret-abcdef012345"
	fb := &fakeBuilder{digest: "sha256:built", logs: []string{"auth " + pushSecret}}
	fx := newBuildLogFixture(t, fb, nil)
	ctx := context.Background()
	b := &build.Build{ID: "01JD0BUILD000000000000000C2", AppID: tAppID, RevisionID: "01JD0REV0000000000000000C2", State: build.StateBuilding}
	require.NoError(t, fx.repos.Builds.Create(ctx, b))
	fx.in.build.inputs[b.ID] = capability.BuildRequest{
		Builder:  specir.BuilderDockerfile,
		PushCred: &capability.RegistryCredential{Server: "reg:5000", Username: "u", Secret: pushSecret},
	}

	fx.in.executeBuild(b)
	for _, f := range fx.in.RecentBuildLogs(b.ID) {
		assert.NotContains(t, string(f.Line), pushSecret, "redaction must hold without the persistence face")
	}
}

// TestRedactTableOrdering 钉替换序：长值优先（短值是长值子串时不抢先）；
// 短值（<8B）不进表。
func TestRedactTableOrdering(t *testing.T) {
	long := "supersecretvalue-0123456789"
	short := "secretvalue" // 是 long 的子串且 ≥8B——必须后替换（否则 long 的匹配面被破坏）
	table := newRedactTable([]string{short, long, "tiny", ""})
	out := string(table.redact([]byte("x " + long + " y " + short + " z tiny")))
	assert.NotContains(t, out, long)
	assert.NotContains(t, out, short)
	assert.Contains(t, out, "x "+fingerprint8(long))
	assert.Contains(t, out, "z tiny", "short values below the threshold stay literal")
}
