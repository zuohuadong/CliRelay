import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { useSystemStats } from "@/modules/dashboard/useSystemStats";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
}));

vi.mock("@/modules/auth/AuthProvider", () => ({
  useAuth: () => ({
    state: {
      apiBase: "https://example.test",
      managementKey: "management-key",
    },
  }),
}));

vi.mock("@/lib/http/client", () => ({
  apiClient: {
    get: mocks.get,
  },
}));

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readonly url: string;
  readyState = FakeWebSocket.CONNECTING;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: Event) => void) | null = null;
  send = vi.fn();
  close = vi.fn(() => {
    this.readyState = FakeWebSocket.CLOSED;
  });

  emitClose() {
    this.onclose?.(new Event("close"));
  }

  constructor(url: string | URL) {
    this.url = String(url);
    FakeWebSocket.instances.push(this);
  }
}

describe("useSystemStats page visibility", () => {
  afterEach(() => {
    vi.useRealTimers();
    FakeWebSocket.instances = [];
    mocks.get.mockReset();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  test("pauses the websocket while hidden and reconnects when visible", () => {
    vi.useFakeTimers();
    let visibility: DocumentVisibilityState = "visible";
    vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
    vi.stubGlobal("WebSocket", FakeWebSocket);

    const { unmount } = renderHook(() => useSystemStats(10));
    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      visibility = "hidden";
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(FakeWebSocket.instances[0]?.close).toHaveBeenCalledTimes(1);
    expect(FakeWebSocket.instances).toHaveLength(1);

    act(() => {
      visibility = "visible";
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(FakeWebSocket.instances).toHaveLength(2);

    act(() => {
      FakeWebSocket.instances[0]?.emitClose();
      vi.advanceTimersByTime(5_000);
    });
    expect(FakeWebSocket.instances).toHaveLength(2);
    expect(mocks.get).not.toHaveBeenCalled();

    unmount();
  });
});
