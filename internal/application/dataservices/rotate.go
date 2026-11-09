package dataservices

// Database 凭证轮换（IA v3 二期⑤b，ADR-0029 决策 6 的轮换半边）：方言级
// 数据面改密（databasetemplate.RotatePassword 渲染）+ 凭证 Secret 原子重写 +
// Kick 收敛循环重下发材料（密文指纹变化即载体滚动替换——redis 声明式形态
// 的生效通道，见 databasetemplate.RotateSpec 注）。
//
// 执行序与崩溃窗（诚实边界）：utility（数据面改密）先行、Secret 重写殿后
// ——utility 失败零状态变更（旧值保持有效，重试安全）；两步之间崩溃则
// 数据库已持新值而 Secret 尚旧（引用方断连），恢复走 Browse 会话手工对齐
// 或重试轮换前先经载体重下发回卷（redis 形态自愈）。窗口宽度 = 单行
// Upsert，与 DeleteDatabase 的拆载体→tombstone 同量级接受。
//
// P5 wave-4 随域拆包迁入（接收者 *Databases → B2e 起 *DataServices，
// ADR-0096 决策 1 单应用模块包）；gRPC 轮换受理面经核门面
// e.RotateDatabasePassword 委托与哨兵别名 engine.ErrRotateRejected（消费
// 点零改）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// ErrRotateRejected 是"数据面改密被数据库拒绝"的哨兵（API 层 map 为
// E_DATABASE_ROTATE_FAILED 的判别锚；engine 不 import api 包——哨兵错误
// + 侧 map 精确码的既有分居，RestartComponent 同款）。包裹的报文携带
// 工具容器 stderr/stdout 尾部（引擎自己的回执是诊断真相）。P5 wave-4
// 拆包导出（核侧经 var 别名零漂移）。
var ErrRotateRejected = errors.New("database rejected the credential change")

// RotateDatabasePassword 轮换数据库凭证：读行 → 解密现值 → 铸新值 →
// 方言渲染（utility 数据面改密；redis 空 Argv 跳过）→ Secret 重写 →
// 事件 + Kick。调用方（API 受理位）负责授权与在服检查；行不存在返回
// state.ErrNotFound（API 404 口径，与 TeardownDatabase 同）。
func (d *DataServices) RotateDatabasePassword(ctx context.Context, databaseID string) error {
	// 轮换单飞（全引擎域）：并发轮换的 utility/Secret 写交错可能落下
	// "Secret 与数据面不一致"的终态（U1 U2 S2 S1 序）；轮换是罕见运维
	// 动作，全域串行是最小的诚实防线。
	d.rotateMu.Lock()
	defer d.rotateMu.Unlock()

	row, err := d.repos.Databases.Get(ctx, databaseID)
	if err != nil {
		return err
	}
	// B2c 起按行值绑版（弃用版本照常轮换——存量全语义续跑，ADR-0094 决策 6）。
	tpl, err := databasetemplate.For(row.Engine, row.Version)
	if err != nil {
		return fmt.Errorf("engine %q left the template registry: %w", row.Engine, err)
	}
	current, err := d.databasePassword(ctx, row)
	if err != nil {
		return err
	}
	next := databasetemplate.MintPassword()
	spec, err := tpl.RotatePassword(contract.DatabaseDNSName(row.ID), current, next)
	if err != nil {
		return err
	}
	if len(spec.Argv) > 0 {
		if err := d.runRotateUtility(ctx, row, tpl, spec); err != nil {
			return err
		}
	}
	// Secret 原子重写（单真源换值；Upsert 同名活跃行覆盖）。新连接串以
	// 模板 ConnURL 铸（host = db-<id>，与创建面 DatabaseConnectionURL 同
	// 一公式——engine 铸名真源分居，模板只拥有 URL 形状）。
	newURL := tpl.ConnURL(contract.DatabaseDNSName(row.ID), next)
	sealed, err := d.cipher.Produce([]byte(newURL))
	if err != nil {
		return fmt.Errorf("seal rotated credential: %w", err)
	}
	sec := &secret.Secret{
		ProjectID: row.ProjectID, Name: row.CredentialsRef,
		Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
	}
	if err := d.repos.Secrets.Upsert(ctx, sec); err != nil {
		return fmt.Errorf("rewrite credential secret %q: %w", row.CredentialsRef, err)
	}
	// 密文已变 → 短路签名失效 → 下一拍重下发材料（redis 生效通道）；
	// Kick 立即驱动不等节拍。事件与审计由 API 受理位落账（DeleteDatabase
	// 同款序：先变更后留痕——engine 侧不携带 outbox 面）。
	d.kickDatabases()
	return nil
}

// runRotateUtility 执行数据面改密（一次性工具容器）。执行链与备份/恢复/
// dynamic 热应用共用（B2d 起 runUtilityContainer 单源：维护互斥读半边 +
// 带界 + stderr/stdout 尾部入错误报文）。P4 wave-2a：utility 随 databases
// 应用模块双持（backups 同源副本，P5 过渡形态）；RunUtility 是 RuntimeUtility
// 型调用，不在 maintenanceguard 载体写动词执法面（Ensure/Remove/attach 族
// 才执法），锁纪律仍由 lockMaintenance 端口承载。
func (d *DataServices) runRotateUtility(ctx context.Context, row *database.Database, tpl databasetemplate.Template, spec databasetemplate.RotateSpec) error {
	req := capability.UtilityRequest{
		ID:          "rotate-" + row.ID,
		Namespace:   capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, Database: row.ID, DatabaseName: row.Name},
		Image:       tpl.Image(),
		User:        tpl.ToolUser(), // B8-fix 通道；数据库五引擎缺省空——零行为变化
		Argv:        spec.Argv,
		Env:         spec.Env,
		Networks:    d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
		SecretFiles: spec.SecretFiles,
	}
	if err := d.runUtilityContainer(ctx, req); err != nil {
		return fmt.Errorf("%w: %s", ErrRotateRejected, err)
	}
	return nil
}

// limitedBuffer 是有界尾部捕获（boundedTail 同款语义的 rotate 局部形态；
// stderrTail 消费 String()）。
type limitedBuffer struct {
	buf []byte
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return contract.TrimTrailing(string(b.buf)) }
