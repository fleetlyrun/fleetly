package apitest

// FakeRuntime 的 RuntimeUtility 假面（ADR-0097 卷源标记门）：状态字段挂
// FakeRuntime（harness.go 的结构性拆分——sizeguard 文件预算）。

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// utilReqs/utilMarker 是 RunUtility 假面状态（卷源标记门 ADR-0097）：
// 调用流水 + 标记回读覆写（字段组随本拆分文件）。
type utilityFakeState struct {
	mu      sync.Mutex
	reqs    []capability.UtilityRequest
	markers []byte
}

// RunUtility 实现 RuntimeUtility（ADR-0097 卷源标记门的假面）：标记脚本
// argv（claim 形态）回显内嵌的标记 JSON——database_id 与被确保库行同源，
// 门恒过；SetVolumeOriginMarker 注入异源/任意覆写（异源拒的测试面）。
// 其余 argv（备份/恢复/轮换未来面）零输出成功。
func (f *FakeRuntime) RunUtility(_ context.Context, req capability.UtilityRequest, stdout, _ io.Writer) error {
	f.utilState.mu.Lock()
	f.utilState.reqs = append(f.utilState.reqs, req)
	override := f.utilState.markers
	f.utilState.mu.Unlock()
	joined := strings.Join(req.Argv, " ")
	if !strings.Contains(joined, dbtemplate.InstanceMarkerFile) {
		return nil
	}
	if override != nil {
		_, _ = stdout.Write(override)
		return nil
	}
	// claim 形态：脚本内嵌 printf %s '<json>' ——回显该载荷（同源）。
	if i := strings.Index(joined, "printf %s '"); i >= 0 {
		rest := joined[i+len("printf %s '"):]
		if j := strings.Index(rest, "' >"); j >= 0 {
			_, _ = stdout.Write([]byte(rest[:j]))
		}
	}
	return nil
}

// SetVolumeOriginMarker 覆写卷源标记探测的回读（nil 清除；异源拒/同源
// 覆写的两态测试面）。
func (f *FakeRuntime) SetVolumeOriginMarker(marker []byte) {
	f.utilState.mu.Lock()
	f.utilState.markers = marker
	f.utilState.mu.Unlock()
}

// UtilityRequests 返回 RunUtility 调用快照（挂卷锚/argv 断言面）。
func (f *FakeRuntime) UtilityRequests() []capability.UtilityRequest {
	f.utilState.mu.Lock()
	defer f.utilState.mu.Unlock()
	out := make([]capability.UtilityRequest, len(f.utilState.reqs))
	copy(out, f.utilState.reqs)
	return out
}
