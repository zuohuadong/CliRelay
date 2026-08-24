import { apiClient } from "@/lib/http/client";

export type EgressProtocol = "socks5" | "http";
export type EgressEndpointSharingMode = "exclusive" | "shared";
export type EgressEndpointStatus = "unknown" | "healthy" | "unhealthy";
export type EgressReadinessVerdict = "ready" | "blocked";
export type EgressEndpointAction = "disable" | "delete";

export interface EgressReadinessIssue {
  code: string;
  message: string;
}

export interface EgressReadiness {
  scope: string;
  verdict: EgressReadinessVerdict;
  readyToEnable: boolean;
  codexOAuthAllowed: boolean;
  blockers: EgressReadinessIssue[];
  warnings: EgressReadinessIssue[];
  notEvaluated: EgressReadinessIssue[];
}

export interface EgressPolicy {
  bindingMode: "exclusive" | "shared" | "per_endpoint";
  failureMode: "fail_closed";
  readinessScope: string;
  hostKillSwitchEnforced: boolean;
}

export interface EgressOverview {
  enabled: boolean;
  revision: string;
  scope: string;
  policy: EgressPolicy;
  readiness: EgressReadiness;
  counts: {
    endpoints: number;
    enabledEndpoints: number;
    bindings: number;
    codexAuths: number;
    boundCodexAuths: number;
    unboundCodexAuths: number;
    missingAccountId: number;
    boundEndpointNotReady: number;
  };
}

export interface EgressEndpointEligibility {
  selectable: boolean;
  eligible: boolean;
  runtimeReady: boolean;
  healthFresh: boolean;
  publicIpMatches: boolean;
  duplicatePublicIp: boolean;
  reasonCodes: string[];
}

export interface EgressEndpoint {
  id: string;
  name: string;
  protocol: EgressProtocol;
  host: string;
  port: number;
  enabled: boolean;
  sharingMode?: EgressEndpointSharingMode;
  hasCredentials?: boolean;
  username?: string;
  status: EgressEndpointStatus;
  latencyMs?: number;
  publicIp?: string;
  expectedPublicIp?: string;
  lastCheckedAt?: string;
  error?: string;
  eligible: boolean;
  runtimeReady: boolean;
  eligibility: EgressEndpointEligibility;
}

export interface EgressBinding {
  identity: string;
  provider?: "codex" | "antigravity" | string;
  authId: string;
  accountLabel: string;
  planType?: string;
  endpointId: string;
  bound: boolean;
  endpointName?: string;
  updatedAt?: string;
  error?: string;
}

export interface EgressEndpointInput {
  name: string;
  protocol: EgressProtocol;
  host: string;
  port: number;
  enabled: boolean;
  sharingMode?: EgressEndpointSharingMode;
  expectedPublicIp?: string;
  username?: string;
  password?: string;
  clearCredentials?: boolean;
}

export interface EgressBindingAssignment {
  identity: string;
  endpointId: string;
  authFileId?: string;
}

export interface EgressBindingPreview {
  expectedRevision: string;
  assignments: EgressBindingAssignment[];
  changeCount: number;
  affectedAccounts: string[];
  blockers: EgressReadinessIssue[];
  warnings: EgressReadinessIssue[];
  valid: boolean;
}

export interface EgressBindingApplyResult {
  revision: string;
  applied: number;
}

export interface EgressEndpointImpact {
  endpointId: string;
  action: EgressEndpointAction;
  expectedRevision: string;
  affectedBindings: number;
  affectedAccounts: string[];
  blocked: boolean;
  reason?: string;
}

type UnknownRecord = Record<string, unknown>;

const asRecord = (value: unknown): UnknownRecord =>
  value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as UnknownRecord)
    : {};

const normalizeString = (value: unknown): string => (typeof value === "string" ? value.trim() : "");

const normalizeOptionalString = (value: unknown): string | undefined => {
  const normalized = normalizeString(value);
  return normalized || undefined;
};

const normalizeBoolean = (value: unknown, fallback = false): boolean =>
  typeof value === "boolean" ? value : fallback;

const normalizeNumber = (value: unknown, fallback = 0): number => {
  const numberValue =
    typeof value === "number" ? value : typeof value === "string" ? Number(value) : NaN;
  return Number.isFinite(numberValue) ? numberValue : fallback;
};

const normalizeStringArray = (value: unknown): string[] =>
  Array.isArray(value)
    ? value
        .map(normalizeString)
        .filter((item, index, items) => item && items.indexOf(item) === index)
    : [];

const normalizeIssues = (value: unknown): EgressReadinessIssue[] =>
  Array.isArray(value)
    ? value
        .map((item) => {
          if (typeof item === "string") {
            const message = item.trim();
            return message ? { code: message, message } : null;
          }
          const record = asRecord(item);
          const message = normalizeString(record.message ?? record.detail ?? record.code);
          if (!message) return null;
          return { code: normalizeString(record.code) || "warning", message };
        })
        .filter((item): item is EgressReadinessIssue => item !== null)
    : [];

const normalizeList = <T>(value: unknown, normalizer: (item: UnknownRecord) => T | null): T[] => {
  const record = asRecord(value);
  const items = Array.isArray(value) ? value : Array.isArray(record.items) ? record.items : [];
  return items.map((item) => normalizer(asRecord(item))).filter((item): item is T => item !== null);
};

const normalizeProtocol = (value: unknown): EgressProtocol | null => {
  const protocol = normalizeString(value).toLowerCase();
  return protocol === "socks5" || protocol === "http" ? protocol : null;
};

const requireProtocol = (value: unknown): EgressProtocol => {
  const protocol = normalizeProtocol(value);
  if (protocol) return protocol;
  throw new Error(`Unsupported egress protocol: ${normalizeString(value).toLowerCase()}`);
};

const normalizeSharingMode = (value: unknown): EgressEndpointSharingMode =>
  normalizeString(value).toLowerCase() === "shared" ? "shared" : "exclusive";

const normalizeStatus = (value: unknown): EgressEndpointStatus => {
  const status = normalizeString(value).toLowerCase();
  if (status === "healthy" || status === "ok" || status === "online") return "healthy";
  if (
    status === "unhealthy" ||
    status === "failed" ||
    status === "offline" ||
    status === "ip_mismatch" ||
    status === "duplicate_public_ip"
  ) {
    return "unhealthy";
  }
  return "unknown";
};

export const normalizeEgressOverview = (value: unknown): EgressOverview => {
  const raw = asRecord(value);
  const counts = asRecord(raw.counts);
  const policy = asRecord(raw.policy);
  const readiness = asRecord(raw.readiness);
  const ready = normalizeBoolean(
    readiness.ready ?? readiness.ready_to_enable ?? readiness.readyToEnable,
  );
  const scope = normalizeString(raw.scope ?? readiness.scope) || "application_egress";
  return {
    enabled: normalizeBoolean(raw.enabled ?? raw.runtime_enabled ?? raw.runtimeEnabled),
    revision: normalizeString(raw.revision ?? readiness.revision),
    scope,
    policy: {
      bindingMode: (() => {
        const bindingMode = normalizeString(
          policy.binding_mode ?? policy.bindingMode,
        ).toLowerCase();
        if (bindingMode === "shared" || bindingMode === "per_endpoint") return bindingMode;
        return "exclusive";
      })(),
      failureMode: "fail_closed",
      readinessScope: normalizeString(policy.readiness_scope ?? policy.readinessScope) || scope,
      hostKillSwitchEnforced: normalizeBoolean(
        policy.host_kill_switch_enforced ?? policy.hostKillSwitchEnforced,
      ),
    },
    readiness: {
      scope: normalizeString(readiness.scope) || scope,
      verdict: ready ? "ready" : "blocked",
      readyToEnable: normalizeBoolean(readiness.ready_to_enable ?? readiness.readyToEnable, ready),
      codexOAuthAllowed: normalizeBoolean(
        readiness.codex_oauth_allowed ?? readiness.codexOAuthAllowed,
      ),
      blockers: normalizeIssues(readiness.blockers ?? readiness.reasons),
      warnings: normalizeIssues(readiness.warnings),
      notEvaluated: normalizeIssues(readiness.not_evaluated ?? readiness.notEvaluated),
    },
    counts: {
      endpoints: normalizeNumber(counts.endpoints),
      enabledEndpoints: normalizeNumber(counts.enabled_endpoints ?? counts.enabledEndpoints),
      bindings: normalizeNumber(counts.bindings),
      codexAuths: normalizeNumber(counts.codex_auths ?? counts.codexAuths),
      boundCodexAuths: normalizeNumber(counts.bound_codex_auths ?? counts.boundCodexAuths),
      unboundCodexAuths: normalizeNumber(counts.unbound_codex_auths ?? counts.unboundCodexAuths),
      missingAccountId: normalizeNumber(counts.missing_account_id ?? counts.missingAccountId),
      boundEndpointNotReady: normalizeNumber(
        counts.bound_endpoint_not_ready ?? counts.boundEndpointNotReady,
      ),
    },
  };
};

export const normalizeEgressEndpoint = (raw: UnknownRecord): EgressEndpoint | null => {
  const id = normalizeString(raw.id);
  const name = normalizeString(raw.name);
  const host = normalizeString(raw.host);
  const port = normalizeNumber(raw.port);
  const protocol = normalizeProtocol(raw.protocol);
  if (!id || !name || !host || !protocol || port <= 0 || port > 65535) return null;
  const username = normalizeOptionalString(raw.username);
  const eligibility = asRecord(raw.eligibility ?? raw.readiness);
  const eligible = normalizeBoolean(raw.eligible ?? eligibility.eligible);
  const runtimeReady = normalizeBoolean(
    raw.runtime_ready ?? raw.runtimeReady ?? eligibility.runtime_ready ?? eligibility.runtimeReady,
  );
  return {
    id,
    name,
    protocol,
    host,
    port,
    enabled: normalizeBoolean(raw.enabled, true),
    sharingMode: normalizeSharingMode(raw.sharing_mode ?? raw.sharingMode),
    ...(typeof (raw.has_credentials ?? raw.hasCredentials) === "boolean"
      ? { hasCredentials: Boolean(raw.has_credentials ?? raw.hasCredentials) }
      : {}),
    ...(username ? { username } : {}),
    status: normalizeStatus(raw.status),
    ...(normalizeNumber(raw.latency_ms ?? raw.latencyMs, -1) >= 0
      ? { latencyMs: Math.round(normalizeNumber(raw.latency_ms ?? raw.latencyMs)) }
      : {}),
    ...(normalizeOptionalString(raw.public_ip ?? raw.publicIp ?? raw.observed_public_ip)
      ? {
          publicIp: normalizeOptionalString(
            raw.public_ip ?? raw.publicIp ?? raw.observed_public_ip,
          ),
        }
      : {}),
    ...(normalizeOptionalString(raw.expected_public_ip ?? raw.expectedPublicIp)
      ? {
          expectedPublicIp: normalizeOptionalString(raw.expected_public_ip ?? raw.expectedPublicIp),
        }
      : {}),
    ...(normalizeOptionalString(raw.last_checked_at ?? raw.lastCheckedAt)
      ? { lastCheckedAt: normalizeOptionalString(raw.last_checked_at ?? raw.lastCheckedAt) }
      : {}),
    ...(normalizeOptionalString(raw.error) ? { error: normalizeOptionalString(raw.error) } : {}),
    eligible,
    runtimeReady,
    eligibility: {
      selectable: runtimeReady,
      eligible,
      runtimeReady,
      healthFresh: normalizeBoolean(
        eligibility.health_fresh ?? eligibility.healthFresh,
        runtimeReady,
      ),
      publicIpMatches: normalizeBoolean(
        eligibility.public_ip_matches ?? eligibility.publicIpMatches,
        runtimeReady,
      ),
      duplicatePublicIp: normalizeBoolean(
        eligibility.duplicate_public_ip ?? eligibility.duplicatePublicIp,
      ),
      reasonCodes: normalizeStringArray(
        eligibility.reasons ?? eligibility.reason_codes ?? eligibility.reasonCodes,
      ),
    },
  };
};

export const normalizeEgressBinding = (raw: UnknownRecord): EgressBinding | null => {
  const identity = normalizeString(raw.identity ?? raw.stable_identity ?? raw.stableIdentity);
  const endpointId = normalizeString(raw.endpoint_id ?? raw.endpointId);
  const authId = normalizeString(raw.auth_id ?? raw.authId ?? raw.auth_file_id ?? raw.authFileId);
  const accountLabel = normalizeString(raw.account_label ?? raw.accountLabel);
  if (!identity && !authId && !accountLabel) return null;
  return {
    identity,
    provider: normalizeString(raw.provider) || identity.split(":", 1)[0] || "codex",
    authId,
    accountLabel: accountLabel || authId || identity,
    ...(normalizeOptionalString(raw.plan_type ?? raw.planType)
      ? { planType: normalizeOptionalString(raw.plan_type ?? raw.planType) }
      : {}),
    endpointId,
    bound: normalizeBoolean(raw.bound, Boolean(endpointId)),
    ...(normalizeOptionalString(raw.endpoint_name ?? raw.endpointName)
      ? { endpointName: normalizeOptionalString(raw.endpoint_name ?? raw.endpointName) }
      : {}),
    ...(normalizeOptionalString(raw.updated_at ?? raw.updatedAt)
      ? { updatedAt: normalizeOptionalString(raw.updated_at ?? raw.updatedAt) }
      : {}),
    ...(normalizeOptionalString(raw.error) ? { error: normalizeOptionalString(raw.error) } : {}),
  };
};

const serializeEndpointInput = (input: Partial<EgressEndpointInput>): UnknownRecord => ({
  ...(input.name !== undefined ? { name: input.name.trim() } : {}),
  ...(input.protocol !== undefined ? { protocol: requireProtocol(input.protocol) } : {}),
  ...(input.host !== undefined ? { host: input.host.trim() } : {}),
  ...(input.port !== undefined ? { port: input.port } : {}),
  ...(input.enabled !== undefined ? { enabled: input.enabled } : {}),
  ...(input.sharingMode !== undefined ? { sharing_mode: input.sharingMode } : {}),
  ...(input.expectedPublicIp !== undefined
    ? { expected_public_ip: input.expectedPublicIp.trim() }
    : {}),
  ...(input.clearCredentials
    ? { username: "", password: "" }
    : {
        ...(input.username?.trim() ? { username: input.username.trim() } : {}),
        ...(input.password ? { password: input.password } : {}),
      }),
});

const normalizeAssignment = (value: unknown): EgressBindingAssignment | null => {
  const raw = asRecord(value);
  const identity = normalizeString(raw.identity);
  if (!identity) return null;
  return {
    identity,
    endpointId: normalizeString(raw.endpoint_id ?? raw.endpointId),
    ...(normalizeOptionalString(raw.auth_file_id ?? raw.authFileId)
      ? { authFileId: normalizeOptionalString(raw.auth_file_id ?? raw.authFileId) }
      : {}),
  };
};

const serializeAssignments = (assignments: EgressBindingAssignment[]) =>
  assignments.map((assignment) => ({
    identity: assignment.identity.trim(),
    endpoint_id: assignment.endpointId.trim(),
    ...(assignment.authFileId?.trim() ? { auth_file_id: assignment.authFileId.trim() } : {}),
  }));

const normalizeBindingPreview = (value: unknown): EgressBindingPreview => {
  const raw = asRecord(value);
  const assignments = Array.isArray(raw.assignments)
    ? raw.assignments
        .map(normalizeAssignment)
        .filter((item): item is EgressBindingAssignment => item !== null)
    : [];
  const blockers = normalizeIssues(raw.blockers ?? raw.conflicts);
  return {
    expectedRevision: normalizeString(
      raw.expected_revision ?? raw.expectedRevision ?? raw.revision,
    ),
    assignments,
    changeCount: normalizeNumber(raw.change_count ?? raw.changeCount, assignments.length),
    affectedAccounts: normalizeStringArray(raw.affected_accounts ?? raw.affectedAccounts),
    blockers,
    warnings: normalizeIssues(raw.warnings),
    valid: normalizeBoolean(raw.valid, blockers.length === 0),
  };
};

const normalizeEndpointImpact = (value: unknown): EgressEndpointImpact => {
  const raw = asRecord(value);
  const action = normalizeString(raw.action) === "disable" ? "disable" : "delete";
  const blockers = normalizeIssues(raw.blockers);
  return {
    endpointId: normalizeString(raw.endpoint_id ?? raw.endpointId),
    action,
    expectedRevision: normalizeString(
      raw.expected_revision ?? raw.expectedRevision ?? raw.revision,
    ),
    affectedBindings: normalizeNumber(
      raw.affected_bindings ?? raw.affectedBindings ?? raw.binding_count,
    ),
    affectedAccounts: normalizeStringArray(
      raw.affected_accounts ?? raw.affectedAccounts ?? raw.binding_identities,
    ),
    blocked: normalizeBoolean(raw.blocked, !normalizeBoolean(raw.allowed, blockers.length === 0)),
    ...(normalizeOptionalString(raw.reason) || blockers.length > 0
      ? {
          reason:
            normalizeOptionalString(raw.reason) ??
            blockers.map((blocker) => blocker.message).join("; "),
        }
      : {}),
  };
};

const encodePath = (value: string): string => encodeURIComponent(value.trim());

export const egressApi = {
  async getOverview(): Promise<EgressOverview> {
    return normalizeEgressOverview(await apiClient.get<unknown>("/egress/overview"));
  },
  async listEndpoints(): Promise<EgressEndpoint[]> {
    return normalizeList(
      await apiClient.get<unknown>("/egress/endpoints"),
      normalizeEgressEndpoint,
    );
  },
  createEndpoint(input: EgressEndpointInput) {
    return apiClient.post("/egress/endpoints", serializeEndpointInput(input));
  },
  updateEndpoint(id: string, input: Partial<EgressEndpointInput>) {
    return apiClient.patch(`/egress/endpoints/${encodePath(id)}`, serializeEndpointInput(input));
  },
  deleteEndpoint(id: string) {
    return apiClient.delete(`/egress/endpoints/${encodePath(id)}`);
  },
  checkEndpoint(id: string) {
    return apiClient.post(`/egress/endpoints/${encodePath(id)}/check`);
  },
  async listBindings(): Promise<EgressBinding[]> {
    return normalizeList(await apiClient.get<unknown>("/egress/bindings"), normalizeEgressBinding);
  },
  async previewBindings(assignments: EgressBindingAssignment[]): Promise<EgressBindingPreview> {
    return normalizeBindingPreview(
      await apiClient.post<unknown>("/egress/bindings/preview", {
        assignments: serializeAssignments(assignments),
      }),
    );
  },
  async applyBindings(
    assignments: EgressBindingAssignment[],
    expectedRevision: string,
    confirmed: boolean,
  ): Promise<EgressBindingApplyResult> {
    const raw = asRecord(
      await apiClient.put<unknown>("/egress/bindings/batch", {
        assignments: serializeAssignments(assignments),
        revision: expectedRevision.trim(),
        confirmed,
      }),
    );
    return {
      revision: normalizeString(raw.revision),
      applied: normalizeNumber(raw.applied ?? raw.applied_count ?? raw.appliedCount),
    };
  },
  async endpointImpact(id: string, action: EgressEndpointAction): Promise<EgressEndpointImpact> {
    return normalizeEndpointImpact(
      await apiClient.post<unknown>(`/egress/endpoints/${encodePath(id)}/impact`, { action }),
    );
  },
  endpointAction(
    id: string,
    action: EgressEndpointAction,
    expectedRevision: string,
    confirmed: boolean,
  ) {
    return apiClient.post(`/egress/endpoints/${encodePath(id)}/actions`, {
      action,
      revision: expectedRevision.trim(),
      confirmed,
    });
  },
};
