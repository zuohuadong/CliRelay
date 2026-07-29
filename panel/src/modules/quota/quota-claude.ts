import type { QuotaItem } from "@/modules/quota/quota-types";
import {
  clampPercent,
  isRecord,
  normalizeNumberValue,
  normalizeStringValue,
  parseResetTimeToMs,
} from "@/modules/quota/quota-normalizers";

type ClaudeUsageWindow = {
  utilization?: number | string;
  resets_at?: string;
  resetsAt?: string;
};

type ClaudeScopedLimit = {
  kind?: string;
  group?: string;
  percent?: number | string;
  resets_at?: string;
  resetsAt?: string;
  scope?: {
    model?: {
      id?: string;
      display_name?: string;
      displayName?: string;
    };
  };
};

export type ClaudeUsagePayload = {
  organization?: Record<string, unknown> | null;
  account?: Record<string, unknown> | null;
  five_hour?: ClaudeUsageWindow | null;
  seven_day?: ClaudeUsageWindow | null;
  seven_day_oauth_apps?: ClaudeUsageWindow | null;
  seven_day_opus?: ClaudeUsageWindow | null;
  seven_day_sonnet?: ClaudeUsageWindow | null;
  seven_day_cowork?: ClaudeUsageWindow | null;
  iguana_necktie?: ClaudeUsageWindow | null;
  limits?: ClaudeScopedLimit[] | null;
  extra_usage?: {
    is_enabled?: boolean;
    monthly_limit?: number | string;
    used_credits?: number | string;
    utilization?: number | string | null;
  } | null;
};

const claudeModelSlug = (value: string): string =>
  value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");

const CLAUDE_SCOPED_QUOTA_LABEL_PREFIX = "claude_quota.model_weekly::";

export const parseClaudeScopedQuotaLabel = (label: string): string | null => {
  const text = String(label ?? "");
  if (!text.startsWith(CLAUDE_SCOPED_QUOTA_LABEL_PREFIX)) return null;
  const modelName = text.slice(CLAUDE_SCOPED_QUOTA_LABEL_PREFIX.length).trim();
  return modelName || null;
};

export const parseClaudePlanType = (payload: unknown): string => {
  const parsed = parseClaudeUsagePayload(payload);
  if (!parsed || !isRecord(parsed)) return "";
  const organization = isRecord(parsed.organization) ? parsed.organization : null;
  const account = isRecord(parsed.account) ? parsed.account : null;
  const organizationType = normalizeStringValue(
    organization?.organization_type ?? organization?.organizationType,
  )?.toLowerCase();
  if (organizationType?.includes("enterprise")) return "enterprise";
  if (organizationType?.includes("team")) return "team";

  const tier = normalizeStringValue(
    organization?.rate_limit_tier ?? organization?.rateLimitTier,
  )?.toLowerCase();
  for (const candidate of ["max_20x", "max_5x", "max", "pro"] as const) {
    if (tier?.includes(candidate)) return candidate;
  }
  for (const candidate of ["max", "pro", "free"] as const) {
    if (organizationType?.includes(candidate)) return candidate;
  }
  if (account?.has_claude_max === true) return "max";
  if (account?.has_claude_pro === true) return "pro";
  return "";
};

const CLAUDE_USAGE_WINDOW_KEYS = [
  { key: "five_hour", id: "five_hour", label: "claude_quota.five_hour" },
  { key: "seven_day", id: "seven_day", label: "claude_quota.seven_day" },
  {
    key: "seven_day_oauth_apps",
    id: "seven_day_oauth_apps",
    label: "claude_quota.seven_day_oauth_apps",
  },
  { key: "seven_day_opus", id: "seven_day_opus", label: "claude_quota.seven_day_opus" },
  { key: "seven_day_sonnet", id: "seven_day_sonnet", label: "claude_quota.seven_day_sonnet" },
  { key: "seven_day_cowork", id: "seven_day_cowork", label: "claude_quota.seven_day_cowork" },
  { key: "iguana_necktie", id: "iguana_necktie", label: "claude_quota.iguana_necktie" },
] as const;

export const parseClaudeUsagePayload = (payload: unknown): ClaudeUsagePayload | null => {
  if (payload === undefined || payload === null) return null;
  if (typeof payload === "string") {
    const trimmed = payload.trim();
    if (!trimmed) return null;
    try {
      return JSON.parse(trimmed) as ClaudeUsagePayload;
    } catch {
      return null;
    }
  }
  return typeof payload === "object" ? (payload as ClaudeUsagePayload) : null;
};

const resolveRemainingPercent = (window?: ClaudeUsageWindow | null): number | null => {
  if (!window) return null;
  const utilization = normalizeNumberValue(window.utilization);
  return utilization === null ? null : clampPercent(100 - clampPercent(utilization));
};

export const buildClaudeItems = (payload: ClaudeUsagePayload): QuotaItem[] => {
  const items: QuotaItem[] = CLAUDE_USAGE_WINDOW_KEYS.flatMap((definition) => {
    const window = payload[definition.key];
    if (!window) return [];
    const percent = resolveRemainingPercent(window);
    const resetAtMs = parseResetTimeToMs(window.resets_at ?? window.resetsAt);
    if (percent === null && !resetAtMs) return [];
    return [
      {
        key: definition.id,
        label: definition.label,
        percent,
        resetAtMs,
      },
    ];
  });

  const seen = new Set(items.map((item) => item.key));
  for (const limit of payload.limits ?? []) {
    if (limit.kind !== "weekly_scoped" || limit.group !== "weekly") continue;
    const percentUsed = normalizeNumberValue(limit.percent);
    if (percentUsed === null) continue;
    const model = limit.scope?.model;
    const modelId = normalizeStringValue(model?.id) ?? "";
    const displayName = normalizeStringValue(model?.display_name ?? model?.displayName) ?? modelId;
    const slug = claudeModelSlug(modelId || displayName);
    if (
      !slug ||
      slug === "all-models" ||
      slug.endsWith("-all-models") ||
      claudeModelSlug(displayName) === "all-models"
    )
      continue;
    if (seen.has("seven_day_opus") && slug.includes("opus")) continue;
    if (seen.has("seven_day_sonnet") && slug.includes("sonnet")) continue;
    const key = `weekly_scoped_${slug}`;
    if (seen.has(key)) continue;
    seen.add(key);
    items.push({
      key,
      label: `${CLAUDE_SCOPED_QUOTA_LABEL_PREFIX}${displayName}`,
      percent: clampPercent(100 - clampPercent(percentUsed)),
      resetAtMs: parseResetTimeToMs(limit.resets_at ?? limit.resetsAt),
    });
  }

  const extra = payload.extra_usage;
  const extraUtilization = normalizeNumberValue(extra?.utilization);
  if (extra?.is_enabled && extraUtilization !== null) {
    const usedCredits = normalizeStringValue(extra.used_credits);
    const monthlyLimit = normalizeStringValue(extra.monthly_limit);
    const meta =
      usedCredits && monthlyLimit ? `${usedCredits} / ${monthlyLimit} credits` : undefined;
    items.push({
      key: "extra_usage",
      label: "claude_quota.extra_usage_label",
      percent: clampPercent(100 - clampPercent(extraUtilization)),
      meta,
    });
  }

  return items;
};
