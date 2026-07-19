import { afterEach, describe, expect, test, vi } from "vitest";
import { authFilesApi } from "@/lib/http/apis";
import type { AuthFileItem } from "@/lib/http/types";
import {
  consumeCodexResetCredit,
  fetchCodexResetCredits,
  parseCodexResetCreditsDetails,
} from "@/modules/quota/quota-codex-reset";

const codexFile: AuthFileItem = {
  name: "codex-user.json",
  type: "codex",
  auth_index: "auth-codex-user",
  account_id: "acct-user",
};

describe("Codex reset credits", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("parses reset-credit details and preserves display metadata", () => {
    expect(
      parseCodexResetCreditsDetails({
        available_count: 2,
        credits: [
          {
            id: "credit-1",
            reset_type: "codex_rate_limits",
            status: "available",
            granted_at: "2026-07-01T00:00:00Z",
            expires_at: "2026-08-01T00:00:00Z",
            title: "Summer reset",
            description: "Resets eligible Codex windows",
          },
        ],
      }),
    ).toEqual({
      availableCount: 2,
      credits: [
        {
          id: "credit-1",
          resetType: "codex_rate_limits",
          status: "available",
          grantedAt: "2026-07-01T00:00:00Z",
          expiresAt: "2026-08-01T00:00:00Z",
          title: "Summer reset",
          description: "Resets eligible Codex windows",
        },
      ],
    });
  });

  test.each([
    {},
    { error: { message: "upstream failed" } },
    {
      available_count: 1,
      credits: [{ id: "credit-with-missing-fields" }],
    },
  ])("rejects a malformed list response %#", (payload) => {
    expect(parseCodexResetCreditsDetails(payload)).toBeNull();
  });

  test("queries credits through the selected auth-file management endpoint", async () => {
    const request = vi.spyOn(authFilesApi, "getCodexResetCredits").mockResolvedValue({
      available_count: 1,
      credits: [
        {
          id: "credit-1",
          reset_type: "codex_rate_limits",
          status: "available",
          granted_at: "2026-07-01T00:00:00Z",
          expires_at: null,
        },
      ],
    });

    const result = await fetchCodexResetCredits(codexFile);

    expect(result.availableCount).toBe(1);
    expect(request).toHaveBeenCalledWith("codex-user.json");
  });

  test("consumes exactly one selected credit with an idempotency key", async () => {
    const request = vi
      .spyOn(authFilesApi, "consumeCodexResetCredit")
      .mockResolvedValue({ code: "reset", windows_reset: 2 });

    const result = await consumeCodexResetCredit(codexFile, {
      creditId: "credit-1",
      idempotencyKey: "550e8400-e29b-41d4-a716-446655440000",
    });

    expect(result).toEqual({ outcome: "reset", windowsReset: 2 });
    expect(request).toHaveBeenCalledWith({
      name: "codex-user.json",
      credit_id: "credit-1",
      idempotency_key: "550e8400-e29b-41d4-a716-446655440000",
    });
  });

  test("reports an unavailable upstream route without treating it as success", async () => {
    vi.spyOn(authFilesApi, "getCodexResetCredits").mockRejectedValue(
      new Error("Request failed (404)"),
    );

    await expect(fetchCodexResetCredits(codexFile)).rejects.toMatchObject({
      code: "unsupported",
    });
  });
});
