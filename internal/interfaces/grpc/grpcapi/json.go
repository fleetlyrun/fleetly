package grpcapi

import "encoding/json"

// MustJSON 是事件/审计载荷的确定性序列化（结构体字段恒可序列化；失败
// 回退空对象，绝不因日志/事件载荷让业务路径失败）。
func MustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
