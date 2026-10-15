// Package channel 是通知通道聚合的领域面（F2.5，ADR-0041 决策 4，
// ADR-0075 Repo 接口层）：配置 age 信封入库（ADR-0014——URL/bot_token
// 是凭证材料，只写不读；加密机械留实现侧）；last_failure 是派发诊断面
// （成功清位）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/channel。
package channel

// 通道类别值域（冻结词汇）。
const (
	KindWebhook  = "webhook"
	KindTelegram = "telegram"
)

// Channel 是一行通知通道。
type Channel struct {
	ID               string
	Seq              int64
	Name             string
	Kind             string
	ConfigCiphertext []byte // age 信封载荷
	Enabled          bool
	LastFailure      string
	CreatedAt        string
	UpdatedAt        string
}
