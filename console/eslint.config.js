import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "coverage", "node_modules"] },
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": "off",
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
    },
  },
  {
    // node 侧文件：Playwright 配置/冒烟 spec 读 process.env（FLEETLY_SMOKE_*），
    // 与 vite/eslint 配置同一 globals 组（W1 T1-V2.6）。
    files: ["vite.config.ts", "eslint.config.js", "playwright.config.ts", "tests/**/*.ts"],
    languageOptions: {
      globals: globals.node,
    },
  },
);
