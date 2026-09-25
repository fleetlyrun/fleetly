// 终端渲染视图（E7 W5-S6）：@xterm/xterm 的挂载薄壳——生命周期与输入/
// 尺寸事件的外抛。独立成组件（页面不直接 import xterm）使 jsdom 单测可以
// 整体 mock 本模块或 xterm（不测真实 PTY/渲染，设计验收分工）。
//
// 生命周期：挂载即建 Terminal（光标常亮、scrollback 2000）；phase=closed/
// idle 且已有实例时销毁重建空闲态——每次「Open terminal」都是全新缓冲
//（断线后的旧输出不跨会话残留——诚实呈现）。输入/行列尺寸事件外抛
//（onData/onResize——页面转发 conn.write/conn.resize 同步 PTY）。

import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { useEffect, useRef } from "react";

import "@xterm/xterm/css/xterm.css";

export interface AppTerminalViewProps {
  phase: "idle" | "opening" | "active" | "closed";
  /** 注册输出汇（connectTerminal 的 onData 落点）。 */
  registerSink: (fn: (data: Uint8Array) => void) => void;
  /** 键入事件（xterm onData——页面转发 conn.write）。 */
  onInput: (data: string) => void;
  /** 客户端行列落定（fit/窗口缩放）——页面转发 conn.resize 同步 PTY。 */
  onResize: (cols: number, rows: number) => void;
}

export function AppTerminalView({ phase, registerSink, onInput, onResize }: AppTerminalViewProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  // 回调经 ref 间接（mount effect 闭包冻结——props 更新在 effect 内落表）。
  const onInputRef = useRef(onInput);
  const onResizeRef = useRef(onResize);
  const registerSinkRef = useRef(registerSink);
  useEffect(() => {
    onInputRef.current = onInput;
    onResizeRef.current = onResize;
    registerSinkRef.current = registerSink;
  }, [onInput, onResize, registerSink]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const term = new Terminal({
      cursorBlink: true,
      scrollback: 2000,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
      fontSize: 12,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    term.onData((data) => onInputRef.current(data));
    term.onResize(({ cols, rows }) => onResizeRef.current(cols, rows));
    // 首帧布局（容器尺寸就位后）+ 容器尺寸变化跟随（ResizeObserver——
    // 面板栅格变化/窗口缩放时保持行宽诚实）。宿主高度必须是定值：fit 以
    // 宿主当前高度算行数，若高度随内容增长（min-h 自撑高），fit→撑高→
    // RO→fit 成正反馈，页面无限拉长（真机走查实爆）——故外层定高 +
    // overflow-hidden 兜底，宿主自身不带 padding/border（fit 直接量它）。
    const ro = typeof ResizeObserver !== "undefined" ? new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        // 容器零尺寸时 fit 抛错——下一轮布局再试，无害。
      }
    }) : null;
    if (ro) ro.observe(host);
    termRef.current = term;
    fitRef.current = fit;
    registerSinkRef.current((data) => {
      term.write(data);
    });
    return () => {
      if (ro) ro.unobserve(host);
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
    // 生命周期只随挂载（phase 变化不重建——缓冲连续）；重开会话由页面层
    // 处置（closed → 新 ticket → 新 WS，同一终端缓冲续写——断线原因在
    // 状态行，不在缓冲里）。
     
  }, []);

  // 会话激活瞬间把已 fit 的行列推给 PTY：连接建立本身不触发 onResize
  // （尺寸未变），而 relay 侧 PTY 从默认 80x24 起步——不推一次则服务端
  // 按旧行列回显，与客户端渲染错位。
  useEffect(() => {
    if (phase !== "active") return;
    const term = termRef.current;
    if (term) onResizeRef.current(term.cols, term.rows);
  }, [phase]);

  return (
    <div
      className={
        "h-96 overflow-hidden rounded-md border bg-background p-2 " +
        (phase === "active" ? "" : "opacity-80")
      }
    >
      <div
        ref={hostRef}
        data-testid="terminal-session-view"
        data-phase={phase}
        className="h-full w-full"
      />
    </div>
  );
}
