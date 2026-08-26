import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import i18n, { ensureLanguageResources } from "@/i18n";
import { EgressPage } from "@/modules/egress/EgressPage";
import { ThemeProvider } from "@/modules/ui/ThemeProvider";
import { ToastProvider } from "@/modules/ui/ToastProvider";

const mocks = vi.hoisted(() => ({
  getOverview: vi.fn(),
  listEndpoints: vi.fn(),
  createEndpoint: vi.fn(),
  updateEndpoint: vi.fn(),
  deleteEndpoint: vi.fn(),
  checkEndpoint: vi.fn(),
  listBindings: vi.fn(),
  previewBindings: vi.fn(),
  applyBindings: vi.fn(),
  endpointImpact: vi.fn(),
  endpointAction: vi.fn(),
}));

const toastMocks = vi.hoisted(() => ({
  error: vi.fn(),
  info: vi.fn(),
  success: vi.fn(),
  warning: vi.fn(),
}));

vi.mock("@/lib/http/apis/egress", () => ({ egressApi: mocks }));
vi.mock("goey-toast", () => ({
  GoeyToaster: () => null,
  goeyToast: toastMocks,
}));

const overview = {
  enabled: true,
  revision: "rev-7",
  scope: "application_egress",
  policy: {
    bindingMode: "exclusive" as const,
    failureMode: "fail_closed" as const,
    readinessScope: "application_egress",
    hostKillSwitchEnforced: false,
  },
  readiness: {
    scope: "application_egress",
    verdict: "ready" as const,
    readyToEnable: true,
    codexOAuthAllowed: true,
    blockers: [],
    warnings: [],
    notEvaluated: [],
  },
  counts: {
    endpoints: 2,
    enabledEndpoints: 2,
    bindings: 1,
    codexAuths: 2,
    boundCodexAuths: 1,
    unboundCodexAuths: 1,
    missingAccountId: 0,
    boundEndpointNotReady: 0,
  },
};

const endpoints = [
  {
    id: "hk-socks",
    name: "Hong Kong",
    protocol: "socks5" as const,
    host: "10.77.0.2",
    port: 1080,
    enabled: true,
    hasCredentials: true,
    username: "relay",
    status: "healthy" as const,
    latencyMs: 31,
    publicIp: "203.0.113.8",
    expectedPublicIp: "203.0.113.8",
    lastCheckedAt: "2026-07-10T10:00:00Z",
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
  {
    id: "sg-socks",
    name: "Singapore",
    protocol: "socks5" as const,
    host: "10.77.0.3",
    port: 1080,
    enabled: true,
    status: "healthy" as const,
    latencyMs: 42,
    publicIp: "203.0.113.9",
    expectedPublicIp: "203.0.113.9",
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
];

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
    Object.values(toastMocks).forEach((mock) => mock.mockReset());
    mocks.getOverview.mockResolvedValue(overview);
    mocks.listEndpoints.mockResolvedValue(endpoints);
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:abc",
        authId: "codex-user.json",
        accountLabel: "user@example.com",
        planType: "pro",
        endpointId: "hk-socks",
        endpointName: "Hong Kong",
        bound: true,
      },
    ]);
    mocks.createEndpoint.mockResolvedValue({});
    mocks.updateEndpoint.mockResolvedValue({});
    mocks.checkEndpoint.mockResolvedValue({});
    mocks.endpointImpact.mockResolvedValue({
      endpointId: "hk-socks",
      action: "delete",
      expectedRevision: "rev-7",
      affectedBindings: 1,
      affectedAccounts: ["user@example.com"],
      blocked: false,
    });
    mocks.endpointAction.mockResolvedValue({});
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
  });

  test("renders only overview, endpoint, and fixed-binding tabs", async () => {
    renderPage();

    expect(await screen.findByText("Proxy egress")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Overview" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Egress endpoints" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Account bindings" })).toBeInTheDocument();
    expect(screen.getAllByRole("tab")).toHaveLength(3);
    expect(
      screen.getByText(/Each Codex OAuth account is pinned to one endpoint/),
    ).toBeInTheDocument();
    expect(screen.getByText(/fails closed instead of using the host network/)).toBeInTheDocument();
    expect(mocks.getOverview).toHaveBeenCalledTimes(1);
    expect(mocks.listEndpoints).toHaveBeenCalledTimes(1);
    expect(mocks.listBindings).toHaveBeenCalledTimes(1);
  });

  test("creates and checks a standalone endpoint", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Egress endpoints" }));
    await user.click(screen.getByRole("button", { name: "Add endpoint" }));

    const dialog = await screen.findByRole("dialog", { name: "Add endpoint" });
    await user.type(within(dialog).getByLabelText("Name"), "Tokyo");
    await user.type(within(dialog).getByLabelText("Host"), "10.77.0.4");
    await user.clear(within(dialog).getByLabelText("Port"));
    await user.type(within(dialog).getByLabelText("Port"), "1080");
    await user.type(within(dialog).getByLabelText("Expected public IP"), "203.0.113.10");
    await user.type(within(dialog).getByLabelText("Username"), "relay");
    await user.type(within(dialog).getByLabelText("Password"), "new-secret");
    await user.click(within(dialog).getByRole("button", { name: "Save endpoint" }));

    await waitFor(() =>
      expect(mocks.createEndpoint).toHaveBeenCalledWith({
        name: "Tokyo",
        protocol: "socks5",
        host: "10.77.0.4",
        port: 1080,
        enabled: true,
        sharingMode: "exclusive",
        expectedPublicIp: "203.0.113.10",
        username: "relay",
        password: "new-secret",
      }),
    );
    expect(screen.queryByText("new-secret")).not.toBeInTheDocument();

    await user.click(screen.getAllByRole("button", { name: "Check Hong Kong" })[0]);
    expect(mocks.checkEndpoint).toHaveBeenCalledWith("hk-socks");
  });

  test.each([
    [
      "en",
      "The endpoint health check is stale or failing.",
      "A safety condition requires attention.",
    ],
    ["zh-CN", "端点健康检查已过期或检查失败。", "存在需要处理的安全状态。"],
    [
      "ru",
      "Проверка работоспособности точки устарела или завершается ошибкой.",
      "Требуется проверить условие безопасности.",
    ],
  ])("localizes the combined endpoint health reason in %s", async (language, expected, unknown) => {
    await ensureLanguageResources(language);
    await i18n.changeLanguage(language);
    mocks.listEndpoints.mockResolvedValueOnce([
      {
        ...endpoints[0],
        status: "unhealthy",
        eligible: false,
        runtimeReady: false,
        eligibility: {
          ...endpoints[0].eligibility,
          eligible: false,
          runtimeReady: false,
          healthFresh: false,
          reasonCodes: ["endpoint_health_stale_or_unhealthy"],
        },
      },
    ]);

    renderPage();
    await userEvent.setup().click((await screen.findAllByRole("tab"))[1]);

    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.queryByText(unknown)).not.toBeInTheDocument();
  });

  test("refreshes persisted endpoint health after a failed check and keeps the error toast", async () => {
    const user = userEvent.setup();
    const unhealthyEndpoint = {
      ...endpoints[0],
      status: "unhealthy" as const,
      publicIp: "203.0.113.99",
      eligible: false,
      runtimeReady: false,
      eligibility: {
        ...endpoints[0].eligibility,
        eligible: false,
        runtimeReady: false,
        healthFresh: false,
        publicIpMatches: false,
        reasonCodes: ["public_ip_mismatch"],
      },
    };
    mocks.checkEndpoint.mockRejectedValueOnce(new Error("proxy probe failed"));
    mocks.listEndpoints.mockResolvedValueOnce(endpoints).mockResolvedValueOnce([unhealthyEndpoint]);

    renderPage();
    await user.click((await screen.findAllByRole("tab"))[1]);
    await user.click(screen.getAllByRole("button", { name: "Check Hong Kong" })[0]);

    await waitFor(() => expect(mocks.listEndpoints).toHaveBeenCalledTimes(2));
    expect((await screen.findAllByText("203.0.113.99")).length).toBeGreaterThan(0);
    expect(
      screen.getByText("Observed public IP does not match the expected IP."),
    ).toBeInTheDocument();
    expect(toastMocks.error).toHaveBeenCalledWith("proxy probe failed", expect.any(Object));
  });

  test("previews and atomically applies a fixed endpoint change", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByRole("tab", { name: "Account bindings" }));
    expect((await screen.findAllByText("Plan: PRO")).length).toBeGreaterThan(0);
    const select = (await screen.findAllByRole("combobox", { name: "Endpoint for user@example.com" }))[0];
    await user.click(select);
    await user.click(await screen.findByRole("option", { name: /Singapore.*203\.0\.113\.9/i }));
    expect(screen.getByText("1 pending change")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Preview 1 change" }));
    await waitFor(() =>
      expect(mocks.previewBindings).toHaveBeenCalledWith([
        { identity: "codex:abc", endpointId: "sg-socks" },
      ]),
    );
    const confirm = await screen.findByRole("dialog", { name: "Apply binding changes" });
    await user.click(within(confirm).getByRole("button", { name: "Apply atomically" }));
    await waitFor(() =>
      expect(mocks.applyBindings).toHaveBeenCalledWith(
        [{ identity: "codex:abc", endpointId: "sg-socks" }],
        "rev-7",
        true,
      ),
    );
  });

  test("supports explicit unbind through the same preview flow", async () => {
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
    await user.click(await screen.findByRole("tab", { name: "Account bindings" }));
    await user.click(screen.getAllByRole("button", { name: "Unbind user@example.com" })[0]);
    await user.click(screen.getByRole("button", { name: "Preview 1 change" }));
    await waitFor(() =>
      expect(mocks.previewBindings).toHaveBeenCalledWith([
        { identity: "codex:abc", endpointId: "" },
      ]),
    );
  });
});
