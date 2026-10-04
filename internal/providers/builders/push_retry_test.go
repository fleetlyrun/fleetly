package builders

// 推送 401 有界退避测试（ADR-0036 N2 兑现节 2：per-Project 凭证的受管
// 滚动可能晚于新项目首构建推送到达——staging 实录）。退避步长经 seam
// 注入缩窗，不真睡。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// unauthorizedMsg 是 docker 推送流 401 的真机形态锚（staging 实录文本）。
const unauthorizedMsg = "unexpected status from HEAD request to http://10.124.0.3:5000/v2/x/y/blobs/sha256:034d: 401 Unauthorized"

func TestPushUnauthorizedRetriesThenSucceeds(t *testing.T) {
	ctx := context.Background()
	calls := 0
	var lines []string
	d := &daemonClients{
		pushRetryBackoff: time.Millisecond,
		imagePush: func(context.Context, string, string) (client.ImagePushResponse, error) {
			calls++
			if calls <= 2 {
				return &fakePushResponse{msgs: []jsonstream.Message{
					{Error: &jsonstream.Error{Code: 1, Message: unauthorizedMsg}},
				}}, nil
			}
			return &fakePushResponse{msgs: []jsonstream.Message{
				auxDigestMsg("sha256:rolled-in"),
			}}, nil
		},
	}
	dg, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1", Target: "reg:5000/p/a:r1"},
		"reg:5000/p/a:r1", &logCollector{lines: &lines})
	require.NoError(t, err)
	assert.Equal(t, "sha256:rolled-in", dg)
	assert.Equal(t, 3, calls, "two unauthorized rounds then success")
	require.Equal(t, []string{
		"push rejected as unauthorized (attempt 1); waiting 1ms for the managed registry to roll the project credential",
		"push rejected as unauthorized (attempt 2); waiting 1ms for the managed registry to roll the project credential",
	}, trimAll(lines), "each retry states the wait in the build log")
}

func TestPushUnauthorizedBounded(t *testing.T) {
	ctx := context.Background()
	calls := 0
	var lines []string
	d := &daemonClients{
		pushRetryBackoff: time.Millisecond,
		imagePush: func(context.Context, string, string) (client.ImagePushResponse, error) {
			calls++
			return &fakePushResponse{msgs: []jsonstream.Message{
				{Error: &jsonstream.Error{Code: 1, Message: unauthorizedMsg}},
			}}, nil
		},
	}
	_, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"},
		"reg:5000/p/a:r1", &logCollector{lines: &lines})
	require.Error(t, err, "persisting 401 must still fail after the bounded window")
	assert.Equal(t, pushUnauthorizedRetries+1, calls, "first attempt plus the bounded retry budget")
	assert.Contains(t, err.Error(), unauthorizedMsg)
}

func TestPushNonUnauthorizedErrorsDoNotRetry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	var lines []string
	d := &daemonClients{
		pushRetryBackoff: time.Hour, // 若误重试，测试将超时红——非 401 必须直败
		imagePush: func(context.Context, string, string) (client.ImagePushResponse, error) {
			calls++
			return nil, errors.New("dial refused")
		},
	}
	_, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"},
		"reg:5000/p/a:r1", &logCollector{lines: &lines})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "non-401 errors fail on the first attempt")

	// 判定只认 unauthorized 文本：digest hex 含 "401" 不得误判（真机形态
	// 的 blob digest 常含该子串）。
	assert.False(t, isUnauthorized(errors.New("push sha256:aa401bb failed: connection reset")))
	assert.True(t, isUnauthorized(errors.New(unauthorizedMsg)))
	assert.False(t, isUnauthorized(nil))
}

// trimAll 逐行去空白（logCollector 保留原始行——注入的重试行自带换行，
// 推送流行经 pushOnce 修剪；对照时统一修剪）。
func trimAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSpace(l)
	}
	return out
}
