package api

// engine/contract 契约叶子的输入面再导出：exec/browse/relay/platform/
// system/workload/registry/dataservices 的值类型、常量、哨兵与纯公式已随
// 应用模块拆包下沉 engine/contract——本包以别名/声明委托原样透出，输入面
// 只 import 本包即可，无需触达 engine 根包（真源仍是 contract，零双源）。

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// 受理位写原语声明面（真源 contract）。
type (
	Check     = contract.Check
	WriteFact = contract.WriteFact
)

// 平台备份/系统观测值类型（真源 contract）。
type (
	PlatformSnapshot = contract.PlatformSnapshot
	SystemAlertState = contract.SystemAlertState
)

// execs 域类型（真源 contract；gRPC 流适配器与 assembly WS 适配器消费）。
type (
	ExecSessionInfo = contract.ExecSessionInfo
	CreateExecInput = contract.CreateExecInput
	ExecClientFrame = contract.ExecClientFrame
	ExecServerFrame = contract.ExecServerFrame
	ExecClientPipe  = contract.ExecClientPipe
	RelayWriter     = contract.RelayWriter
	RelayStatus     = contract.RelayStatus
)

// browses 域类型与常量（真源 contract）。
type (
	BrowseInput       = contract.BrowseInput
	BrowseSessionInfo = contract.BrowseSessionInfo
)

const (
	BrowseMaxSessionsPerTeam = contract.BrowseMaxSessionsPerTeam
	BrowseHardTTL            = contract.BrowseHardTTL
)

// 契约叶子哨兵（api/assembly 层 errors.Is 判定零改；真源 contract）。
var (
	ErrExecUnsupported           = contract.ErrExecUnsupported
	ErrExecNoInstance            = contract.ErrExecNoInstance
	ErrExecInstanceGone          = contract.ErrExecInstanceGone
	ErrExecNodeUnanchored        = contract.ErrExecNodeUnanchored
	ErrExecRelayOffline          = contract.ErrExecRelayOffline
	ErrExecTeamLimit             = contract.ErrExecTeamLimit
	ErrPlatformBackupInProgress  = contract.ErrPlatformBackupInProgress
	ErrRotateRejected            = contract.ErrRotateRejected
	ErrSettingsApplyRejected     = contract.ErrSettingsApplyRejected
	ErrVolumeForeignOrigin       = contract.ErrVolumeForeignOrigin
	ErrInvalidRegistryRepository = contract.ErrInvalidRegistryRepository
	ErrRegistryUnavailable       = contract.ErrRegistryUnavailable
)

// 纯公式/声明委托（真源 contract；输入面既名引用零改）。
var (
	WorkloadID                    = contract.WorkloadID
	DBCredentialSecretName        = contract.DBCredentialSecretName
	DatabaseDNSName               = contract.DatabaseDNSName
	DatabaseConnectionURL         = contract.DatabaseConnectionURL
	IsDatabaseCredentialSecret    = contract.IsDatabaseCredentialSecret
	IsPubSubCredentialSecret      = contract.IsPubSubCredentialSecret
	PubSubConnMaterial            = contract.PubSubConnMaterial
	IsBucketStoreCredentialSecret = contract.IsBucketStoreCredentialSecret
	BucketStoreConnMaterial       = contract.BucketStoreConnMaterial
	ValidateRegistryRepository    = contract.ValidateRegistryRepository
	PubCredentialSecretName       = contract.PubCredentialSecretName
	PubSubDNSName                 = contract.PubSubDNSName
	BKTCredentialSecretName       = contract.BKTCredentialSecretName
	BucketStoreDNSName            = contract.BucketStoreDNSName
)
