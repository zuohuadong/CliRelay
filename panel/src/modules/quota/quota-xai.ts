import type { QuotaItem } from "@/modules/quota/quota-types";
import {
  clampPercent,
  isRecord,
  normalizeNumberValue,
  normalizeStringValue,
  parseResetTimeToMs,
} from "@/modules/quota/quota-normalizers";

const WEEK_SECONDS = 7 * 24 * 60 * 60;
const MONTH_SECONDS = 30 * 24 * 60 * 60;

export type XaiBillingPayload = {
  config?: Record<string, unknown> | null;
  usage?: Record<string, unknown> | null;
  [key: string]: unknown;
};

export const parseXaiBillingPayload = (payload: unknown): XaiBillingPayload | null => {
  if (payload === undefined || payload === null) return null;
  if (typeof payload === "string") {
    const trimmed = payload.trim();
    if (!trimmed) return null;
    try {
      const parsed = JSON.parse(trimmed) as unknown;
      return isRecord(parsed) ? (parsed as XaiBillingPayload) : null;
    } catch {
      return null;
    }
  }
  return isRecord(payload) ? (payload as XaiBillingPayload) : null;
};

const unwrapNumber = (value: unknown): number | null => {
  const direct = normalizeNumberValue(value);
  if (direct !== null) return direct;
  if (!isRecord(value)) return null;
  return unwrapNumber(value.val ?? value.value ?? value.float ?? value.int);
};

const unwrapString = (value: unknown): string | null => {
  const direct = normalizeStringValue(value);
  if (direct) return direct;
  if (!isRecord(value)) return null;
  return unwrapString(value.val ?? value.value ?? value.string);
};

const parseXaiTimeToMs = (value: unknown): number | undefined => {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "number" && Number.isFinite(value)) {
    if (value <= 0) return undefined;
    return value >= 1e12 ? value : value * 1000;
  }
  if (typeof value === "string") {
    const asNumber = normalizeNumberValue(value);
    if (asNumber !== null && /^\d+(\.\d+)?$/.test(value.trim())) {
      return parseXaiTimeToMs(asNumber);
    }
    return parseResetTimeToMs(value);
  }
  if (!isRecord(value)) return undefined;
  const seconds = unwrapNumber(value.seconds ?? value.Seconds);
  if (seconds !== null && seconds > 0) {
    const nanos = unwrapNumber(value.nanos ?? value.Nanos) ?? 0;
    return seconds * 1000 + Math.floor(nanos / 1e6);
  }
  return parseXaiTimeToMs(value.val ?? value.value ?? value.end ?? value.time);
};

const usedPercentFromValue = (value: number): number => {
  if (!Number.isFinite(value) || value < 0) return 0;
  if (value > 0 && value <= 1) return value * 100;
  return value;
};

const remainingPercentFromUsed = (usedPercent: number): number =>
  Math.round(clampPercent(100 - usedPercent));

const formatCredits = (value: number): string => Math.round(value).toLocaleString("en-US");

const resolvePeriodType = (
  rawType: string | null,
  hasMonthlyLimit: boolean,
): "weekly" | "monthly" => {
  const normalized = rawType?.toLowerCase() ?? "";
  if (normalized.includes("week")) return "weekly";
  if (normalized.includes("month")) return "monthly";
  if (hasMonthlyLimit) return "monthly";
  return "weekly";
};

const resolvePlanTypeCandidate = (...values: unknown[]): string | null => {
  for (const value of values) {
    const text = unwrapString(value);
    if (!text) continue;
    const trimmed = text.replace(/^subscription:/i, "").trim();
    if (trimmed) return trimmed;
  }
  return null;
};

export const parseXaiPlanType = (payload: unknown): string | null => {
  const parsed = parseXaiBillingPayload(payload);
  if (!parsed) return null;
  const config = isRecord(parsed.config) ? parsed.config : null;
  return resolvePlanTypeCandidate(
    parsed.subscription_tier_display,
    parsed.subscriptionTierDisplay,
    parsed.subscription_tier,
    parsed.subscriptionTier,
    parsed.entitlement_status,
    parsed.entitlementStatus,
    parsed.plan_type,
    parsed.planType,
    parsed.tier,
    parsed.plan,
    config?.subscription_tier_display,
    config?.subscriptionTierDisplay,
    config?.subscription_tier,
    config?.subscriptionTier,
    config?.entitlement_status,
    config?.entitlementStatus,
    config?.tier,
    config?.plan,
  );
};

export const buildXaiItems = (payload: XaiBillingPayload): QuotaItem[] => {
  const config = isRecord(payload.config) ? payload.config : null;
  const usage = isRecord(payload.usage) ? payload.usage : null;
  const currentPeriod =
    [config?.currentPeriod, config?.current_period, payload.currentPeriod, payload.current_period].find(
      isRecord,
    ) ?? null;

  const limit = unwrapNumber(
    config?.monthlyLimit ??
      config?.monthly_limit ??
      payload.monthlyLimit ??
      payload.monthly_limit,
  );
  const used = unwrapNumber(
    config?.used ?? usage?.creditUsage ?? usage?.credit_usage ?? payload.used,
  );
  const remainingCredits = unwrapNumber(
    payload.remainingCredits ??
      payload.remaining_credits ??
      config?.remainingCredits ??
      config?.remaining_credits,
  );
  const rawUsedPercent = unwrapNumber(
    config?.creditUsagePercent ??
      config?.credit_usage_percent ??
      payload.creditUsagePercent ??
      payload.credit_usage_percent ??
      payload.usage_percent ??
      usage?.creditUsagePercent,
  );
  const resetAtMs = parseXaiTimeToMs(
    currentPeriod?.end ??
      config?.billingPeriodEnd ??
      config?.billing_period_end ??
      payload.billingPeriodEnd ??
      payload.billing_period_end,
  );
  const periodType = resolvePeriodType(
    unwrapString(currentPeriod?.type ?? payload.period_type ?? payload.periodType),
    limit !== null && limit > 0,
  );

  let remainingPercent: number | null = null;
  if (rawUsedPercent !== null) {
    remainingPercent = remainingPercentFromUsed(usedPercentFromValue(rawUsedPercent));
  } else if (limit !== null && limit > 0 && used !== null) {
    remainingPercent = remainingPercentFromUsed((used / limit) * 100);
  } else if (resetAtMs !== undefined) {
    // Protobuf JSON omits zero-valued usage fields. A live period means 0 used.
    remainingPercent = 100;
  } else if (remainingCredits !== null && remainingCredits >= 0 && (limit === null || limit <= 0)) {
    remainingPercent = remainingCredits > 0 ? 100 : 0;
  }

  const resolvedRemaining =
    remainingCredits !== null
      ? remainingCredits
      : limit !== null && used !== null
        ? Math.max(0, limit - used)
        : null;
  const meta =
    resolvedRemaining !== null && limit !== null && limit > 0
      ? `${formatCredits(resolvedRemaining)} / ${formatCredits(limit)}`
      : resolvedRemaining !== null
        ? formatCredits(resolvedRemaining)
        : undefined;

  if (remainingPercent === null && resetAtMs === undefined && !meta) return [];

  const label =
    periodType === "monthly"
      ? "xai_quota.monthly"
      : remainingPercent === null && meta
        ? "xai_quota.credits"
        : "xai_quota.weekly";
  const key =
    periodType === "monthly" ? "xai_month" : remainingPercent === null ? "xai_credits" : "xai_week";


  return [
    {
      key,
      label,
      percent: remainingPercent,
      resetAtMs,
      windowSeconds: periodType === "monthly" ? MONTH_SECONDS : WEEK_SECONDS,
      meta,
    },
  ];
};
