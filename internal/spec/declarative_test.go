package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// ParseSpecFile（F3.5 第四源）：AppRef 权威覆写（文件内声明恒被覆盖）+
// schema_version 缺省当前版 + 未知字段 fail-closed 拒 + 跳代拒。
func TestParseSpecFile(t *testing.T) {
	body := `{"app":{"id":"WRONG","project":"WRONG"},"source":{"image":{"ref":"nginx:1.27"}},` +
		`"processes":[{"name":"web","image":"nginx:1.27","ports":[{"port":8080,"protocol":"PROTOCOL_H2C"}]}]}`
	s, err := ParseSpecFile([]byte(body), "real-app", "real-prj")
	require.NoError(t, err)
	assert.Equal(t, "real-app", s.GetApp().GetId())
	assert.Equal(t, "real-prj", s.GetApp().GetProject())
	assert.Equal(t, int32(SchemaVersion), s.GetSchemaVersion(), "absent schema_version defaults to current")
	assert.Equal(t, specv1.Protocol_PROTOCOL_H2C, s.GetProcesses()[0].GetPorts()[0].GetProtocol())

	_, err = ParseSpecFile([]byte(`{"not_a_field":true}`), "a", "p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid AppSpec JSON")

	_, err = ParseSpecFile([]byte(`{"schema_version":99}`), "a", "p")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported schema version")
}
