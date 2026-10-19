package engine

// 构建输入登记的终态释放（R03-M1，S16-c8）：build.inputs 持有
// BuildRequest——含推送凭证明文（RegistryCredential.Secret）。终态
//（succeeded/failed/expired/cancelled）必须清登记：否则进程生命期内凭证
// 常驻内存，且表只增不清（每次构建一行）。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

func TestBuildInputsReleasedOnEveryTerminal(t *testing.T) {
	cases := []struct {
		name     string
		from, to build.State
	}{
		{"succeeded", build.StateBuilding, build.StateSucceeded},
		{"failed", build.StateBuilding, build.StateFailed},
		{"expired", build.StateBuilding, build.StateExpired},
		{"cancelled", build.StateQueued, build.StateCancelled},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newBuildEngine(t, &fakeBuilder{digest: "sha256:x"})
			ctx := context.Background()
			b := &build.Build{
				ID: fmt.Sprintf("01JD0BUILD00000000000000%02d", i), AppID: tAppID,
				RevisionID: "01JD0REV0000000000000000B1", State: tc.from,
			}
			require.NoError(t, e.repos.Builds.Create(ctx, b))
			// 登记面按真实形态布置：推送凭证是明文敏感值。
			e.build.inputs[b.ID] = capability.BuildRequest{
				BuildID: b.ID, Builder: specir.BuilderDockerfile, ContextDir: "/tmp/ctx",
				PushCred: &capability.RegistryCredential{Server: "registry.test", Username: "u", Secret: "s3cret"},
			}

			_, err := e.transitBuild(ctx, b, []build.State{tc.from}, tc.to, nil)
			require.NoError(t, err)

			e.build.inputsMu.Lock()
			_, present := e.build.inputs[b.ID]
			e.build.inputsMu.Unlock()
			assert.False(t, present,
				"terminal build (%s) must release its input registration: the map carries push credentials and only grows", tc.to)
		})
	}
}
