import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { AuthFileItem } from "@/lib/http/types";
import { useCodexResetCredits } from "@/modules/auth-files/hooks/useCodexResetCredits";
import { CodexResetCreditError } from "@/modules/quota/quota-codex-reset";

const mocks = vi.hoisted(() => ({
  fetchCodexResetCredits: vi.fn(),
  consumeCodexResetCredit: vi.fn(),
}));

vi.mock("@/modules/quota/quota-codex-reset", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/modules/quota/quota-codex-reset")>();
  return {
    ...mod,
    fetchCodexResetCredits: mocks.fetchCodexResetCredits,
    consumeCodexResetCredit: mocks.consumeCodexResetCredit,
  };
});

const file: AuthFileItem = {
  name: "codex-reset@example.test.json",
  type: "codex",
  auth_index: "auth-codex-reset",
  account_id: "acct-codex-reset",
};

const availableCredit = {
  id: "credit-reset-1",
  resetType: "codex_rate_limits",
  status: "available" as const,
  grantedAt: "2026-07-01T00:00:00Z",
  expiresAt: "2026-08-01T00:00:00Z",
  title: "One reset",
  description: "Reset eligible Codex windows",
};

describe("useCodexResetCredits", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    mocks.fetchCodexResetCredits.mockReset();
    mocks.consumeCodexResetCredit.mockReset();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    window.sessionStorage.clear();
  });

  test("queries, confirms one credit, consumes it idempotently, and verifies the result", async () => {
    mocks.fetchCodexResetCredits
      .mockResolvedValueOnce({ availableCount: 1, credits: [availableCredit] })
      .mockResolvedValueOnce({ availableCount: 0, credits: [] });
    mocks.consumeCodexResetCredit.mockResolvedValue({ outcome: "reset", windowsReset: 2 });
    const refreshQuota = vi.fn(async () => undefined);
    const { result } = renderHook(() => useCodexResetCredits({ refreshQuota }));

    await act(async () => {
      await result.current.openForFile(file);
    });
    expect(result.current.state.open).toBe(true);
    expect(result.current.state.selectedCreditId).toBe("credit-reset-1");

    await act(async () => {
      await result.current.redeemSelected();
    });

    expect(mocks.consumeCodexResetCredit).toHaveBeenCalledWith(
      file,
      expect.objectContaining({
        creditId: "credit-reset-1",
        idempotencyKey: expect.any(String),
      }),
    );
    expect(mocks.fetchCodexResetCredits).toHaveBeenCalledTimes(2);
    expect(refreshQuota).toHaveBeenCalledWith(file, "codex");
    expect(result.current.state.outcome).toBe("reset");
    expect(result.current.state.windowsReset).toBe(2);
    expect(result.current.state.availableCount).toBe(0);
    expect(result.current.state.verificationWarning).toBe(false);
  });

  test("keeps the same idempotency key when a redemption request must be retried", async () => {
    mocks.fetchCodexResetCredits.mockResolvedValue({
      availableCount: 1,
      credits: [availableCredit],
    });
    mocks.consumeCodexResetCredit
      .mockRejectedValueOnce(new Error("connection lost"))
      .mockResolvedValueOnce({ outcome: "already_redeemed", windowsReset: 0 });
    const refreshQuota = vi.fn(async () => undefined);
    const { result } = renderHook(() => useCodexResetCredits({ refreshQuota }));

    await act(async () => {
      await result.current.openForFile(file);
    });
    await act(async () => {
      await result.current.redeemSelected();
    });
    await waitFor(() => expect(result.current.state.errorCode).toBe("request_failed"));

    await act(async () => {
      await result.current.redeemSelected();
    });

    const calls = mocks.consumeCodexResetCredit.mock.calls as [
      AuthFileItem,
      { creditId: string; idempotencyKey: string },
    ][];
    expect(calls).toHaveLength(2);
    expect(calls[1]?.[1].idempotencyKey).toBe(calls[0]?.[1].idempotencyKey);
  });

  test("treats a malformed verification response as a warning and retains the retry key", async () => {
    mocks.fetchCodexResetCredits
      .mockResolvedValueOnce({ availableCount: 1, credits: [availableCredit] })
      .mockRejectedValueOnce(new CodexResetCreditError("invalid_response"))
      .mockResolvedValueOnce({ availableCount: 0, credits: [] });
    mocks.consumeCodexResetCredit
      .mockResolvedValueOnce({ outcome: "reset", windowsReset: 2 })
      .mockResolvedValueOnce({ outcome: "already_redeemed", windowsReset: 0 });
    const { result } = renderHook(() =>
      useCodexResetCredits({ refreshQuota: vi.fn(async () => undefined) }),
    );

    await act(async () => {
      await result.current.openForFile(file);
    });
    await act(async () => {
      await result.current.redeemSelected();
    });
    expect(result.current.state.verificationWarning).toBe(true);
    expect(result.current.state.selectedCreditId).toBe(availableCredit.id);

    await act(async () => {
      await result.current.redeemSelected();
    });

    const calls = mocks.consumeCodexResetCredit.mock.calls as [
      AuthFileItem,
      { creditId: string; idempotencyKey: string },
    ][];
    expect(calls).toHaveLength(2);
    expect(calls[1]?.[1].idempotencyKey).toBe(calls[0]?.[1].idempotencyKey);
    expect(result.current.state.verificationWarning).toBe(false);
  });

  test("uses an in-memory retry key when session storage writes are unavailable", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("storage disabled", "SecurityError");
    });
    mocks.fetchCodexResetCredits.mockResolvedValue({
      availableCount: 1,
      credits: [availableCredit],
    });
    mocks.consumeCodexResetCredit.mockRejectedValue(new Error("connection lost"));
    const { result } = renderHook(() =>
      useCodexResetCredits({ refreshQuota: vi.fn(async () => undefined) }),
    );

    await act(async () => {
      await result.current.openForFile(file);
    });
    await act(async () => {
      await result.current.redeemSelected();
    });
    await act(async () => {
      await result.current.redeemSelected();
    });

    const calls = mocks.consumeCodexResetCredit.mock.calls as [
      AuthFileItem,
      { creditId: string; idempotencyKey: string },
    ][];
    expect(calls).toHaveLength(2);
    expect(calls[1]?.[1].idempotencyKey).toBe(calls[0]?.[1].idempotencyKey);
  });
});
