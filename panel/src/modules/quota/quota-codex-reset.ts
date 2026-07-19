import { authFilesApi } from "@/lib/http/apis";
import type { AuthFileItem } from "@/lib/http/types";
import { isRecord } from "@/modules/quota/quota-helpers";

export type CodexResetCreditStatus = "available" | "redeeming" | "redeemed" | "unknown";

export interface CodexResetCredit {
  id: string;
  resetType: string;
  status: CodexResetCreditStatus;
  grantedAt: string;
  expiresAt: string | null;
  title: string | null;
  description: string | null;
}

export interface CodexResetCreditsDetails {
  credits: CodexResetCredit[];
  availableCount: number;
}

export type CodexResetCreditOutcome =
  | "reset"
  | "nothing_to_reset"
  | "no_credit"
  | "already_redeemed";

export interface CodexResetCreditResult {
  outcome: CodexResetCreditOutcome;
  windowsReset: number;
}

export type CodexResetCreditErrorCode = "unsupported" | "invalid_response" | "request_failed";

export class CodexResetCreditError extends Error {
  readonly code: CodexResetCreditErrorCode;

  constructor(code: CodexResetCreditErrorCode, message?: string) {
    super(message ?? code);
    this.name = "CodexResetCreditError";
    this.code = code;
  }
}

const nullableString = (value: unknown): string | null | undefined => {
  if (value === null || value === undefined) return null;
  if (typeof value !== "string") return undefined;
  return value.trim() || null;
};

const requiredString = (value: unknown): string | null => {
  if (typeof value !== "string") return null;
  return value.trim() || null;
};

const normalizeStatus = (value: unknown): CodexResetCreditStatus | null => {
  if (typeof value !== "string" || !value.trim()) return null;
  const normalized = value.trim().toLowerCase();
  if (normalized === "available" || normalized === "redeeming" || normalized === "redeemed") {
    return normalized;
  }
  return "unknown";
};

const parseCredit = (rawCredit: unknown): CodexResetCredit | null => {
  if (!isRecord(rawCredit)) return null;
  const id = requiredString(rawCredit.id);
  const resetType = requiredString(rawCredit.reset_type);
  const status = normalizeStatus(rawCredit.status);
  const grantedAt = requiredString(rawCredit.granted_at);
  const expiresAt = nullableString(rawCredit.expires_at);
  const title = nullableString(rawCredit.title);
  const description = nullableString(rawCredit.description);
  if (!id || !resetType || !status || !grantedAt) return null;
  if (expiresAt === undefined || title === undefined || description === undefined) return null;

  return { id, resetType, status, grantedAt, expiresAt, title, description };
};

export const parseCodexResetCreditsDetails = (
  rawPayload: unknown,
): CodexResetCreditsDetails | null => {
  if (!isRecord(rawPayload) || !Array.isArray(rawPayload.credits)) return null;
  const availableCount = rawPayload.available_count;
  if (
    typeof availableCount !== "number" ||
    !Number.isInteger(availableCount) ||
    availableCount < 0
  ) {
    return null;
  }

  const credits = rawPayload.credits.map(parseCredit);
  if (credits.some((credit) => credit === null)) return null;
  return { credits: credits as CodexResetCredit[], availableCount };
};

const parseConsumeResult = (rawPayload: unknown): CodexResetCreditResult | null => {
  if (!isRecord(rawPayload)) return null;
  const outcome = requiredString(rawPayload.code)?.toLowerCase();
  if (
    outcome !== "reset" &&
    outcome !== "nothing_to_reset" &&
    outcome !== "no_credit" &&
    outcome !== "already_redeemed"
  ) {
    return null;
  }
  const windowsReset = rawPayload.windows_reset;
  if (typeof windowsReset !== "number" || !Number.isInteger(windowsReset) || windowsReset < 0) {
    return null;
  }
  return { outcome, windowsReset };
};

const requestError = (error: unknown): CodexResetCreditError => {
  const detail = error instanceof Error ? error.message : String(error);
  if (/\(404\)|404 page not found/i.test(detail)) {
    return new CodexResetCreditError("unsupported", detail);
  }
  return new CodexResetCreditError("request_failed", detail);
};

export const fetchCodexResetCredits = async (
  file: AuthFileItem,
): Promise<CodexResetCreditsDetails> => {
  let rawPayload: unknown;
  try {
    rawPayload = await authFilesApi.getCodexResetCredits(file.name);
  } catch (error: unknown) {
    throw requestError(error);
  }
  const parsed = parseCodexResetCreditsDetails(rawPayload);
  if (!parsed) throw new CodexResetCreditError("invalid_response");
  return parsed;
};

export const consumeCodexResetCredit = async (
  file: AuthFileItem,
  request: { creditId: string; idempotencyKey: string },
): Promise<CodexResetCreditResult> => {
  const creditId = request.creditId.trim();
  const idempotencyKey = request.idempotencyKey.trim();
  if (!creditId || !idempotencyKey) throw new CodexResetCreditError("invalid_response");

  let rawPayload: unknown;
  try {
    rawPayload = await authFilesApi.consumeCodexResetCredit({
      name: file.name,
      credit_id: creditId,
      idempotency_key: idempotencyKey,
    });
  } catch (error: unknown) {
    throw requestError(error);
  }
  const parsed = parseConsumeResult(rawPayload);
  if (!parsed) throw new CodexResetCreditError("invalid_response");
  return parsed;
};
