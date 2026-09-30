package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/model/errcode"
)

// 渲染单点（render 单点约定）：人类形态与 --json 形态的输出全部经本文件
// 落笔，golden 双形态钉死。protojson 与 gateway marshaler 同策略
//（UseProtoNames: snake_case 字段），防 Console/API/CLI 割裂。

var protoJSONMarshal = protojson.MarshalOptions{
	UseProtoNames:   true,
	EmitUnpopulated: false,
}

// writeJSON 输出本地合成结构的 --json 形态（两空格缩进 + 尾换行）。
func writeJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}

// writeProtoJSON 输出 proto 消息的 --json 形态（protojson snake_case；
// json.Indent 归一 map 遍历序，保 golden 确定性）。
func writeProtoJSON(w io.Writer, m proto.Message) error {
	data, err := protoJSONMarshal.Marshal(m)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, buf.String())
	return err
}

// displayVersion 返回人读版本串（dev 兜底）。
func displayVersion(info buildinfo.BuildInfo) string {
	if info.Version == "" {
		return "0.0.0-dev"
	}
	return info.Version
}

// renderErrorFor 是 CLI 错误信封 stderr 渲染（render 单点的一部分）：
// apperr 信封还原成功时输出多行可行动提示；否则退回 err.Error() 单行。
// errChanges 不是错误（diff 类动词的有变化信号，退出码 2 已承载语义），
// 渲染为空——stdout 的 diff 输出才是内容面。
func renderErrorFor(err error) string {
	if errors.Is(err, errChanges) {
		return ""
	}
	if env, ok := apperr.FromError(err); ok {
		var b strings.Builder
		b.WriteString(env.Error())
		if s := env.Suggestion(); s != "" {
			b.WriteString("\nsuggestion: ")
			b.WriteString(s)
		}
		if docs := errcode.DocsURL(env.Code()); docs != "" {
			b.WriteString("\ndocs: ")
			b.WriteString(docs)
		}
		return b.String()
	}
	return err.Error()
}
