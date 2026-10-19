package k3s

// k3s 集中式回环中继的凭证来源（H10 §4.5/§4.7）：每节点连接在确定
// carrier（k8s 节点名）后经 NodeRelayOptions.Credential 回调取凭证——k3s
// 没有节点侧凭证，回调返回 engine 内部 token；node token 不再参与 relay
// 鉴权。本文件用最小 WS 服务端锚住 carrier 透传与 Bearer 头。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

func TestNodeRelayUsesCredentialCallback(t *testing.T) {
	type captured struct {
		auth  string
		hello string
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done") //nolint:errcheck // 测试收尾
		conn.SetReadLimit(1 << 20)
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		got <- captured{auth: r.Header.Get("Authorization"), hello: string(data)}
	}))
	defer srv.Close()

	p, _ := newFakeProvider()
	var callbackCarrier string
	err := p.nodeRelayOnce(context.Background(), capability.NodeRelayOptions{
		GatewayURL: srv.URL,
		Version:    "v-test",
		Credential: func(_ context.Context, carrierNodeID string) (string, error) {
			callbackCarrier = carrierNodeID
			return "fltrelay_k3s_internal", nil
		},
	}, "k3s-node-1")
	require.Error(t, err, "the server closes after hello, so the read loop ends with an error")

	gotMsg := <-got
	assert.Equal(t, "k3s-node-1", callbackCarrier,
		"the credential callback must receive the carrier node name")
	assert.Equal(t, "Bearer fltrelay_k3s_internal", gotMsg.auth,
		"the dial must carry the credential returned by the callback")
	var hello capability.RelayHello
	require.NoError(t, json.Unmarshal([]byte(gotMsg.hello), &hello))
	assert.Equal(t, "hello", hello.Type)
	assert.Equal(t, "k3s-node-1", hello.CarrierNodeID)
	assert.Equal(t, "v-test", hello.RelayVersion)

	// 无回调 = 无凭证来源，诚实失败（不回落到 node token 文件）。
	err = p.nodeRelayOnce(context.Background(), capability.NodeRelayOptions{
		GatewayURL: srv.URL,
	}, "k3s-node-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential callback is required")
}
