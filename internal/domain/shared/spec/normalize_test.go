package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageDeployNormalizes(t *testing.T) {
	spec, err := ImageDeploy("app-1", "prj-1", "nginx:1.27", "", nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(SchemaVersion), spec.GetSchemaVersion())
	assert.Equal(t, "app-1", spec.GetApp().GetId())
	assert.Equal(t, "nginx:1.27", spec.GetSource().GetImage().GetRef())
	require.Len(t, spec.GetProcesses(), 1)
	assert.Equal(t, "web", spec.GetProcesses()[0].GetName())
	assert.Equal(t, "nginx:1.27", spec.GetProcesses()[0].GetImage())
}
