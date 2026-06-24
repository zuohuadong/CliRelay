// jsdom 在部分 Node 版本下（Node 26 实验性 localStorage 未启用时）
// 会让 window.localStorage/sessionStorage 的 getter 返回 undefined，
// 导致组件测试访问时报 "Cannot read properties of undefined (reading 'clear')"。
// 这里提供一个最小的内存版 Storage，仅当 jsdom 未提供可用实现时安装。
function installMemoryStorage(name: "localStorage" | "sessionStorage") {
  if (typeof window === "undefined") return;
  if (typeof (window as unknown as Record<string, unknown>)[name] !== "undefined") return;

  const store = new Map<string, string>();
  const storage: Storage = {
    get length() {
      return store.size;
    },
    clear: () => store.clear(),
    getItem: (key: string) => (store.has(key) ? store.get(key)! : null),
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => {
      store.delete(key);
    },
    setItem: (key: string, value: string) => {
      store.set(key, String(value));
    },
  };
  Object.defineProperty(window, name, {
    configurable: true,
    value: storage,
  });
}

installMemoryStorage("localStorage");
installMemoryStorage("sessionStorage");

import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";
import "@/i18n";

afterEach(() => {
  cleanup();
});

if (typeof window !== "undefined") {
  if (!window.matchMedia) {
    window.matchMedia = ((query: string) =>
      ({
        matches: false,
        media: query,
        onchange: null,
        addListener: () => undefined,
        removeListener: () => undefined,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
        dispatchEvent: () => false,
      }) as unknown as MediaQueryList) as typeof window.matchMedia;
  }
}

if (typeof globalThis !== "undefined" && !(globalThis as any).ResizeObserver) {
  (globalThis as any).ResizeObserver = class ResizeObserver {
    observe() {
      // noop
    }
    unobserve() {
      // noop
    }
    disconnect() {
      // noop
    }
  };
}
