import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import { panelMetadataPlugin } from "./src/build/panelMetadata";

export default defineConfig({
  base: "/manage/",
  plugins: [react(), tailwindcss(), panelMetadataPlugin()],
  define: {
    // Prefer CI-provided build version (branch+sha/tag) so UI version auto-refreshes on deploy.
    __APP_VERSION__: JSON.stringify(
      process.env.VITE_APP_VERSION ??
        process.env.APP_VERSION ??
        process.env.npm_package_version ??
        "dev",
    ),
  },
  test: {
    environment: "jsdom",
    // jsdom 默认用 opaque origin，会导致 window.localStorage 抛 SecurityError，
    // 进而在组件测试里报 "Cannot read properties of undefined (reading 'clear')"。
    // 指定一个真实 origin 后 Storage API（localStorage/sessionStorage）即可用。
    environmentOptions: {
      jsdom: {
        url: "http://localhost:3000/",
      },
    },
    setupFiles: ["src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
    restoreMocks: true,
    clearMocks: true,
    mockReset: true,
    testTimeout: 10_000,
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
  css: {
    modules: {
      localsConvention: "camelCase",
      generateScopedName: "[name]__[local]___[hash:base64:5]",
    },
    preprocessorOptions: {
      scss: {
        additionalData: '@use "@/styles/variables.scss" as *;',
      },
    },
  },
  build: {
    target: ["chrome107", "edge107", "firefox104", "safari16"],
    rolldownOptions: {
      input: {
        main: path.resolve(__dirname, "index.html"),
        manage: path.resolve(__dirname, "manage.html"),
      },
      output: {
        codeSplitting: {
          includeDependenciesRecursively: true,
          groups: [
            {
              name: "vendor-react",
              test: /node_modules[\\/](?:react|react-dom|react-router-dom)(?:[\\/]|$)/,
            },
            {
              name: "vendor-animation",
              test: /node_modules[\\/](?:framer-motion|gsap)(?:[\\/]|$)/,
            },
            {
              name: "vendor-i18n",
              test: /node_modules[\\/](?:i18next|react-i18next|goey-toast)(?:[\\/]|$)/,
            },
            {
              name: "vendor-echarts",
              test: /node_modules[\\/](?:echarts|echarts-for-react)(?:[\\/]|$)/,
            },
            {
              name: "vendor-charts",
              test: /node_modules[\\/](?:chart.js|react-chartjs-2)(?:[\\/]|$)/,
            },
            {
              name: "vendor-markdown",
              test:
                /node_modules[\\/](?:react-markdown|react-syntax-highlighter|remark-gfm)(?:[\\/]|$)/,
            },
          ],
        },
      },
    },
  },
  server: {
    host: true,
    port: 5173,
    proxy: {
      "/v0": {
        target: "http://127.0.0.1:8317",
        changeOrigin: false,
        ws: true,
      },
      "/v1": {
        target: "http://127.0.0.1:8317",
        changeOrigin: false,
        ws: true,
      },
      "/v1beta": {
        target: "http://127.0.0.1:8317",
        changeOrigin: false,
        ws: true,
      },
    },
  },
});
