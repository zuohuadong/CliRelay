import { useCallback, useRef, useState } from "react";
import type { AuthFileItem } from "@/lib/http/types";
import {
  CodexResetCreditError,
  consumeCodexResetCredit,
  fetchCodexResetCredits,
  type CodexResetCredit,
  type CodexResetCreditOutcome,
} from "@/modules/quota/quota-codex-reset";

export interface CodexResetCreditsState {
  open: boolean;
  file: AuthFileItem | null;
  loading: boolean;
  consuming: boolean;
  credits: CodexResetCredit[];
  availableCount: number;
  selectedCreditId: string;
  errorCode: string | null;
  errorDetail: string | null;
  outcome: CodexResetCreditOutcome | null;
  windowsReset: number;
  verificationWarning: boolean;
}

const initialState: CodexResetCreditsState = {
  open: false,
  file: null,
  loading: false,
  consuming: false,
  credits: [],
  availableCount: 0,
  selectedCreditId: "",
  errorCode: null,
  errorDetail: null,
  outcome: null,
  windowsReset: 0,
  verificationWarning: false,
};

const availableCredits = (credits: CodexResetCredit[]): CodexResetCredit[] =>
  credits.filter((credit) => credit.status === "available");

const memoryIdempotencyKeys = new Map<string, string>();

const idempotencyStorageKey = (file: AuthFileItem, creditId: string): string => {
  const authIndex = String(file.auth_index ?? file.authIndex ?? file.name).trim();
  return `codex-reset-credit:${authIndex}:${creditId}`;
};

const createIdempotencyKey = (): string => {
  if (typeof globalThis.crypto.randomUUID === "function") {
    return globalThis.crypto.randomUUID();
  }
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
};

const getOrCreateIdempotencyKey = (file: AuthFileItem, creditId: string): string => {
  const storageKey = idempotencyStorageKey(file, creditId);
  try {
    const stored = window.sessionStorage.getItem(storageKey)?.trim();
    if (stored) return stored;
  } catch {
    // 浏览器隐私策略可能禁用 sessionStorage；内存副本仍能保证当前页面重试幂等。
  }
  const fallback = memoryIdempotencyKeys.get(storageKey);
  if (fallback) return fallback;
  const created = createIdempotencyKey();
  try {
    window.sessionStorage.setItem(storageKey, created);
  } catch {
    memoryIdempotencyKeys.set(storageKey, created);
  }
  return created;
};

const clearIdempotencyKey = (file: AuthFileItem, creditId: string) => {
  const storageKey = idempotencyStorageKey(file, creditId);
  memoryIdempotencyKeys.delete(storageKey);
  try {
    window.sessionStorage.removeItem(storageKey);
  } catch {
    // sessionStorage 不可用时，清理内存副本即可。
  }
};

const errorState = (error: unknown): Pick<CodexResetCreditsState, "errorCode" | "errorDetail"> => {
  if (error instanceof CodexResetCreditError) {
    return { errorCode: error.code, errorDetail: error.message };
  }
  return {
    errorCode: "request_failed",
    errorDetail: error instanceof Error ? error.message : String(error),
  };
};

interface RedemptionVerification {
  credits: CodexResetCredit[];
  availableCount: number;
  warning: boolean;
}

const verifyRedemption = async ({
  file,
  creditId,
  outcome,
  previousCredits,
  previousAvailableCount,
  refreshQuota,
}: {
  file: AuthFileItem;
  creditId: string;
  outcome: CodexResetCreditOutcome;
  previousCredits: CodexResetCredit[];
  previousAvailableCount: number;
  refreshQuota: (file: AuthFileItem, provider: "codex") => Promise<void>;
}): Promise<RedemptionVerification> => {
  let details: Awaited<ReturnType<typeof fetchCodexResetCredits>>;
  try {
    details = await fetchCodexResetCredits(file);
  } catch {
    return { credits: previousCredits, availableCount: previousAvailableCount, warning: true };
  }
  const credits = availableCredits(details.credits);
  try {
    await refreshQuota(file, "codex");
  } catch {
    return { credits, availableCount: details.availableCount, warning: true };
  }
  const shouldDisappear = outcome === "reset" || outcome === "already_redeemed";
  const warning = shouldDisappear && credits.some((credit) => credit.id === creditId);
  if (!warning) clearIdempotencyKey(file, creditId);
  return { credits, availableCount: details.availableCount, warning };
};

export function useCodexResetCredits({
  refreshQuota,
}: {
  refreshQuota: (file: AuthFileItem, provider: "codex") => Promise<void>;
}) {
  const [state, setState] = useState<CodexResetCreditsState>(initialState);
  const requestVersionRef = useRef(0);

  const close = useCallback(() => {
    setState((current) => (current.consuming ? current : initialState));
  }, []);

  const openForFile = useCallback(async (file: AuthFileItem) => {
    const requestVersion = requestVersionRef.current + 1;
    requestVersionRef.current = requestVersion;
    setState({ ...initialState, open: true, file, loading: true });
    try {
      const details = await fetchCodexResetCredits(file);
      if (requestVersionRef.current !== requestVersion) return;
      const credits = availableCredits(details.credits);
      setState((current) => ({
        ...current,
        loading: false,
        credits,
        availableCount: details.availableCount,
        selectedCreditId: credits[0]?.id ?? "",
      }));
    } catch (error: unknown) {
      if (requestVersionRef.current !== requestVersion) return;
      setState((current) => ({
        ...current,
        loading: false,
        ...errorState(error),
      }));
    }
  }, []);

  const selectCredit = useCallback((creditId: string) => {
    setState((current) => ({
      ...current,
      selectedCreditId: creditId,
      errorCode: null,
      errorDetail: null,
      outcome: null,
      windowsReset: 0,
      verificationWarning: false,
    }));
  }, []);

  const redeemSelected = useCallback(async () => {
    const file = state.file;
    const creditId = state.selectedCreditId.trim();
    if (!file || !creditId || state.consuming) return;

    const idempotencyKey = getOrCreateIdempotencyKey(file, creditId);
    setState((current) => ({
      ...current,
      consuming: true,
      errorCode: null,
      errorDetail: null,
      outcome: null,
      verificationWarning: false,
    }));

    try {
      const result = await consumeCodexResetCredit(file, { creditId, idempotencyKey });
      const verification = await verifyRedemption({
        file,
        creditId,
        outcome: result.outcome,
        previousCredits: state.credits,
        previousAvailableCount: state.availableCount,
        refreshQuota,
      });

      setState((current) => ({
        ...current,
        consuming: false,
        credits: verification.credits,
        availableCount: verification.availableCount,
        selectedCreditId: verification.credits[0]?.id ?? "",
        outcome: result.outcome,
        windowsReset: result.windowsReset,
        verificationWarning: verification.warning,
      }));
    } catch (error: unknown) {
      setState((current) => ({
        ...current,
        consuming: false,
        ...errorState(error),
      }));
    }
  }, [
    refreshQuota,
    state.availableCount,
    state.consuming,
    state.credits,
    state.file,
    state.selectedCreditId,
  ]);

  return { state, close, openForFile, selectCredit, redeemSelected };
}
