package eventcode

// 注册表使用点扫描测试（MG-2，B4）：遍历注册表全部事件名，断言每个事件
// 在仓内生产代码（internal + cmd，排除 _test 与注册表自身）有 ≥1 引用点
//（带引号的字符串形态——事件名只以字面量出现在 AppendEvent/appendEvent
// 调用面）。注册但零引用的事件名 = 注册表与实现脱节（漏发或死注册）：
// 真实漏发补最小实现；确属预留的入下方豁免清单（每条必须有注释理由）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// eventExemptions 是零引用事件的豁免清单（MG-2：豁免必须有注释理由；
// 新增豁装需要同步写明预留依据，注册表行同加 `// 预留：…` 注释）。
var eventExemptions = map[string]string{
	// 预留：系统性失败恢复排队（release-semantics §2.7）——v0.1 失败分流
	// 不建新 deployment（D-REL-6 默认只告警），自动恢复动作随 v0.2 恢复器。
	"deployment.recovery_scheduled": "预留：自动恢复排队事件，随 v0.2 恢复器接线",
	// 预留：部署被更新目标取代（陈旧终态）——v0.1 同 app 互斥排队
	//（§2.5），不存在「在途部署被新目标取代」的路径；superseded 随并发
	// 部署策略（v0.2）开放。
	"deployment.superseded": "预留：同 app 互斥排队下无取代路径，随 v0.2 并发策略",
	// 预留：绑定变更事件只在显式确认的迁移路径（rebind/move）发出——
	// v0.1 单机无第二候选（MultiNodeUnsupported 守卫），rebind CLI 属 v0.2。
	"placement.changed": "预留：rebind/迁移路径 v0.2（单机无第二候选）",
	// 预留：DR 后绑定无法判定的显式放置要求——DR 恢复阶梯 L1/L2 的 v0.2
	// 面（单机 v0.1 无 DR 绑定歧义场景）。
	"placement.unresolved": "预留：DR 后绑定判定，随 v0.2 恢复阶梯",
	// 预留：节点观测事件族（joined/down/up/removed）由节点观测器发出——
	// v0.1 单节点无节点观测器循环（节点状态经放置 Preflight 直读）；观测
	// 器随 v0.2 多节点接入。
	"node.joined":  "预留：节点观测器 v0.2（单机无节点观测循环）",
	"node.down":    "预留：节点观测器 v0.2（单机经放置 Preflight 直读）",
	"node.up":      "预留：节点观测器 v0.2",
	"node.removed": "预留：节点观测器 v0.2",
	// 预留：卷声明移除（detached）与显式丢弃（discarded）的事件面——
	// v0.1 对账只动服务面，卷声明移除的显性化与 admin 丢弃 CLI 随卷生命
	// 周期票（v0.2 discard/orphan 面一起接线）。
	"volume.detached":  "预留：卷声明移除显性化，随 v0.2 卷生命周期票",
	"volume.discarded": "预留：admin 丢弃 CLI v0.2",
	// 预留：控制面恢复流程完成事件——恢复器（state-model §2.7 恢复阶梯）
	// 未实现（同 E_BACKUP_KEY_MISSING 的预留裁决）。
	"restore.completed": "预留：恢复器未实现（同 E_BACKUP_KEY_MISSING）",
	// 预留：cron 触发跳过事件——cron 整体入 v0.2（architecture §4.3）。
	"cron.skipped": "预留：cron v0.2",
}

// productionSources 收集 internal 与 cmd 下的生产 .go 文件文本（排除
// _test 与注册表包自身）。
func productionSources(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	out := map[string]string{}
	for _, sub := range []string{"internal", "cmd"} {
		base := filepath.Join(root, sub)
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// 排除注册表包自身（定义文件不算引用点）。
				if filepath.Base(path) == "eventcode" && path == filepath.Join(root, "internal", "eventcode") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(raw)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no production sources scanned（扫描基座失效）")
	}
	return out
}

// TestRegistryEventsReferencedInProduction 注册表全量扫描：每个事件名在
// 生产代码有 ≥1 个带引号引用点（或进入带理由的豁免清单）。
func TestRegistryEventsReferencedInProduction(t *testing.T) {
	srcs := productionSources(t)
	for _, ev := range Default().All() {
		if _, exempt := eventExemptions[ev.Name]; exempt {
			continue
		}
		quoted := `"` + ev.Name + `"`
		found := ""
		for path, src := range srcs {
			if strings.Contains(src, quoted) {
				found = path
				break
			}
		}
		if found == "" {
			t.Errorf("事件 %s 注册后零生产引用（真实漏发则补实现；确属预留则在 eventExemptions 与注册表行注明理由）", ev.Name)
		}
	}
}

// TestEventExemptionsStillRegistered 豁免清单健康度：豁免项必须仍在注册表
// （豁免的是「零引用」而不是「可注销」——名永不复用）；注册表新增事件若
// 零引用且未豁免由上一测试兜底。
func TestEventExemptionsStillRegistered(t *testing.T) {
	for name := range eventExemptions {
		if _, ok := Default().Get(name); !ok {
			t.Errorf("豁免清单项 %s 不在注册表（清单与注册表脱节）", name)
		}
	}
}
