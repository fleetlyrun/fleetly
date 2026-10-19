package cmd

import (
	"context"
	"flag"

	"github.com/lynx-go/commands"
)

// flaggedVerb 是 fleetly 动词的统一形态：旗标经 SetFlags 声明（连接旗标
// 等），--json 由 root bool flag 剥取后经 jsonOut 传入 run——每动词的
// 人类/机器双形态在同一 run 内收口，golden 双形态钉死。
type flaggedVerb struct {
	name     string
	synopsis string
	usage    string
	setFlags func(fs *flag.FlagSet) // 可选：追加旗标声明
	run      func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error

	// subcommands 非空 = 动词组（groupVerb 装配时记录子命令名）。
	// 覆盖守卫据此递归枚举组内子命令，组员与组本身同受双 golden 契约。
	subcommands []string
	// innerApp 是组的分发内核（仅 groupVerb 装配时非 nil）：覆盖守卫递归
	// 下钻二层及以上嵌套组（alerts rules / apps vars）用——subcommands 只有
	// 名字、没有子命令对象，无法判定组员是否又是组（R10-L1 只递归一层）。
	innerApp *commands.App
}

func (v *flaggedVerb) Name() string     { return v.name }
func (v *flaggedVerb) Synopsis() string { return v.synopsis }
func (v *flaggedVerb) Usage() string    { return v.usage }

func (v *flaggedVerb) SetFlags(fs *flag.FlagSet) {
	if v.setFlags != nil {
		v.setFlags(fs)
	}
}

func (v *flaggedVerb) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return v.run(ctx, env, args, env.RootBools["json"])
}
