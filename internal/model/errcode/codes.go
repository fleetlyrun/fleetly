package errcode

import "google.golang.org/grpc/codes"

// builtins 是首发码集。纪律：只增不复用——新码必须带 Source 锚点并在
// 生产代码出现（usage 反扫），预留码进 usage_test.go 豁免清单并在此注明。
var builtins = []Code{
	{
		ID:         "E_INTERNAL",
		Summary:    "Internal server error.",
		Suggestion: "Check fleetlyd logs for the matching error_id. If the error persists, report it with the error_id attached.",
		Source:     "added during implementation",
		GRPC:       codes.Internal,
	},
}
