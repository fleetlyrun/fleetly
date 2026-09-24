package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// git.hostkey_fingerprint 指纹台账（v0.3 W3-S2，rbac-teams §6 裁决 D-W0-8
// FZ-12）：git SSH host key 的 SHA256 指纹落 platform_settings，host key
// 启动装载时与台账比对——无台账 = 首启（静默写入，零事件零审计）；一致 =
// 无动作；不同 = host key 已重建/换钥 → 审计 + 事件 git.hostkey_changed
// 与台账更新同事务（Outbox，fail-closed）。
//
// 指纹 = gossh.FingerprintSHA256（OpenSSH 形态 SHA256:…）——公钥指纹是
// 公开材料（known_hosts 核对值），可进审计/事件/读面；私钥文件本体绝不
// 入库（state-model §2.9 secret 纪律）。变更检测挂在启动装载（单次装载
// 现状）：进程生命周期内的文件替换在下次启动披露——诚实边界，见
// internal/gitserver/hostkey.go。

// GitKeyHostKeyFingerprint 是 git SSH host key 指纹台账键（词表只增；键名
// 常量为本包唯一登记点）。
const GitKeyHostKeyFingerprint = "git.hostkey_fingerprint"

// git host key 事件/审计词根（审计动作词不入 errcode/eventcode 注册表；
// 事件名 git.hostkey_changed 已在 eventcode 注册表登记——只增纪律）。
const (
	gitHostKeyChangedAction = "git.hostkey_changed"
	gitHostKeySubject       = "platform:git"
)

// LoadGitHostKeyFingerprint 读取指纹台账（每次现读）。无台账 = 空串非错误
// （「首启」与「未设置」同形——装载方以空串为首次写入判定）。
func (s *Store) LoadGitHostKeyFingerprint(ctx context.Context) (string, error) {
	value, err := s.loadSettingRaw(ctx, GitKeyHostKeyFingerprint)
	if err != nil {
		return "", fmt.Errorf("state: load git host key fingerprint: %w", err)
	}
	return value, nil
}

// loadSettingRawTx 是事务内的单条 platform_settings 值读取（loadSettingRaw
// 的 Tx 形态——比对与写入同事务的消费者；缺行 = 空串非错误）。
func loadSettingRawTx(ctx context.Context, tx *Tx, key string) (string, error) {
	var v string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM platform_settings WHERE key = ?`, key).Scan(&v)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return "", fmt.Errorf("state: load %s: %w", key, err)
}

// ReconcileGitHostKeyFingerprint 把装载指纹与台账比对并收敛（单写点，
// 单事务）：无台账或同值 = 写入/无动作且返回 changed=false；台账已有且
// 不同 = host key 重建/换钥 → 台账更新 + 审计（actor=system）+ 事件
// git.hostkey_changed 同事务落库，返回 changed=true。payload/审计 diff 只
// 带新旧指纹（公开材料）。
func (s *Store) ReconcileGitHostKeyFingerprint(ctx context.Context, fingerprint string) (bool, error) {
	changed := false
	err := s.InTx(ctx, func(tx *Tx) error {
		previous, err := loadSettingRawTx(ctx, tx, GitKeyHostKeyFingerprint)
		if err != nil {
			return fmt.Errorf("state: read git host key fingerprint: %w", err)
		}
		if previous == fingerprint {
			return nil
		}
		const q = `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
		if _, err := tx.ExecContext(ctx, q, GitKeyHostKeyFingerprint, fingerprint, nowNano()); err != nil {
			return fmt.Errorf("state: upsert git host key fingerprint: %w", err)
		}
		if previous == "" {
			// 首启建账：静默写入（零事件零审计——D-W0-8 口径，首启非变更）。
			return nil
		}
		changed = true
		diff := DiffSummary("old", previous, "new", fingerprint)
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      gitHostKeyChangedAction,
			Target:      gitHostKeySubject,
			Result:      "ok",
			DiffSummary: diff,
		}); err != nil {
			return err
		}
		_, err = tx.AppendEvent(ctx, Event{
			Name:    gitHostKeyChangedAction,
			Subject: gitHostKeySubject,
			Payload: diff,
		})
		return err
	})
	if err != nil {
		return false, fmt.Errorf("state: reconcile git host key fingerprint: %w", err)
	}
	return changed, nil
}
