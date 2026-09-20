// 登出测试（M9-9）：Sign out 在清 localStorage 凭据的同时清空
// react-query 缓存——下一个会话（换 token/换操作员）不得复用上一个
// 会话的服务端状态。

import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";

import { Layout } from "@/components/layout";
import { getToken, setToken } from "@/api/client";
import { AuthProvider } from "@/auth";
import { queryClient } from "@/query";

describe("Layout sign out (M9-9)", () => {
  it("clears the react-query cache along with credentials", async () => {
    setToken("flt_operator_a");
    // 预置缓存：上一个会话的应用列表与部署历史。
    queryClient.setQueryData(["apps"], { apps: [{ name: "secret-app" }] });
    queryClient.setQueryData(["deployments", "secret-app"], {
      deployments: [],
    });
    expect(queryClient.getQueryData(["apps"])).toBeTruthy();

    render(
      <MemoryRouter>
        <AuthProvider>
          <QueryClientProvider client={queryClient}>
            <Layout />
          </QueryClientProvider>
        </AuthProvider>
      </MemoryRouter>,
    );

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Sign out" }));

    // 凭据与缓存一并清空。
    expect(getToken()).toBe("");
    expect(queryClient.getQueryData(["apps"])).toBeUndefined();
    expect(
      queryClient.getQueryData(["deployments", "secret-app"]),
    ).toBeUndefined();
  });
});
