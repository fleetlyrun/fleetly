import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { createExecSession, openExecStream, sendResize, sendStdin } from "@/api/execstream";
import { ApiError } from "@/api/client";
import type { ServerFrame } from "@/lib/execFrames";

// exec 会话生命周期单源（批3 订阅族收尾，uikit §12-5「terminal 自持收尾
// ——保持一份实现」）：受理换票据 → WS 流 → xterm 双向接线 → stdin/resize/
// exit/error 帧投递 → 断线/退出呈现重开面 → dispose。协议/会话语义零改动
//（ADR-0049：会话不迁移，重开 = 新会话新票据，决策 5；实例锚定 ADR-0067
// ——instance 空串 = auto，平台代选首个在跑）。
// 行为语义取两处现存实现（平台 /terminal 页 + app Terminal tab）的并集：
// - connectSeq 作废在飞受理 + ws 身份比对（app tab 侧加固）——切换/断开/
//   卸载后旧流的 onClose 与迟到的受理决议不再迁移状态；
// - disconnect 主动收口（app tab 侧）；
// - connect 显式传参（state 在异步闭包里滞后的反例面）；
// - connect 防御性收口旧流（对既有编排无感——页面先 disconnect 的路径不变）。
// 差异面（进程/实例选择 UI、app 语境固化、切换重连编排、多会话）留在页面层。

export type ExecStatus = "idle" | "connecting" | "open" | "closed";

// TERMINAL_BACKGROUND xterm 画布底色：镜像 demo token --background（styles.css
// oklch(0.145 0 0)）。xterm 主题色经 canvas fillStyle 解析、不认 var()——
// 具体色串在此单源；容器面走 bg-background 同值对齐（原硬编码
// oklch(0.135 0.015 285) 为 demo 色板采纳前的旧值）。
export const TERMINAL_BACKGROUND = "oklch(0.145 0 0)";

export interface ExecConnectTarget {
  appId: string;
  process: string;
  /** argv 空数组 = /bin/sh（服务端缺省） */
  argv: string[];
  /** 实例锚（ADR-0067）；空 = auto（平台代选首个在跑） */
  instance?: string;
}

export interface ExecSessionController {
  status: ExecStatus;
  /** 终态/断线横幅（exit 退出码、error 码、disconnected）——呈现归页面 */
  banner: string;
  fontSize: number;
  setFontSize: (next: number | ((prev: number) => number)) => void;
  /** 外层容器（观察尺寸驱动 fit）与 xterm 挂载点——页面分发给两个 div */
  shellRef: RefObject<HTMLDivElement | null>;
  termDivRef: RefObject<HTMLDivElement | null>;
  /** 受理新会话；现役流/在飞受理被静默接管（弃置会话的票据秒级 TTL 自然过期） */
  connect: (target: ExecConnectTarget) => void;
  /** 主动收口现役会话，呈现重开面 */
  disconnect: () => void;
}

export function useExecSession(): ExecSessionController {
  const [status, setStatus] = useState<ExecStatus>("idle");
  const [banner, setBanner] = useState("");
  const [fontSize, setFontSize] = useState(13);

  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  // connectSeq 作废在飞受理：切换/断开/卸载时递增序号，旧受理的 .then 见
  // 序号落后即弃置（弃置会话的票据秒级 TTL 自然过期——客户端无从撤销受理）。
  const connectSeqRef = useRef(0);
  const shellRef = useRef<HTMLDivElement | null>(null);
  const termDivRef = useRef<HTMLDivElement | null>(null);

  // xterm 挂载（一次）：输出写入 + 输入上行 + resize 双向；ResizeObserver
  // 观察容器（flex 自适应高度形态，页面定高/自适应皆可）。
  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      scrollback: 5000,
      theme: { background: TERMINAL_BACKGROUND },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    if (termDivRef.current !== null) {
      term.open(termDivRef.current);
      fit.fit();
    }
    term.onData((data) => sendStdin(wsRef.current, data));
    term.onResize(({ cols, rows }) => sendResize(wsRef.current, cols, rows));
    termRef.current = term;
    fitRef.current = fit;
    const observer = new ResizeObserver(() => fitRef.current?.fit());
    if (shellRef.current !== null) observer.observe(shellRef.current);
    return () => {
      observer.disconnect();
      connectSeqRef.current += 1; // 卸载作废在飞受理——迟到决议不再触状态
      wsRef.current?.close();
      wsRef.current = null;
      term.dispose();
      termRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (termRef.current != null) termRef.current.options.fontSize = fontSize;
    fitRef.current?.fit();
  }, [fontSize]);

  function deliver(term: Terminal, frame: ServerFrame) {
    switch (frame.kind) {
      case "stdout":
      case "stderr":
        term.write(frame.data);
        return;
      case "exit":
        term.writeln(`\x1b[90m# exited with code ${frame.exit.code}\x1b[0m`);
        setBanner(`exited with code ${frame.exit.code}`);
        setStatus("closed");
        return;
      case "error":
        term.writeln(`\x1b[31m# ${frame.error.code}: ${frame.error.message}\x1b[0m`);
        setBanner(`${frame.error.code}: ${frame.error.message}`);
        setStatus("closed");
        return;
      case "meta":
        return;
    }
  }

  // disconnect 主动收口当前会话（旧流 onClose 静默——状态迁移只认现役流）。
  function disconnect() {
    connectSeqRef.current += 1;
    const ws = wsRef.current;
    wsRef.current = null;
    if (ws != null && ws.readyState !== WebSocket.CLOSED) ws.close();
    setStatus("closed");
    setBanner("disconnected");
    termRef.current?.writeln("\x1b[90m# disconnected\x1b[0m");
  }

  // connect 以显式目标受理（state 在异步闭包里会滞后——切换重连路径必须
  // 传新值）；先静默收口旧流，不留幽灵会话。
  function connect(target: ExecConnectTarget) {
    if (target.appId === "" || target.process === "" || termRef.current === null) return;
    connectSeqRef.current += 1;
    const seq = connectSeqRef.current;
    const stale = wsRef.current;
    wsRef.current = null;
    if (stale != null && stale.readyState !== WebSocket.CLOSED) stale.close();
    const term = termRef.current;
    term.reset();
    setBanner("");
    setStatus("connecting");
    createExecSession(target.appId, target.process, target.argv, true, target.instance ?? "")
      .then((created) => {
        if (seq !== connectSeqRef.current) return; // 已被后续切换/断开取代
        const session = created.session;
        if (session?.id === "" || session?.id == null || created.ticket == null || created.ticket === "") {
          throw new ApiError(0, "no_session", "session acceptance returned no ticket");
        }
        term.writeln(`\x1b[90m# session ${session.id} on ${target.appId}/${target.process} (instance ${session.instance ?? "?"}, node ${session.node_id ?? "?"})\x1b[0m`);
        const ws = openExecStream(session.id, created.ticket, {
          onFrame: (frame: ServerFrame) => deliver(term, frame),
          onClose: () => {
            if (wsRef.current === ws) setStatus((cur) => (cur === "open" ? "closed" : cur));
          },
        });
        ws.onopen = () => {
          if (wsRef.current !== ws) return;
          setStatus("open");
          fitRef.current?.fit();
        };
        wsRef.current = ws;
      })
      .catch((err: unknown) => {
        if (seq !== connectSeqRef.current) return;
        const message = err instanceof ApiError ? `${err.code}: ${err.message}` : String(err);
        term.writeln(`\x1b[31m${message}\x1b[0m`);
        setBanner(message);
        setStatus("idle");
      });
  }

  return { status, banner, fontSize, setFontSize, shellRef, termDivRef, connect, disconnect };
}
