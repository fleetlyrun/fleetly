package api

// B1 Change Preview 的输入面 DTO（真源自 engine 根包迁出，P3-7 输入面契约
// 收口）：预览产物与拆除级联清单是 interfaces 层装配响应的直接输入，形状
// 与 engine 合成/投影产物同源。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// PreviewSpecResult 是 PreviewSpec 的产物（api 层装配 PreviewDeployResponse
// 的全部引擎侧输入）。
type PreviewSpecResult struct {
	// Spec 是合成后 Spec（段二产物；未冻结——from_build 引用原形保留）。
	Spec *specv1.AppSpec
	// SpecBody 是 Spec 的 marshalSpec 同参规范形（EmitUnpopulated:false +
	// UseProtoNames:true——与冻结咽喉、diff、request_digest 同序列化，
	// 设计稿 §1.3 diff 序列化铁律；「预览=执行」契约测试断言本字节与随后冻结
	// 体逐字节相等）。
	SpecBody []byte
	// Workloads 是提案投影（段三；strict 口径；from_build 进程的 Image 为
	// PreviewDigestPendingImage 占位）。未并卷钉住（placement 对照由调用方
	// 经 applyVolumePinning 只读并入——渲染同款纪律，render.go 注释）。
	Workloads []capability.Workload
	// PendingFromBuild 是 digest-pending 进程名集（from_build 源）。
	PendingFromBuild []string
	// PeerErr 是 CheckPeerRefsSpec 的 strict 预检结果（非 nil = 入队即拒项，
	// 预览期透出为 ADMISSION_PRECHECK warning——谓词与 Submit 单源）。
	PeerErr error
	// PreDeployNetworksErr 是 CheckPreDeployNetworksSpec 的结果（同上）。
	PreDeployNetworksErr error
	// StrictDegraded 报告 strict 投影因未批准 peer 引用失败、workloads 已
	// 退 isolate 重投影（口径差诚实透出：对照半边 RenderWorkloads 恒
	// isolate，本半边恒 strict——设计稿 §1.3 口径差进 fixture）。
	StrictDegraded bool
	// QueueCapacity 是每 App 排队上限（入队即拒项 queue-full 预检的对照值）。
	QueueCapacity int
}

// TeardownCascadeEntry 是拆除级联清单的结构化条目（修复批 F2 铸/验共享
// 单源，设计稿 §1.4 #8）：PreviewTeardown 铸票面（清单展示 + RequestDigest）
// 与 peekTeardownConfirm 消费面（重算比对）经 TeardownCascade 同函数产出
// ——散写双源一旦漂移即铸验 digest 必不相等的静默失配。
type TeardownCascadeEntry struct {
	// Kind 是条目分类（deliveryv1.TeardownItemKind 直引——proto 枚举是
	// 唯一契约源，镜像常量即第二真源）。
	Kind deliveryv1.TeardownItemKind
	// Name 是条目名（workload = 投影 ID；hook 行 = repo 名；route 行 =
	// host+path——与 PreviewTeardown 展示面同形）。
	Name string
}

// TeardownCascadeDigest 计算级联清单的票面指纹（修复批 F2 铸/验共享单源，
// 设计稿 §1.4 #8）：行形 "kind数值:name" 同序拼接（"\n" 连接 + 尾行终止符
// ——与 PreviewTeardown 铸票面原公式逐字节同源）的 sha256。铸票面与
// peekTeardownConfirm 消费面同参数调用，漂移即 STALE。
func TeardownCascadeDigest(entries []TeardownCascadeEntry) string {
	lines := make([]string, 0, len(entries))
	for _, ent := range entries {
		lines = append(lines, fmt.Sprintf("%d:%s", ent.Kind, ent.Name))
	}
	h := sha256.New()
	h.Write([]byte(strings.Join(lines, "\n")))
	h.Write([]byte("\n"))
	return hex.EncodeToString(h.Sum(nil))
}
