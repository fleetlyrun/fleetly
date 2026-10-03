package swarm

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsNodeVersionRace（B14-3）：版本竞态文案匹配面——两种 swarmkit 稳
// 定文案都命中，其余错误不误判（重试只花在竞态上）。
func TestIsNodeVersionRace(t *testing.T) {
	for _, msg := range []string{
		"rpc error: code = Unknown desc = object was updated by another process",
		"update out of sequence",
		"Error response from daemon: update out of sequence",
	} {
		assert.True(t, isNodeVersionRace(errors.New(msg)), "version race text must match: %s", msg)
	}
	for _, err := range []error{
		nil,
		errors.New("node list: connection refused"),
		errors.New("swarm node X: permission denied"),
		context.Canceled,
	} {
		assert.False(t, isNodeVersionRace(err), "non-race error must not match: %v", err)
	}
}

// TestRetryNodeVersionRace（B14-3）：availability 更新撞版本竞态时重取节
// 点重试至多 3 次（mintNodeID 先例——被拒的更新未生效，新鲜版本上重放同
// 一语义写即正确），仍败原样上抛；非竞态错误不重试。
func TestRetryNodeVersionRace(t *testing.T) {
	raceErr := errors.New("object was updated by another process")
	node := func(index uint64) *swarm.Node {
		// Version 在内嵌 Meta 上（真机 inspect 形态）。
		return &swarm.Node{ID: "carrier-1", Meta: swarm.Meta{Version: swarm.Version{Index: index}}}
	}

	t.Run("race then success writes on the reloaded version", func(t *testing.T) {
		updates := 0
		var written swarm.Version
		err := retryNodeVersionRace(context.Background(),
			func(_ context.Context, n swarm.Node) error {
				updates++
				if updates == 1 {
					return raceErr
				}
				written = n.Version
				return nil
			},
			func(_ context.Context, id string) (swarm.Node, error) {
				require.Equal(t, "carrier-1", id)
				return swarm.Node{ID: id, Meta: swarm.Meta{Version: swarm.Version{Index: 7}}}, nil
			},
			node(1),
		)
		require.NoError(t, err)
		assert.Equal(t, 2, updates)
		assert.Equal(t, uint64(7), written.Index, "retry must write on the reloaded version, not the stale one")
	})

	t.Run("races exhausted return the raw error", func(t *testing.T) {
		updates := 0
		err := retryNodeVersionRace(context.Background(),
			func(context.Context, swarm.Node) error { updates++; return raceErr },
			func(_ context.Context, _ string) (swarm.Node, error) {
				//nolint:gosec // G115：updates 计数域内 [1,3]，无溢出面
				return swarm.Node{ID: "carrier-1", Meta: swarm.Meta{Version: swarm.Version{Index: uint64(updates)}}}, nil
			},
			node(1),
		)
		require.ErrorIs(t, err, raceErr, "exhausted retries surface the last error as-is")
		assert.Equal(t, 3, updates, "exactly maxAttempts updates are attempted")
	})

	t.Run("non-race error is not retried", func(t *testing.T) {
		denied := errors.New("swarm node X: permission denied")
		updates := 0
		err := retryNodeVersionRace(context.Background(),
			func(context.Context, swarm.Node) error { updates++; return denied },
			func(context.Context, string) (swarm.Node, error) {
				t.Fatal("reload must not run for a non-race error")
				return swarm.Node{}, nil
			},
			node(1),
		)
		require.ErrorIs(t, err, denied)
		assert.Equal(t, 1, updates)
	})

	t.Run("reload failure surfaces", func(t *testing.T) {
		boom := errors.New("inspect failed")
		err := retryNodeVersionRace(context.Background(),
			func(context.Context, swarm.Node) error { return raceErr },
			func(context.Context, string) (swarm.Node, error) { return swarm.Node{}, boom },
			node(1),
		)
		require.ErrorIs(t, err, boom)
		assert.Contains(t, err.Error(), "re-inspect node carrier-1")
	})

	t.Run("sequence conflict text also retried", func(t *testing.T) {
		updates := 0
		err := retryNodeVersionRace(context.Background(),
			func(_ context.Context, _ swarm.Node) error {
				updates++
				if updates == 1 {
					return errors.New("update out of sequence")
				}
				return nil
			},
			func(_ context.Context, _ string) (swarm.Node, error) { return swarm.Node{ID: "carrier-1"}, nil },
			node(1),
		)
		require.NoError(t, err)
		assert.Equal(t, 2, updates, "ErrSequenceConflict is the same race family and retried")
	})
}
