package appgenprotoanchor

// 判据分辨力锚（appgenprotofreeze_test.go 防盲③）：合成样本，不是真实
// application 代码。三条 import 各钉判据一条路径——genproto 面必须命中、
// 标准库必须不命中、genproto 模块外的第三方必须不命中。改本文件须与
// TestApplicationGenprotoFreezeDiscrimination 同批。

import (
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// anchorSpec 证明 genproto import 被扫描器提取（类型引用形）。
func anchorSpec() *specv1.AppSpec { return &specv1.AppSpec{} }

// anchorRoundtrip 证明 genproto 模块外的 protobuf runtime import 不命中
// （与 domainpurity 同口径：protobuf runtime 不在 genproto 禁列）。
func anchorRoundtrip() []byte {
	b, _ := protojson.Marshal(anchorSpec())
	return b
}

var _ = fmt.Sprintf
