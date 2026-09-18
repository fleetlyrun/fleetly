package api

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timeTime 是 time.Time 的包内别名（读感统一）。
type timeTime = time.Time

// tstamp 是 time.Time → protobuf Timestamp 的统一包装（零值时间不投影
// ——调用方按字段可选语义留空）。
func tstamp(t timeTime) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
