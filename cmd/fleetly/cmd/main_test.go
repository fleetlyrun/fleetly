package cmd

// TestMain 承担两职：①常规测试运行；②假 restic 子进程面——CLI golden 的
// Platform Backup 动词（F2.3）需要可用 restic 链，夹具引擎以 ResticPath
// 指回本测试二进制，重 invoked 时 os.Args[1] 是 "-r"（engine 的 restic
// 调用形如 restic -r <repo> <action>）。假面对 init/backup/check/forget
// 恒成功；snapshots 回 canned 集（golden 确定性：固定短 id/时间/主机名）。
// 跨平台 hermetic（Go 测试二进制在两平台皆可执行——无需 shell 脚本）。

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "-r" {
		fakeResticMain(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeResticSnapsOut 是 snapshots 动作的 canned 集（platform backup/backups
// golden 的确定性锚）。
const fakeResticSnapsOut = `[{"short_id":"a1b2c3d4","time":"2026-01-01T00:00:00Z","hostname":"golden-cp"}]` + "\n"

func fakeResticMain(args []string) {
	for _, a := range args {
		if a == "snapshots" {
			fmt.Print(fakeResticSnapsOut)
			return
		}
	}
	fmt.Println("fake restic ok")
}
