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

  test("normalizes overview settings and counts", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    getMock.mockResolvedValue({
      enabled: true,
      headscale: {
        configured: true,
        reachable: true,
        enabled: true,
        url: " https://headscale.internal ",
        api_key_configured: true,
        service_tag: "tag:clirelay-egress",
        last_sync_at: "2026-07-10T10:00:00Z",
      },
      counts: {
        nodes: "4",
        online_nodes: 3,
        endpoints: 5,
        enabled_endpoints: 4,
        bindings: 8,
        accounts: 10,
        routable_accounts: 7,
        unbound_accounts: 2,
        missing_identity_accounts: 1,
      },
      policy: {
        binding_mode: "exclusive",
        node_freshness_ttl_seconds: 300,
        endpoint_check_ttl_seconds: 300,
      },
      revision: "rev-7",
      readiness: {
        scope: "application_egress",
        verdict: "blocked",
        ready_to_enable: false,
        codex_oauth_allowed: false,
        blockers: [{ code: "unbound_accounts", message: "2 accounts are unbound" }],
        warnings: ["Headscale sync is nearing its freshness limit"],
        not_evaluated: [],
      },
      local_endpoint_enabled: false,
    });

    await expect(egressApi.getOverview()).resolves.toEqual({
      enabled: true,
      headscale: {
        configured: true,
        reachable: true,
        url: "https://headscale.internal",
        apiKeyConfigured: true,
        serviceTag: "tag:clirelay-egress",
        lastSyncAt: "2026-07-10T10:00:00Z",
      },
      counts: {
        nodes: 4,
        onlineNodes: 3,
        endpoints: 5,
        enabledEndpoints: 4,
        bindings: 8,
        accounts: 10,
        routableAccounts: 7,
        unboundAccounts: 2,
        missingIdentityAccounts: 1,
      },
      policy: {
        bindingMode: "exclusive",
        nodeFreshnessTtlSeconds: 300,
        endpointCheckTtlSeconds: 300,
      },
      revision: "rev-7",
      readiness: {
        scope: "application_egress",
        verdict: "blocked",
        readyToEnable: false,
        codexOAuthAllowed: false,
        blockers: [{ code: "unbound_accounts", message: "2 accounts are unbound" }],
        warnings: [
          {
            code: "Headscale sync is nearing its freshness limit",
            message: "Headscale sync is nearing its freshness limit",
          },
        ],
        notEvaluated: [],
      },
      localEndpointEnabled: false,
    });
    expect(getMock).toHaveBeenCalledWith("/egress/overview");
  });

  test("normalizes a disabled runtime separately from prepared Headscale configuration", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    getMock.mockResolvedValue({
      enabled: false,
      headscale: {
        configured: true,
        reachable: true,
        url: "https://headscale.internal",
      },
      counts: {},
    });

    await expect(egressApi.getOverview()).resolves.toMatchObject({
      enabled: false,
      headscale: { configured: true, reachable: true },
    });
  });

  test("normalizes node, endpoint, and binding list payloads", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    getMock
      .mockResolvedValueOnce({
        items: [
          {
            id: "node-1",
            name: "egress-hk",
            given_name: "hk",
            addresses: ["100.64.0.2"],
            online: true,
            fresh: true,
            synced_at: "2026-07-10T09:59:40Z",
            sync_age_seconds: 20,
            tags: ["tag:clirelay-egress"],
            last_seen: "2026-07-10T10:00:00Z",
          },
          { id: "", name: "invalid" },
        ],
      })
      .mockResolvedValueOnce({
        items: [
          {
            id: "hk-socks",
            node_id: "node-1",
            name: "Hong Kong",
            protocol: "SOCKS5",
            host: "100.64.0.2",
            port: "1080",
            enabled: true,
            is_local: false,
            status: "healthy",
            latency_ms: "31",
            public_ip: "203.0.113.8",
            expected_public_ip: "203.0.113.8",
            eligibility: {
              state: "eligible",
              selectable: true,
              reason_codes: [],
              checked_age_seconds: 12,
              node_online: true,
              node_stale: false,
              binding_count: 1,
              exclusive_owner_identity: "codex:abc",
              duplicate_public_ip: false,
            },
          },
          {
            id: "origin-socks",
            node_id: "",
            name: "Origin server",
            protocol: "socks5",
            host: "127.0.0.1",
            port: 1081,
            enabled: false,
            local_server: true,
            status: "unknown",
          },
        ],
      })
      .mockResolvedValueOnce({
        items: [
          {
            identity: "codex:abc",
            auth_id: "codex-user.json",
            account_label: "user@example.com",
            endpoint_id: "hk-socks",
            endpoint_name: "Hong Kong",
            updated_at: "2026-07-10T10:00:00Z",
          },
          {
            identity: "codex:def",
            auth_id: "codex-new.json",
            account_label: "new@example.com",
            endpoint_id: "",
            bound: false,
          },
          {
            identity: "",
            auth_id: "codex-invalid.json",
            account_label: "codex-invalid.json",
            endpoint_id: "",
            bound: false,
            error: "missing account_id",
          },
        ],
      });

    await expect(egressApi.listNodes()).resolves.toEqual([
      {
        id: "node-1",
        name: "egress-hk",
        givenName: "hk",
        ipAddresses: ["100.64.0.2"],
        online: true,
        fresh: true,
        syncedAt: "2026-07-10T09:59:40Z",
        syncAgeSeconds: 20,
        tags: ["tag:clirelay-egress"],
        lastSeen: "2026-07-10T10:00:00Z",
      },
    ]);
    await expect(egressApi.listEndpoints()).resolves.toEqual([
      {
        id: "hk-socks",
        nodeId: "node-1",
        name: "Hong Kong",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1080,
        enabled: true,
        isLocal: false,
        status: "healthy",
        latencyMs: 31,
        publicIp: "203.0.113.8",
        expectedPublicIp: "203.0.113.8",
        eligibility: {
          state: "eligible",
          selectable: true,
          reasonCodes: [],
          checkedAgeSeconds: 12,
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 1,
          exclusiveOwnerIdentity: "codex:abc",
          duplicatePublicIp: false,
        },
      },
      {
        id: "origin-socks",
        nodeId: "",
        name: "Origin server",
        protocol: "socks5",
        host: "127.0.0.1",
        port: 1081,
        enabled: false,
        isLocal: true,
        status: "unknown",
        eligibility: {
          state: "unknown",
          selectable: false,
          reasonCodes: [],
          nodeOnline: false,
          nodeStale: false,
          bindingCount: 0,
          duplicatePublicIp: false,
        },
      },
    ]);
    await expect(egressApi.listBindings()).resolves.toEqual([
      {
        identity: "codex:abc",
        authId: "codex-user.json",
        accountLabel: "user@example.com",
        endpointId: "hk-socks",
        bound: true,
        endpointName: "Hong Kong",
        updatedAt: "2026-07-10T10:00:00Z",
      },
      {
        identity: "codex:def",
        authId: "codex-new.json",
        accountLabel: "new@example.com",
        endpointId: "",
        bound: false,
      },
      {
        identity: "",
        authId: "codex-invalid.json",
        accountLabel: "codex-invalid.json",
        endpointId: "",
        bound: false,
        error: "missing account_id",
      },
    ]);
  });

  test("serializes endpoint CRUD, health check, node sync, and enrollment", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    postMock.mockResolvedValue({});
    patchMock.mockResolvedValue({});
    deleteMock.mockResolvedValue({});

    await egressApi.syncNodes();
    await egressApi.createEnrollment({ name: "egress-hk" });
    await egressApi.createEndpoint({
      nodeId: "node-1",
      name: "Hong Kong",
      protocol: "socks5",
      host: "100.64.0.2",
      port: 1080,
      enabled: true,
      isLocal: false,
      expectedPublicIp: "203.0.113.8",
    });
    await egressApi.updateEndpoint("hk/socks", { enabled: false });
    await egressApi.updateEndpoint("hk/socks", { password: "" });
    await egressApi.updateEndpoint("hk/socks", { clearCredentials: true });
    await egressApi.checkEndpoint("hk/socks");
    await egressApi.deleteEndpoint("hk/socks");

    expect(postMock).toHaveBeenNthCalledWith(1, "/egress/nodes/sync");
    expect(postMock).toHaveBeenNthCalledWith(2, "/egress/enrollment", {
      name: "egress-hk",
    });
    expect(postMock).toHaveBeenNthCalledWith(3, "/egress/endpoints", {
      node_id: "node-1",
      name: "Hong Kong",
      protocol: "socks5",
      host: "100.64.0.2",
      port: 1080,
      enabled: true,
      local_server: false,
      expected_public_ip: "203.0.113.8",
    });
    expect(patchMock).toHaveBeenNthCalledWith(1, "/egress/endpoints/hk%2Fsocks", {
      enabled: false,
    });
    expect(patchMock).toHaveBeenNthCalledWith(2, "/egress/endpoints/hk%2Fsocks", {});
    expect(patchMock).toHaveBeenNthCalledWith(3, "/egress/endpoints/hk%2Fsocks", {
      username: "",
      password: "",
    });
    expect(postMock).toHaveBeenNthCalledWith(4, "/egress/endpoints/hk%2Fsocks/check");
    expect(deleteMock).toHaveBeenCalledWith("/egress/endpoints/hk%2Fsocks");
  });

  test("previews and atomically applies staged binding assignments", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    postMock.mockResolvedValueOnce({
      expected_revision: "rev-7",
      change_count: 2,
      affected_accounts: ["a@example.com", "b@example.com"],
      assignments: [
        { identity: "codex:a", endpoint_id: "" },
        { identity: "codex:b", endpoint_id: "sg" },
      ],
      blockers: [],
      warnings: ["Review the affected accounts"],
    });
    putMock.mockResolvedValueOnce({ revision: "rev-8", applied: 2 });

    const assignments = [
      { identity: "codex:a", endpointId: "" },
      { identity: "codex:b", endpointId: "sg" },
    ];
    await expect(egressApi.previewBindings(assignments)).resolves.toMatchObject({
      expectedRevision: "rev-7",
      changeCount: 2,
      affectedAccounts: ["a@example.com", "b@example.com"],
    });
    await expect(egressApi.applyBindings(assignments, "rev-7", true)).resolves.toMatchObject({
      revision: "rev-8",
      applied: 2,
    });

    expect(postMock).toHaveBeenNthCalledWith(1, "/egress/bindings/preview", {
      assignments: [
        { identity: "codex:a", endpoint_id: "" },
        { identity: "codex:b", endpoint_id: "sg" },
      ],
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

  test("treats IP mismatch and duplicate public IP as unhealthy and never selectable by fallback", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    getMock.mockResolvedValue({
      items: [
        {
          id: "mismatch",
          node_id: "node-1",
          name: "Mismatch",
          protocol: "socks5",
          host: "100.64.0.2",
          port: 1080,
          enabled: true,
          status: "ip_mismatch",
          expected_public_ip: "203.0.113.8",
          observed_public_ip: "203.0.113.9",
          eligibility: {
            reasons: ["public_ip_mismatch"],
            runtime_ready: false,
          },
        },
        {
          id: "duplicate",
          node_id: "node-1",
          name: "Duplicate",
          protocol: "socks5",
          host: "100.64.0.3",
          port: 1080,
          enabled: true,
          status: "duplicate_public_ip",
          eligibility: {
            reasons: ["duplicate_public_ip"],
            runtime_ready: false,
          },
        },
      ],
    });
    const endpoints = await egressApi.listEndpoints();
    expect(endpoints.map((endpoint) => endpoint.status)).toEqual(["unhealthy", "unhealthy"]);
    expect(endpoints.every((endpoint) => endpoint.eligibility?.selectable === false)).toBe(true);
  });

  test("previews endpoint impact before a confirmed disable or delete action", async () => {
    const { egressApi } = await import("@/lib/http/apis/egress");
    postMock
      .mockResolvedValueOnce({
        endpoint_id: "hk/socks",
        action: "delete",
        revision: "rev-7",
        binding_count: 2,
        binding_identities: ["codex:a", "codex:b"],
        allowed: true,
        requires_confirmation: true,
        blockers: [],
      })
      .mockResolvedValueOnce({ revision: "rev-8", action: "delete" });

    await expect(egressApi.endpointImpact("hk/socks", "delete")).resolves.toMatchObject({
      endpointId: "hk/socks",
      action: "delete",
      expectedRevision: "rev-7",
      affectedBindings: 2,
    });
    await egressApi.endpointAction("hk/socks", "delete", "rev-7", true);

    expect(postMock).toHaveBeenNthCalledWith(1, "/egress/endpoints/hk%2Fsocks/impact", {
      action: "delete",
    });
    expect(postMock).toHaveBeenNthCalledWith(2, "/egress/endpoints/hk%2Fsocks/actions", {
      action: "delete",
      revision: "rev-7",
      confirmed: true,
    });
  });
});
