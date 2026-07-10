import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import i18n, { ensureLanguageResources } from "@/i18n";
import { EgressPage } from "@/modules/egress/EgressPage";
import { ThemeProvider } from "@/modules/ui/ThemeProvider";
import { ToastProvider } from "@/modules/ui/ToastProvider";

const mocks = vi.hoisted(() => ({
  getOverview: vi.fn(),
  listNodes: vi.fn(),
  syncNodes: vi.fn(),
  createEnrollment: vi.fn(),
  listEndpoints: vi.fn(),
  createEndpoint: vi.fn(),
  updateEndpoint: vi.fn(),
  deleteEndpoint: vi.fn(),
  checkEndpoint: vi.fn(),
  listBindings: vi.fn(),
  updateBinding: vi.fn(),
  deleteBinding: vi.fn(),
  previewBindings: vi.fn(),
  applyBindings: vi.fn(),
  endpointImpact: vi.fn(),
  endpointAction: vi.fn(),
}));

vi.mock("@/lib/http/apis/egress", () => ({
  egressApi: mocks,
}));

function renderPage() {
  return render(
    <ThemeProvider>
      <ToastProvider>
        <EgressPage />
      </ToastProvider>
    </ThemeProvider>,
  );
}

describe("EgressPage", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
    Object.values(mocks).forEach((mock) => mock.mockReset());
    mocks.getOverview.mockResolvedValue({
      enabled: true,
      headscale: {
        configured: true,
        reachable: false,
        url: "https://headscale.internal",
        apiKeyConfigured: true,
        serviceTag: "tag:clirelay-egress",
        lastSyncAt: "2026-07-10T10:00:00Z",
        error: "connection refused",
      },
      revision: "rev-7",
      policy: {
        bindingMode: "exclusive",
        nodeFreshnessTtlSeconds: 300,
        endpointCheckTtlSeconds: 300,
      },
      readiness: {
        scope: "application_egress",
        verdict: "blocked",
        readyToEnable: false,
        codexOAuthAllowed: false,
        blockers: [
          {
            code: "headscale_unreachable",
            message: "Headscale is unreachable",
          },
        ],
        warnings: [],
        notEvaluated: [],
      },
      counts: {
        nodes: 2,
        onlineNodes: 1,
        endpoints: 1,
        enabledEndpoints: 1,
        bindings: 1,
        accounts: 3,
        routableAccounts: 1,
        unboundAccounts: 1,
        missingIdentityAccounts: 1,
      },
      localEndpointEnabled: false,
    });
    mocks.listNodes.mockResolvedValue([
      {
        id: "node-1",
        name: "egress-hk",
        givenName: "hk",
        ipAddresses: ["100.64.0.2"],
        online: true,
        fresh: true,
        syncedAt: "2026-07-10T10:00:00Z",
        syncAgeSeconds: 20,
        tags: ["tag:clirelay-egress"],
        lastSeen: "2026-07-10T10:00:00Z",
      },
    ]);
    mocks.listEndpoints.mockResolvedValue([
      {
        id: "hk-socks",
        nodeId: "node-1",
        name: "Hong Kong",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1080,
        enabled: true,
        isLocal: false,
        hasCredentials: true,
        username: "relay",
        status: "healthy",
        latencyMs: 31,
        publicIp: "203.0.113.8",
        expectedPublicIp: "203.0.113.8",
        lastCheckedAt: "2026-07-10T10:00:00Z",
        eligibility: {
          state: "eligible",
          selectable: true,
          reasonCodes: [],
          checkedAgeSeconds: 20,
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 1,
          exclusiveOwnerIdentity: "codex:abc",
          duplicatePublicIp: false,
        },
      },
    ]);
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:abc",
        authId: "codex-user.json",
        accountLabel: "user@example.com",
        endpointId: "hk-socks",
        bound: true,
        endpointName: "Hong Kong",
        updatedAt: "2026-07-10T10:00:00Z",
      },
    ]);
    mocks.syncNodes.mockResolvedValue({});
    mocks.createEnrollment.mockResolvedValue({
      key: "one-time-secret",
      expiresAt: "2026-07-10T11:00:00Z",
      command: "tailscale up --login-server https://headscale.internal --auth-key one-time-secret",
    });
    mocks.createEndpoint.mockResolvedValue({});
    mocks.updateEndpoint.mockResolvedValue({});
    mocks.deleteEndpoint.mockResolvedValue({});
    mocks.checkEndpoint.mockResolvedValue({});
    mocks.updateBinding.mockResolvedValue({});
    mocks.deleteBinding.mockResolvedValue({});
    mocks.previewBindings.mockResolvedValue({
      expectedRevision: "rev-7",
      assignments: [{ identity: "codex:abc", endpointId: "sg-socks" }],
      changeCount: 1,
      affectedAccounts: ["user@example.com"],
      blockers: [],
      warnings: [],
      valid: true,
    });
    mocks.applyBindings.mockResolvedValue({ revision: "rev-8", applied: 1 });
    mocks.endpointImpact.mockResolvedValue({
      endpointId: "hk-socks",
      action: "delete",
      expectedRevision: "rev-7",
      affectedBindings: 1,
      affectedAccounts: ["user@example.com"],
      blocked: false,
    });
    mocks.endpointAction.mockResolvedValue({});
  });

  test("shows a dense overview and distinguishes unreachable Headscale", async () => {
    renderPage();

    expect(await screen.findByText("Egress network")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Overview" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Nodes" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Endpoints" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Bindings" })).toBeInTheDocument();
    expect((await screen.findAllByText("Configured, unreachable")).length).toBeGreaterThan(0);
    expect(screen.getAllByText("Runtime enabled").length).toBeGreaterThan(0);
    expect(screen.getByText("Runtime enabled · readiness blocked")).toBeInTheDocument();
    expect(screen.getByText(/Headscale is unreachable/)).toBeInTheDocument();
    expect(
      screen.getByText(/Missing, disabled, or unreachable endpoints fail closed/),
    ).toBeInTheDocument();
    expect(screen.getByText("connection refused")).toBeInTheDocument();
    expect(screen.getAllByText("https://headscale.internal").length).toBeGreaterThan(0);
    expect(screen.getByText(/Headscale nodes: 1 \/ 2/)).toBeInTheDocument();
  });

  test("prominently distinguishes prepared configuration from a disabled runtime", async () => {
    mocks.getOverview.mockResolvedValue({
      enabled: false,
      headscale: {
        configured: true,
        reachable: true,
        url: "https://headscale.internal",
        apiKeyConfigured: true,
        serviceTag: "tag:clirelay-egress",
      },
      revision: "rev-7",
      policy: {
        bindingMode: "exclusive",
        nodeFreshnessTtlSeconds: 300,
        endpointCheckTtlSeconds: 300,
      },
      readiness: {
        scope: "application_egress",
        verdict: "ready",
        readyToEnable: true,
        codexOAuthAllowed: false,
        blockers: [],
        warnings: [],
        notEvaluated: [],
      },
      counts: {
        nodes: 2,
        onlineNodes: 2,
        endpoints: 2,
        enabledEndpoints: 2,
        bindings: 1,
        accounts: 1,
        routableAccounts: 1,
        unboundAccounts: 0,
        missingIdentityAccounts: 0,
      },
      localEndpointEnabled: false,
    });

    renderPage();

    expect(
      (await screen.findAllByText("Preparation mode · runtime disabled")).length,
    ).toBeGreaterThan(0);
    expect(screen.getByText("Ready to enable · runtime disabled")).toBeInTheDocument();
    expect(
      screen.getByText(/Codex OAuth traffic is blocked while egress-network.enabled is off/),
    ).toBeInTheDocument();
    expect(screen.getByText(/cannot fall back to the CliRelay host network/)).toBeInTheDocument();
  });

  test("syncs nodes and presents a one-time enrollment command", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Nodes" }));

    expect((await screen.findAllByText("egress-hk")).length).toBeGreaterThan(0);
    await user.click(screen.getByRole("button", { name: "Sync nodes" }));
    await waitFor(() => expect(mocks.syncNodes).toHaveBeenCalledTimes(1));

    await user.click(screen.getByRole("button", { name: "Enroll node" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Enroll egress node",
    });
    await user.type(within(dialog).getByLabelText("Node name"), "egress-sg");
    await user.click(within(dialog).getByRole("button", { name: "Generate command" }));

    expect(mocks.createEnrollment).toHaveBeenCalledWith({ name: "egress-sg" });
    expect(await within(dialog).findByText("one-time-secret")).toBeInTheDocument();
    expect(within(dialog).getByText(/tailscale up --login-server/)).toBeInTheDocument();
    expect(within(dialog).getByText("Shown once")).toBeInTheDocument();
  });

  test("shows an online but stale node as stale with its sync age", async () => {
    mocks.listNodes.mockResolvedValueOnce([
      {
        id: "node-stale",
        name: "egress-stale",
        ipAddresses: ["100.64.0.9"],
        online: true,
        fresh: false,
        syncedAt: "2026-07-10T09:50:00Z",
        syncAgeSeconds: 600,
        tags: ["tag:clirelay-egress"],
      },
    ]);
    renderPage();
    await userEvent.setup().click(await screen.findByRole("tab", { name: "Nodes" }));
    expect(screen.getAllByText("Stale").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/600s/).length).toBeGreaterThan(0);
  });

  test("creates, checks, edits, and deletes an endpoint without exposing its password", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Endpoints" }));

    expect((await screen.findAllByText("Credentials configured")).length).toBeGreaterThan(0);
    expect(screen.queryByText("new-secret")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Add endpoint" }));
    const createDialog = await screen.findByRole("dialog", {
      name: "Add endpoint",
    });
    expect(
      within(createDialog).getByRole("switch", {
        name: "Origin server endpoint",
      }),
    ).toBeDisabled();
    expect(
      within(createDialog).getByText(
        "Enable local-server egress explicitly in YAML before creating a local endpoint.",
      ),
    ).toBeInTheDocument();
    await user.type(within(createDialog).getByLabelText("Name"), "Singapore");
    await user.click(within(createDialog).getByRole("combobox", { name: "Node" }));
    await user.click(await screen.findByRole("option", { name: /egress-hk/ }));
    await user.clear(within(createDialog).getByLabelText("Host"));
    await user.type(within(createDialog).getByLabelText("Host"), "100.64.0.2");
    await user.clear(within(createDialog).getByLabelText("Port"));
    await user.type(within(createDialog).getByLabelText("Port"), "1081");
    await user.type(within(createDialog).getByLabelText("Expected public IP"), "203.0.113.9");
    await user.type(within(createDialog).getByLabelText("Username"), "relay");
    await user.type(within(createDialog).getByLabelText("Password"), "new-secret");
    await user.click(within(createDialog).getByRole("button", { name: "Save endpoint" }));

    await waitFor(() => {
      expect(mocks.createEndpoint).toHaveBeenCalledWith({
        nodeId: "node-1",
        name: "Singapore",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1081,
        enabled: true,
        isLocal: false,
        expectedPublicIp: "203.0.113.9",
        username: "relay",
        password: "new-secret",
      });
    });

    await user.click(screen.getAllByRole("button", { name: "Check Hong Kong" })[0]);
    expect(mocks.checkEndpoint).toHaveBeenCalledWith("hk-socks");

    await user.click(screen.getAllByRole("button", { name: "Edit Hong Kong" })[0]);
    const editDialog = await screen.findByRole("dialog", {
      name: "Edit endpoint",
    });
    expect(within(editDialog).getByLabelText("Password")).toHaveValue("");
    expect(
      within(editDialog).getByText("Leave blank to keep the existing password."),
    ).toBeInTheDocument();
    await user.click(within(editDialog).getByRole("button", { name: "Save endpoint" }));
    await waitFor(() =>
      expect(mocks.updateEndpoint).toHaveBeenCalledWith(
        "hk-socks",
        expect.not.objectContaining({ password: expect.anything() }),
      ),
    );

    await user.click(screen.getAllByRole("button", { name: "Delete Hong Kong" })[0]);
    await waitFor(() => expect(mocks.endpointImpact).toHaveBeenCalledWith("hk-socks", "delete"));
    const confirm = await screen.findByRole("dialog", {
      name: "Delete endpoint",
    });
    expect(within(confirm).getByText(/1 affected binding/)).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Delete" }));
    expect(mocks.endpointAction).toHaveBeenCalledWith("hk-socks", "delete", "rev-7", true);
  });

  test("stages searchable binding changes and atomically applies them after preview", async () => {
    const user = userEvent.setup();
    mocks.listEndpoints.mockResolvedValue([
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
        publicIp: "203.0.113.8",
        lastCheckedAt: "2026-07-10T10:00:00Z",
        eligibility: {
          state: "eligible",
          selectable: true,
          reasonCodes: [],
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 1,
          exclusiveOwnerIdentity: "codex:abc",
          duplicatePublicIp: false,
        },
      },
      {
        id: "sg-socks",
        nodeId: "node-1",
        name: "Singapore",
        protocol: "socks5",
        host: "100.64.0.3",
        port: 1080,
        enabled: true,
        isLocal: false,
        status: "healthy",
        publicIp: "203.0.113.9",
        lastCheckedAt: "2026-07-10T10:00:00Z",
        eligibility: {
          state: "eligible",
          selectable: true,
          reasonCodes: [],
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 0,
          duplicatePublicIp: false,
        },
      },
    ]);
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Bindings" }));

    await user.type(screen.getByRole("searchbox", { name: "Search Codex bindings" }), "user@");
    const select = (
      await screen.findAllByRole("combobox", {
        name: "Endpoint for user@example.com",
      })
    )[0];
    await user.click(select);
    await user.click(
      await screen.findByRole("option", {
        name: /Singapore.*203\.0\.113\.9.*healthy/i,
      }),
    );
    expect(mocks.updateBinding).not.toHaveBeenCalled();
    expect(screen.getByText("1 pending change")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Preview 1 change" }));
    await waitFor(() =>
      expect(mocks.previewBindings).toHaveBeenCalledWith([
        { identity: "codex:abc", endpointId: "sg-socks" },
      ]),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Apply binding changes",
    });
    expect(within(confirm).getByText(/1 binding change/)).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Apply atomically" }));
    await waitFor(() =>
      expect(mocks.applyBindings).toHaveBeenCalledWith(
        [{ identity: "codex:abc", endpointId: "sg-socks" }],
        "rev-7",
        true,
      ),
    );
  });

  test("stages and atomically applies an explicit unbind", async () => {
    const user = userEvent.setup();
    mocks.previewBindings.mockResolvedValueOnce({
      expectedRevision: "rev-7",
      assignments: [{ identity: "codex:abc", endpointId: "" }],
      changeCount: 1,
      affectedAccounts: ["user@example.com"],
      blockers: [],
      warnings: [],
      valid: true,
    });
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Bindings" }));
    await user.click(
      screen.getAllByRole("button", {
        name: "Remove binding for user@example.com",
      })[0],
    );
    await user.click(screen.getByRole("button", { name: "Preview 1 change" }));
    await waitFor(() =>
      expect(mocks.previewBindings).toHaveBeenCalledWith([
        { identity: "codex:abc", endpointId: "" },
      ]),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Apply binding changes",
    });
    await user.click(within(confirm).getByRole("button", { name: "Apply atomically" }));
    await waitFor(() =>
      expect(mocks.applyBindings).toHaveBeenCalledWith(
        [{ identity: "codex:abc", endpointId: "" }],
        "rev-7",
        true,
      ),
    );
  });

  test("removes enabled from the post-action patch when disabling an edited endpoint", async () => {
    const user = userEvent.setup();
    mocks.endpointImpact.mockResolvedValueOnce({
      endpointId: "hk-socks",
      action: "disable",
      expectedRevision: "rev-7",
      affectedBindings: 1,
      affectedAccounts: ["user@example.com"],
      blocked: false,
    });
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Endpoints" }));
    await user.click(screen.getAllByRole("button", { name: "Edit Hong Kong" })[0]);
    const editDialog = await screen.findByRole("dialog", {
      name: "Edit endpoint",
    });
    await user.clear(within(editDialog).getByLabelText("Name"));
    await user.type(within(editDialog).getByLabelText("Name"), "Hong Kong disabled");
    await user.click(within(editDialog).getByRole("switch", { name: "Enabled" }));
    await user.click(within(editDialog).getByRole("button", { name: "Save endpoint" }));
    const confirm = await screen.findByRole("dialog", {
      name: "Disable endpoint",
    });
    await user.click(within(confirm).getByRole("button", { name: "Disable" }));
    await waitFor(() =>
      expect(mocks.endpointAction).toHaveBeenCalledWith("hk-socks", "disable", "rev-7", true),
    );
    expect(mocks.updateEndpoint).toHaveBeenCalledWith(
      "hk-socks",
      expect.objectContaining({ name: "Hong Kong disabled" }),
    );
    expect(mocks.updateEndpoint).toHaveBeenCalledWith(
      "hk-socks",
      expect.not.objectContaining({ enabled: expect.anything() }),
    );
  });

  test("shows expected and observed IP plus endpoint readiness failures", async () => {
    mocks.listEndpoints.mockResolvedValueOnce([
      {
        id: "hk-socks",
        nodeId: "node-1",
        name: "Hong Kong",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1080,
        enabled: true,
        isLocal: false,
        status: "unhealthy",
        expectedPublicIp: "203.0.113.8",
        publicIp: "203.0.113.9",
        error: "observed public IP does not match expected IP",
        eligibility: {
          state: "blocked",
          selectable: false,
          reasonCodes: ["public_ip_mismatch"],
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 0,
          duplicatePublicIp: false,
        },
      },
    ]);
    renderPage();
    await userEvent.setup().click(await screen.findByRole("tab", { name: "Endpoints" }));
    expect(screen.getAllByText("203.0.113.8").length).toBeGreaterThan(0);
    expect(screen.getAllByText("203.0.113.9").length).toBeGreaterThan(0);
    expect(
      screen.getAllByText(/observed public IP does not match expected IP/).length,
    ).toBeGreaterThan(0);
    expect(
      screen.getAllByText("Observed public IP does not match the expected IP.").length,
    ).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText(/public_ip_mismatch/)).not.toBeInTheDocument();
  });

  test("localizes overview and endpoint safety codes without exposing backend messages", async () => {
    await i18n.changeLanguage("zh-CN");
    mocks.getOverview.mockResolvedValue({
      enabled: false,
      headscale: {
        configured: true,
        reachable: true,
        url: "https://headscale.internal",
        apiKeyConfigured: true,
        serviceTag: "tag:clirelay-egress",
      },
      revision: "rev-7",
      policy: {
        bindingMode: "exclusive",
        nodeFreshnessTtlSeconds: 300,
        endpointCheckTtlSeconds: 300,
      },
      readiness: {
        scope: "application_egress",
        verdict: "blocked",
        readyToEnable: false,
        codexOAuthAllowed: false,
        blockers: [
          {
            code: "no_runtime_ready_endpoints",
            message: "No endpoint is runtime ready.",
          },
        ],
        warnings: [
          {
            code: "runtime_disabled",
            message: "Egress runtime is disabled; configuration can still be prepared.",
          },
        ],
        notEvaluated: [],
      },
      counts: {
        nodes: 1,
        onlineNodes: 0,
        endpoints: 1,
        enabledEndpoints: 1,
        bindings: 0,
        accounts: 0,
        routableAccounts: 0,
        unboundAccounts: 0,
        missingIdentityAccounts: 0,
      },
      localEndpointEnabled: false,
    });
    mocks.listEndpoints.mockResolvedValue([
      {
        id: "hk-socks",
        nodeId: "node-1",
        name: "Hong Kong",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1080,
        enabled: true,
        isLocal: false,
        status: "unhealthy",
        expectedPublicIp: "203.0.113.8",
        publicIp: "203.0.113.9",
        error: "connection refused",
        eligibility: {
          state: "blocked",
          selectable: false,
          reasonCodes: ["node_offline", "public_ip_mismatch"],
          nodeOnline: false,
          nodeStale: false,
          bindingCount: 0,
          duplicatePublicIp: false,
        },
      },
    ]);

    renderPage();

    expect(await screen.findByText(/没有运行时就绪的出口端点。/)).toBeInTheDocument();
    expect(screen.getByText(/出口运行态已关闭，当前仍可继续准备配置。/)).toBeInTheDocument();
    expect(screen.getByText(/检查范围：应用出口/)).toBeInTheDocument();
    expect(screen.getByText(/绑定策略：独占端点绑定/)).toBeInTheDocument();
    expect(screen.queryByText("No endpoint is runtime ready.")).not.toBeInTheDocument();
    expect(screen.queryByText(/no_runtime_ready_endpoints/)).not.toBeInTheDocument();
    expect(screen.queryByText(/application_egress/)).not.toBeInTheDocument();

    await ensureLanguageResources("ru");
    await i18n.changeLanguage("ru");
    expect(await screen.findByText(/Нет точки выхода, готовой к работе./)).toBeInTheDocument();
    expect(
      screen.getByText(/Рабочий режим выхода выключен; конфигурация остаётся в режиме подготовки./),
    ).toBeInTheDocument();
    expect(screen.getByText(/Область: Исходящий трафик приложения/)).toBeInTheDocument();
    expect(screen.getByText(/Политика привязки: Эксклюзивная привязка точки/)).toBeInTheDocument();
    expect(screen.queryByText("No endpoint is runtime ready.")).not.toBeInTheDocument();
    expect(screen.queryByText(/no_runtime_ready_endpoints/)).not.toBeInTheDocument();

    await i18n.changeLanguage("zh-CN");
    await userEvent.setup().click(screen.getByRole("tab", { name: "出口端点" }));
    expect(screen.getAllByText("Headscale 节点已离线。").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("观测公网 IP 与预期值不一致。").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("connection refused").length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText(/node_offline|public_ip_mismatch/)).not.toBeInTheDocument();
  });

  test("auto-assigns selected accounts to unique selectable endpoints", async () => {
    const user = userEvent.setup();
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:a",
        authId: "a.json",
        accountLabel: "a@example.com",
        endpointId: "",
        bound: false,
      },
      {
        identity: "codex:b",
        authId: "b.json",
        accountLabel: "b@example.com",
        endpointId: "",
        bound: false,
      },
    ]);
    mocks.listEndpoints.mockResolvedValue([
      {
        id: "hk",
        nodeId: "node-1",
        name: "Hong Kong",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1080,
        enabled: true,
        isLocal: false,
        status: "healthy",
        publicIp: "203.0.113.8",
        eligibility: {
          state: "eligible",
          selectable: true,
          reasonCodes: [],
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 0,
          duplicatePublicIp: false,
        },
      },
      {
        id: "sg",
        nodeId: "node-1",
        name: "Singapore",
        protocol: "socks5",
        host: "100.64.0.3",
        port: 1080,
        enabled: true,
        isLocal: false,
        status: "healthy",
        publicIp: "203.0.113.9",
        eligibility: {
          state: "eligible",
          selectable: true,
          reasonCodes: [],
          nodeOnline: true,
          nodeStale: false,
          bindingCount: 0,
          duplicatePublicIp: false,
        },
      },
    ]);
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Bindings" }));
    await user.click(screen.getAllByRole("checkbox", { name: "Select a@example.com" })[0]);
    await user.click(screen.getAllByRole("checkbox", { name: "Select b@example.com" })[0]);
    await user.click(screen.getByRole("button", { name: "Auto-assign unique endpoints" }));

    expect(screen.getByText("2 pending changes")).toBeInTheDocument();
  });
});
