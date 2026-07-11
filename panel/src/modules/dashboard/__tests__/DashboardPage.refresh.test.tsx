import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import type { DashboardSummary } from "@/lib/http/apis/usage";
import { DashboardPage } from "@/modules/dashboard/DashboardPage";

const mocks = vi.hoisted(() => ({
  getDashboardSummary: vi.fn(),
  notify: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: mocks.translate,
  }),
}));

vi.mock("@/lib/http/apis/usage", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/lib/http/apis/usage")>();
  return {
    ...mod,
    usageApi: {
      ...mod.usageApi,
      getDashboardSummary: mocks.getDashboardSummary,
    },
  };
});

vi.mock("@/modules/dashboard/SystemMonitorSection", () => ({
  SystemMonitorSection: () => null,
}));

vi.mock("@/modules/dashboard/useSystemStats", () => ({
  useSystemStats: () => ({ stats: null, connected: false }),
}));

vi.mock("@/modules/ui/AnimatedNumber", () => ({
  AnimatedNumber: ({ value }: { value: number }) => <span>{value}</span>,
}));

vi.mock("@/modules/ui/ToastProvider", () => ({
  useToast: () => ({ notify: mocks.notify }),
}));

vi.mock("@/modules/ui/charts/EChart", () => ({
  EChart: () => null,
}));

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function createSummary(days: number): DashboardSummary {
  return {
    days,
    kpi: {
      total_requests: 0,
      success_requests: 0,
      failed_requests: 0,
      success_rate: 0,
      input_tokens: 0,
      output_tokens: 0,
      reasoning_tokens: 0,
      cached_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
    },
    counts: {
      api_keys: 0,
      providers_total: 0,
      gemini_keys: 0,
      claude_keys: 0,
      codex_keys: 0,
      vertex_keys: 0,
      openai_providers: 0,
      auth_files: 0,
    },
  };
}

describe("DashboardPage refresh", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  test("keeps dashboard summary polling single-flight and resumes on the next interval", async () => {
    vi.useFakeTimers();
    const firstRequest = createDeferred<DashboardSummary>();
    mocks.getDashboardSummary
      .mockReturnValueOnce(firstRequest.promise)
      .mockResolvedValue(createSummary(7));

    render(<DashboardPage />);

    expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(1);
    expect(mocks.getDashboardSummary).toHaveBeenLastCalledWith(7);

    act(() => {
      vi.advanceTimersByTime(15_000);
    });

    expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(1);

    firstRequest.resolve(createSummary(7));
    await firstRequest.promise;
    await Promise.resolve();

    expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(1);

    act(() => {
      vi.advanceTimersByTime(5_000);
    });

    expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(2);
    expect(mocks.getDashboardSummary).toHaveBeenLastCalledWith(7);
  });

  test("loads the latest selected range after the active summary request completes", async () => {
    const firstRequest = createDeferred<DashboardSummary>();
    mocks.getDashboardSummary
      .mockReturnValueOnce(firstRequest.promise)
      .mockResolvedValue(createSummary(30));

    render(<DashboardPage />);

    expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("tab", { name: "dashboard.last_30_days" }));
    expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(1);

    firstRequest.resolve(createSummary(7));
    await firstRequest.promise;

    await waitFor(() => {
      expect(mocks.getDashboardSummary).toHaveBeenCalledTimes(2);
    });
    expect(mocks.getDashboardSummary).toHaveBeenLastCalledWith(30);
  });
});
