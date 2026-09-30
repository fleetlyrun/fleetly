package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/fleetlyrun/fleetly/internal/buildinfo"
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
