package grpcapi

// ListLimit 归一 List limit（ADR-0026 after_* + limit 惯例的缺省）：
// n<=0 回落缺省 50；n>200 钳到上界 200 而非回落缺省——调用方明确要大页
// （500）时静默砍到 50 是对显式意图的覆盖（N1 C21），钳上界既尊重请求
// 又守住查询面。
func ListLimit(n int32) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return int(n)
}
