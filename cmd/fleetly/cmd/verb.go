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
