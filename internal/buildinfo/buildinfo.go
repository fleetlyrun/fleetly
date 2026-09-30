// Package buildinfo 承载编译期注入的构建信息（ldflags -X 填充 package 级
// var，由各二进制 main 持有、经 wire provider 收敛到此类型）。
package buildinfo

// BuildInfo 是版本三元组；未注入时（go run / 测试）字段为零值，展示层
// 自行兜底为 dev 形态。
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// IsZero 报告是否完全没有注入版本信息。
func (b BuildInfo) IsZero() bool {
	return b.Version == "" && b.Commit == "" && b.Date == ""
}
