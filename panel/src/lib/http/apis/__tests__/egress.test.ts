import { beforeEach, describe, expect, test, vi } from "vitest";

const getMock = vi.fn();
const postMock = vi.fn();
const patchMock = vi.fn();
const deleteMock = vi.fn();
const putMock = vi.fn();

vi.mock("@/lib/http/client", () => ({
  apiClient: {
    get: getMock,
    post: postMock,
    patch: patchMock,
    delete: deleteMock,
    put: putMock,
  },
}));

describe("egressApi", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
    patchMock.mockReset();
    deleteMock.mockReset();
    putMock.mockReset();
  });

  test("normalizes the per-auth fail-closed overview", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    getMock.mockResolvedValue({
      enabled: true,
      revision: "rev-7",
      scope: "application_egress",
      policy: {
        binding_mode: "exclusive",
        failure_mode: "fail_closed",
        readiness_scope: "application_egress",
        host_kill_switch_enforced: false,
      },
      counts: {
        endpoints: "5",
        enabled_endpoints: 4,
        bindings: 2,
        codex_auths: 3,
        bound_codex_auths: 2,
        unbound_codex_auths: 1,
        missing_account_id: 0,
        bound_endpoint_not_ready: 1,
      },
      readiness: {
        ready: false,
        ready_to_enable: false,
        codex_oauth_allowed: false,
        blockers: [{ code: "bound_endpoint_not_ready", message: "not ready" }],
        warnings: [{ code: "runtime_disabled", message: "disabled" }],
      },
    });

    const overview = await egressApi.getOverview();

    expect(overview).toEqual({
      enabled: true,
      revision: "rev-7",
      scope: "application_egress",
      policy: {
        bindingMode: "exclusive",
        failureMode: "fail_closed",
        readinessScope: "application_egress",
        hostKillSwitchEnforced: false,
      },
      counts: {
        endpoints: 5,
        enabledEndpoints: 4,
        bindings: 2,
        codexAuths: 3,
        boundCodexAuths: 2,
        unboundCodexAuths: 1,
        missingAccountId: 0,
        boundEndpointNotReady: 1,
      },
      readiness: {
        scope: "application_egress",
        verdict: "blocked",
        readyToEnable: false,
        codexOAuthAllowed: false,
        blockers: [{ code: "bound_endpoint_not_ready", message: "not ready" }],
        warnings: [{ code: "runtime_disabled", message: "disabled" }],
        notEvaluated: [],
      },
    });
  });

  test("normalizes standalone endpoints and runtime readiness", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    getMock.mockResolvedValue({
      items: [
        {
          id: "hk-socks",
          name: "Hong Kong",
          protocol: "SOCKS5",
          host: "10.77.0.2",
          port: "1080",
          enabled: true,
          username: "relay",
          has_credentials: true,
          status: "healthy",
          latency_ms: "31",
          observed_public_ip: "203.0.113.8",
          expected_public_ip: "203.0.113.8",
          eligible: true,
          runtime_ready: true,
          eligibility: {
            eligible: true,
            runtime_ready: true,
            health_fresh: true,
            public_ip_matches: true,
            duplicate_public_ip: false,
            reasons: [],
          },
        },
        {
          id: "unsupported-https",
          name: "Unsupported HTTPS CONNECT",
          protocol: "https",
          host: "10.77.0.3",
          port: 443,
        },
        { id: "", name: "invalid" },
      ],
    });

    await expect(egressApi.listEndpoints()).resolves.toEqual([
      {
        id: "hk-socks",
        name: "Hong Kong",
        protocol: "socks5",
        host: "10.77.0.2",
        port: 1080,
        enabled: true,
        username: "relay",
        hasCredentials: true,
        status: "healthy",
        latencyMs: 31,
        publicIp: "203.0.113.8",
        expectedPublicIp: "203.0.113.8",
        eligible: true,
        runtimeReady: true,
        eligibility: {
          selectable: true,
          eligible: true,
          runtimeReady: true,
          healthFresh: true,
          publicIpMatches: true,
          duplicatePublicIp: false,
          reasonCodes: [],
        },
      },
    ]);
  });

  test("serializes standalone endpoint CRUD", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    postMock.mockResolvedValue({});
    patchMock.mockResolvedValue({});
    deleteMock.mockResolvedValue({});

    await egressApi.createEndpoint({
      name: "Hong Kong",
      protocol: "socks5",
      host: "10.77.0.2",
      port: 1080,
      enabled: true,
      expectedPublicIp: "203.0.113.8",
      username: "relay",
      password: "secret",
    });
    await egressApi.updateEndpoint("hk/socks", { clearCredentials: true });
    await egressApi.checkEndpoint("hk/socks");
    await egressApi.deleteEndpoint("hk/socks");

    expect(postMock).toHaveBeenNthCalledWith(1, "/egress/endpoints", {
      name: "Hong Kong",
      protocol: "socks5",
      host: "10.77.0.2",
      port: 1080,
      enabled: true,
      expected_public_ip: "203.0.113.8",
      username: "relay",
      password: "secret",
    });
    expect(patchMock).toHaveBeenCalledWith("/egress/endpoints/hk%2Fsocks", {
      username: "",
      password: "",
    });
    expect(postMock).toHaveBeenNthCalledWith(2, "/egress/endpoints/hk%2Fsocks/check");
    expect(deleteMock).toHaveBeenCalledWith("/egress/endpoints/hk%2Fsocks");
  });

  test("rejects unsupported HTTPS CONNECT endpoint inputs before the request", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");

    expect(() =>
      egressApi.createEndpoint({
        name: "Unsupported HTTPS CONNECT",
        protocol: "https" as never,
        host: "10.77.0.3",
        port: 443,
        enabled: true,
        expectedPublicIp: "198.51.100.46",
      }),
    ).toThrow("Unsupported egress protocol: https");
    expect(postMock).not.toHaveBeenCalled();
  });

  test("previews and atomically applies fixed endpoint assignments including unbind", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    postMock.mockResolvedValueOnce({
      revision: "rev-7",
      assignments: [
        { identity: "codex:a", endpoint_id: "" },
        { identity: "codex:b", endpoint_id: "sg" },
      ],
      conflicts: [],
      valid: true,
    });
    putMock.mockResolvedValueOnce({ revision: "rev-8", applied: 2 });

    const assignments = [
      { identity: "codex:a", endpointId: "" },
      { identity: "codex:b", endpointId: "sg" },
    ];
    await expect(egressApi.previewBindings(assignments)).resolves.toMatchObject({
      expectedRevision: "rev-7",
      assignments,
      changeCount: 2,
      valid: true,
    });
    await expect(egressApi.applyBindings(assignments, "rev-7", true)).resolves.toEqual({
      revision: "rev-8",
      applied: 2,
    });
    expect(putMock).toHaveBeenCalledWith("/egress/bindings/batch", {
      assignments: [
        { identity: "codex:a", endpoint_id: "" },
        { identity: "codex:b", endpoint_id: "sg" },
      ],
      revision: "rev-7",
      confirmed: true,
    });
  });

  test("keeps confirmed endpoint impact actions", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    postMock
      .mockResolvedValueOnce({
        endpoint_id: "hk/socks",
        action: "delete",
        revision: "rev-7",
        binding_count: 2,
        binding_identities: ["codex:a", "codex:b"],
        allowed: true,
        blockers: [],
      })
      .mockResolvedValueOnce({});

    await expect(egressApi.endpointImpact("hk/socks", "delete")).resolves.toMatchObject({
      endpointId: "hk/socks",
      action: "delete",
      expectedRevision: "rev-7",
      affectedBindings: 2,
      blocked: false,
    });
    await egressApi.endpointAction("hk/socks", "delete", "rev-7", true);

    expect(postMock).toHaveBeenNthCalledWith(2, "/egress/endpoints/hk%2Fsocks/actions", {
      action: "delete",
      revision: "rev-7",
      confirmed: true,
    });
  });
});
