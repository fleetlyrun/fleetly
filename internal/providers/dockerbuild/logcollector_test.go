package dockerbuild

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// logCollector 收集日志帧（测试断言实时流用）。
type logCollector struct{ lines *[]string }

func (c *logCollector) WriteLog(_ context.Context, f capability.LogFrame) error {
	*c.lines = append(*c.lines, string(f.Line))
	return nil
}
