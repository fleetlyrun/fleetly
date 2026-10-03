package builders

// pushBuiltImage 的 hermetic 表测（2026-10-03 架构评审候选 6）：经
// imagePush/imageInspect 函数值 seam 直达推送路径——digest 回退链
//（aux 优先/RepoDigests 兜底/全无即终态错）、错误映射、凭证透传、推送
// 流文本行进构建日志。此前该面只有 FLEETLY_TEST_DOCKER=1 真机可测。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"testing"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakePushResponse 是 ImagePushResponse 的假面：JSONMessages 回放预制
// 消息（stream 行 / aux digest / 错误详情），序列末尾可选上抛错误。
type fakePushResponse struct {
	msgs []jsonstream.Message
	err  error
}

func (f *fakePushResponse) Read([]byte) (int, error) { return 0, io.EOF }
func (f *fakePushResponse) Close() error             { return nil }
func (f *fakePushResponse) Wait(context.Context) error {
	return nil
}
func (f *fakePushResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {
		for _, m := range f.msgs {
			if !yield(m, nil) {
				return
			}
		}
		if f.err != nil {
			yield(jsonstream.Message{}, f.err)
		}
	}
}

// auxDigestMsg 构造一条携带 manifest digest 的 aux 消息。
func auxDigestMsg(digest string) jsonstream.Message {
	raw := json.RawMessage(`{"Digest":"` + digest + `"}`)
	return jsonstream.Message{Aux: &raw}
}

// newPushFixture 建注入双 seam 的 daemonClients（cli/bk 为 nil——推送
// 路径不触碰）。
func newPushFixture(push fakePushResponse, inspect client.ImageInspectResult, inspectErr error) (*daemonClients, *[]string) {
	var lines []string
	return &daemonClients{
		imagePush: func(_ context.Context, _ string, _ string) (client.ImagePushResponse, error) {
			return &push, nil
		},
		imageInspect: func(context.Context, string) (client.ImageInspectResult, error) {
			return inspect, inspectErr
		},
	}, &lines
}

func TestPushBuiltImageAuxDigestWins(t *testing.T) {
	ctx := context.Background()
	d, lines := newPushFixture(fakePushResponse{msgs: []jsonstream.Message{
		{Stream: "The push refers to repository [reg:5000/team/app]\n"},
		auxDigestMsg("sha256:first"),
		auxDigestMsg("sha256:final"),
		{Stream: "v1: digest sha256:final size: 528\n"},
	}}, client.ImageInspectResult{}, nil)

	dg, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1", Target: "reg:5000/team/app:v1"},
		"reg:5000/team/app:v1", &logCollector{lines: lines})
	require.NoError(t, err)
	assert.Equal(t, "sha256:final", dg, "last aux frame wins")
	assert.Equal(t, []string{
		"The push refers to repository [reg:5000/team/app]",
		"v1: digest sha256:final size: 528",
	}, *lines, "push stream lines join the build log stream (trimmed)")
}

func TestPushBuiltImageStreamErrorSurfaces(t *testing.T) {
	ctx := context.Background()
	d, lines := newPushFixture(fakePushResponse{msgs: []jsonstream.Message{
		{Stream: "pushing\n"},
		{Error: &jsonstream.Error{Code: 1, Message: "registry returned error: denied"}},
	}}, client.ImageInspectResult{}, nil)

	_, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"}, "reg:5000/team/app:v1",
		&logCollector{lines: lines})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "denied", "stream error detail must surface")
}

func TestPushBuiltImageIterationErrorWrapped(t *testing.T) {
	ctx := context.Background()
	d, lines := newPushFixture(fakePushResponse{
		err: errors.New("connection reset"),
	}, client.ImageInspectResult{}, nil)

	_, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"}, "reg:5000/team/app:v1",
		&logCollector{lines: lines})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "push reg:5000/team/app:v1", "iteration errors carry the target")

	// seam 层 ImagePush 调用失败同款包装。
	d2, lines2 := newPushFixture(fakePushResponse{}, client.ImageInspectResult{}, nil)
	d2.imagePush = func(context.Context, string, string) (client.ImagePushResponse, error) {
		return nil, errors.New("dial refused")
	}
	_, err = d2.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"}, "reg:5000/team/app:v1",
		&logCollector{lines: lines2})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dial refused")
}

func TestPushBuiltImageRepoDigestsFallback(t *testing.T) {
	ctx := context.Background()
	// 无 aux → RepoDigests 兜底：tag 与 host:port 冒号区分（targetRepoPrefix
	// 锚），非本 repo 的条目不误配。
	d, lines := newPushFixture(fakePushResponse{msgs: []jsonstream.Message{
		{Stream: "pushed\n"},
	}}, client.ImageInspectResult{InspectResponse: image.InspectResponse{
		RepoDigests: []string{
			"other/app@sha256:not-this-one",
			"reg:5000/team/app@sha256:fallback-digest",
		},
	}}, nil)

	dg, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"}, "reg:5000/team/app:v1",
		&logCollector{lines: lines})
	require.NoError(t, err)
	assert.Equal(t, "sha256:fallback-digest", dg)
}

func TestPushBuiltImageInspectErrorWrapped(t *testing.T) {
	ctx := context.Background()
	d, lines := newPushFixture(fakePushResponse{msgs: []jsonstream.Message{{Stream: "pushed\n"}}},
		client.ImageInspectResult{}, errors.New("daemon gone"))

	_, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"}, "reg:5000/team/app:v1",
		&logCollector{lines: lines})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inspect pushed image", "fallback failure is explicit")
}

func TestPushBuiltImageNoDigestIsTerminal(t *testing.T) {
	ctx := context.Background()
	d, lines := newPushFixture(fakePushResponse{msgs: []jsonstream.Message{{Stream: "pushed\n"}}},
		client.ImageInspectResult{}, nil) // 兜底也无匹配条目

	_, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1"}, "reg:5000/team/app:v1",
		&logCollector{lines: lines})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no manifest digest reported", "terminal error is unmistakable")
}

func TestPushBuiltImageCredentialPassesSeam(t *testing.T) {
	ctx := context.Background()
	cred := capability.RegistryCredential{Server: "reg:5000", Username: "fleetly", Secret: "pw"}
	var authSeen string
	var lines []string
	d := &daemonClients{
		imagePush: func(_ context.Context, _ string, registryAuth string) (client.ImagePushResponse, error) {
			authSeen = registryAuth
			return &fakePushResponse{msgs: []jsonstream.Message{auxDigestMsg("sha256:x")}}, nil
		},
	}
	dg, err := d.pushBuiltImage(ctx, capability.BuildRequest{BuildID: "b1", PushCred: &cred},
		"reg:5000/team/app:v1", &logCollector{lines: &lines})
	require.NoError(t, err)
	assert.Equal(t, "sha256:x", dg)

	// 凭证以 X-Registry-Auth 编码形态透传（encodeRegistryAuth 同一编码）。
	raw, derr := base64.StdEncoding.DecodeString(authSeen)
	require.NoError(t, derr)
	assert.Contains(t, string(raw), `"username":"fleetly"`)
	assert.Contains(t, string(raw), `"serveraddress":"reg:5000"`)
}
