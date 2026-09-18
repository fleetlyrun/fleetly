import "@testing-library/jest-dom/vitest";

// jsdom 缺少的少量 API（TextDecoder 流式解码在 node 环境由 polyfill 提供）。
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
  window.localStorage.clear();
  window.sessionStorage.clear();
});
