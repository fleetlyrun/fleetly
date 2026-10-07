import { act, cleanup, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { Modal } from "./ui";

// vitest 无 globals 时 RTL 不自动 cleanup——跨测试残留 dialog 会串场。
afterEach(cleanup);

// jsdom 未实现 dialog 的 showModal/close——测试侧 polyfill 到位即可驱动
// 受控开合语义（open 属性 + close 事件），组件代码不为测试环境让步。
beforeAll(() => {
  if (typeof HTMLDialogElement.prototype.showModal !== "function") {
    HTMLDialogElement.prototype.showModal = function (this: HTMLDialogElement) {
      this.setAttribute("open", "");
    };
    HTMLDialogElement.prototype.close = function (this: HTMLDialogElement) {
      this.removeAttribute("open");
      this.dispatchEvent(new Event("close"));
    };
  }
});

// Modal 结构守卫（2026-10-07 浏览器走查 W1/W3 回归锚）：
// ①壳层不得包 form——嵌套 form 的 submit 在真实浏览器不冒泡出外层
// form，Modal 内全部业务表单的 onSubmit 会整体失效（jsdom 冒泡行为
// 不同，走查前的组件测试全绿正是漏网原因）；
// ②dialog 的 close/cancel 不冒泡，React 委托面收不到——必须原生监听
// 直挂节点，否则 Escape 关窗后受控 open 态失同步（"再开无响应"）。
describe("Modal", () => {
  it("does not wrap children in a form (nested forms break submit bubbling in real browsers)", () => {
    render(
      <Modal title="New project" open onClose={() => {}}>
        <form onSubmit={() => {}}>
          <input aria-label="Name" />
        </form>
      </Modal>,
    );
    const forms = document.querySelectorAll("form");
    expect(forms.length).toBe(1);
    const form = forms[0];
    // 内层业务 form 之上不得再有 form 祖先（嵌套即 W1 形态）。
    expect(form.closest("dialog")?.querySelectorAll("form").length).toBe(1);
    expect(form.parentElement?.closest("form")).toBeNull();
  });

  it("renders the close affordance as a plain button that reports onClose", () => {
    const onClose = vi.fn();
    render(
      <Modal title="New project" open onClose={onClose}>
        <p>body</p>
      </Modal>,
    );
    const close = screen.getByRole("button", { name: "✕" });
    expect(close.getAttribute("type")).toBe("button");
    close.click();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("syncs onClose when the native dialog close event fires (Escape desync guard)", () => {
    const onClose = vi.fn();
    render(
      <Modal title="New project" open onClose={onClose}>
        <p>body</p>
      </Modal>,
    );
    // Escape 的浏览器收口 = 原生 close 事件；React 收不到（不冒泡），
    // 组件必须靠直挂监听把它归一到 onClose。
    const dlg = document.querySelector("dialog");
    expect(dlg).not.toBeNull();
    dlg!.dispatchEvent(new Event("close"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("re-opens after a native close when the controlled open state toggles", () => {
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button type="button" onClick={() => setOpen((v) => !v)}>
            toggle
          </button>
          <Modal title="New project" open={open} onClose={() => setOpen(false)}>
            <p>body</p>
          </Modal>
        </>
      );
    }
    render(<Harness />);
    const dlg = document.querySelector("dialog") as HTMLDialogElement;
    // 原生关闭（Escape 语义，close 事件随之）→ 受控态翻转 → 再次 open 应重新 showModal。
    // 每步 act 冲刷，避免与后续 setState 同批（open 净值不变时 effect 不重跑）。
    act(() => {
      dlg.close();
    });
    expect((document.querySelector("dialog") as HTMLDialogElement).open).toBe(false);
    act(() => {
      screen.getByRole("button", { name: "toggle" }).click();
    });
    expect((document.querySelector("dialog") as HTMLDialogElement).open).toBe(true);
  });
});
