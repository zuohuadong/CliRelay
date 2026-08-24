import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { ToastProvider } from "@/modules/ui/ToastProvider";
import { ThemeProvider } from "@/modules/ui/ThemeProvider";
import { AuthFilesPage } from "@/modules/auth-files/AuthFilesPage";
import type { AuthFileItem } from "@/lib/http/types";
import type { EgressBinding, EgressEndpoint, EgressOverview } from "@/lib/http/apis/egress";

const mocks = vi.hoisted(() => ({
  list: vi.fn<() => Promise<{ files: AuthFileItem[] }>>(async () => ({
    files: [],
  })),
  getEntityStats: vi.fn(async () => ({ source: [], auth_index: [] })),
  startAuth: vi.fn(async () => ({ url: "", state: "" })),
  getAuthStatus: vi.fn(async () => ({ status: "waiting" })),
  submitCallback: vi.fn(async () => ({})),
  iflowCookieAuth: vi.fn(async () => ({ status: "ok" })),
  importCredential: vi.fn(async () => ({})),
  listEndpoints: vi.fn<() => Promise<EgressEndpoint[]>>(async () => []),
  getOverview: vi.fn<() => Promise<EgressOverview>>(),
  listBindings: vi.fn<() => Promise<EgressBinding[]>>(async () => []),
  previewBindings: vi.fn(),
  applyBindings: vi.fn(),
}));

vi.mock("@/lib/http/apis", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/lib/http/apis")>();
  return {
    ...mod,
    authFilesApi: { ...mod.authFilesApi, list: mocks.list },
    usageApi: { ...mod.usageApi, getEntityStats: mocks.getEntityStats },
    oauthApi: {
      ...mod.oauthApi,
      startAuth: mocks.startAuth,
      getAuthStatus: mocks.getAuthStatus,
      submitCallback: mocks.submitCallback,
      iflowCookieAuth: mocks.iflowCookieAuth,
    },
    vertexApi: { ...mod.vertexApi, importCredential: mocks.importCredential },
  };
});

vi.mock("@/lib/http/apis/egress", () => ({
  egressApi: {
    listEndpoints: mocks.listEndpoints,
    getOverview: mocks.getOverview,
    listBindings: mocks.listBindings,
    previewBindings: mocks.previewBindings,
    applyBindings: mocks.applyBindings,
  },
}));

beforeEach(() => {
  window.localStorage.clear();
  window.sessionStorage.clear();
  mocks.list.mockClear();
  mocks.getEntityStats.mockClear();
  mocks.startAuth.mockClear();
  mocks.getAuthStatus.mockClear();
  mocks.submitCallback.mockClear();
  mocks.iflowCookieAuth.mockClear();
  mocks.importCredential.mockClear();
  mocks.listEndpoints.mockReset();
  mocks.getOverview.mockReset();
  mocks.listBindings.mockReset();
  mocks.previewBindings.mockReset();
  mocks.applyBindings.mockReset();
  mocks.listEndpoints.mockResolvedValue([
    {
      id: "hk",
      name: "HK Egress",
      protocol: "socks5",
      host: "100.64.0.2",
      port: 1080,
      enabled: true,
      status: "healthy",
      latencyMs: 88,
      publicIp: "203.0.113.8",
      eligible: true,
      runtimeReady: true,
      eligibility: {
        selectable: true,
        eligible: true,
        runtimeReady: true,
        healthFresh: true,
        publicIpMatches: true,
        reasonCodes: [],
        duplicatePublicIp: false,
      },
    },
  ]);
  mocks.getOverview.mockResolvedValue({
    enabled: true,
    revision: "rev-1",
    scope: "application_egress",
    policy: {
      bindingMode: "exclusive",
      failureMode: "fail_closed",
      readinessScope: "application_egress",
      hostKillSwitchEnforced: false,
    },
    readiness: {
      scope: "application_egress",
      verdict: "ready",
      readyToEnable: true,
      codexOAuthAllowed: true,
      blockers: [],
      warnings: [],
      notEvaluated: [],
    },
    counts: {
      endpoints: 1,
      enabledEndpoints: 1,
      bindings: 0,
      codexAuths: 0,
      boundCodexAuths: 0,
      unboundCodexAuths: 0,
      missingAccountId: 0,
      boundEndpointNotReady: 0,
    },
  });
  mocks.listBindings.mockResolvedValue([]);
  mocks.previewBindings.mockResolvedValue({
    expectedRevision: "rev-1",
    assignments: [],
    changeCount: 0,
    affectedAccounts: [],
    blockers: [],
    warnings: [],
    valid: true,
  });
  mocks.applyBindings.mockResolvedValue({ revision: "rev-2", applied: 1 });
});

describe("AuthFilesPage OAuth login dialog", () => {
  test("shows a bound Codex egress summary directly on the auth file card", async () => {
    window.localStorage.setItem("authFilesPage.filesViewMode.v1", JSON.stringify("cards"));
    mocks.list.mockResolvedValueOnce({
      files: [
        {
          name: "codex-account.json",
          path: "/tmp/codex-account.json",
          type: "codex",
          provider: "codex",
          size: 2048,
          modified: Date.now(),
          disabled: false,
        },
      ],
    });
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:account-1",
        authId: "codex-account.json",
        accountLabel: "account@example.com",
        endpointId: "hk",
        endpointName: "HK Egress",
        bound: true,
      },
    ]);

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    const cards = await screen.findByTestId("auth-files-cards");
    const summary = await within(cards).findByLabelText(
      "Proxy egress endpoint: HK Egress · SOCKS5 · Healthy · Runtime ready · Latency 88 ms",
    );
    expect(summary).toHaveTextContent("HK Egress");
    expect(summary).toHaveTextContent("SOCKS5");
    expect(summary).toHaveTextContent("Healthy");
    expect(summary).toHaveTextContent("Runtime ready");
    expect(summary).toHaveTextContent("88 ms");
  });

  test("shows the Codex egress summary in the auth files table", async () => {
    window.localStorage.setItem("authFilesPage.filesViewMode.v1", JSON.stringify("table"));
    mocks.list.mockResolvedValueOnce({
      files: [
        {
          name: "codex-table.json",
          path: "/tmp/codex-table.json",
          type: "codex",
          provider: "codex",
          size: 2048,
          modified: Date.now(),
          disabled: false,
        },
      ],
    });
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:table-account",
        authId: "codex-table.json",
        accountLabel: "table@example.com",
        endpointId: "hk",
        endpointName: "HK Egress",
        bound: true,
      },
    ]);

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    expect(
      await screen.findByRole("columnheader", { name: "Proxy egress endpoint" }),
    ).toBeInTheDocument();
    expect(
      await screen.findByLabelText(
        "Proxy egress endpoint: HK Egress · SOCKS5 · Healthy · Runtime ready · Latency 88 ms",
      ),
    ).toBeInTheDocument();
  });

  test("marks a healthy endpoint unavailable when the fixed egress runtime is disabled", async () => {
    window.localStorage.setItem("authFilesPage.filesViewMode.v1", JSON.stringify("cards"));
    mocks.list.mockResolvedValueOnce({
      files: [
        {
          name: "codex-disabled-runtime.json",
          path: "/tmp/codex-disabled-runtime.json",
          type: "codex",
          provider: "codex",
          size: 2048,
          modified: Date.now(),
          disabled: false,
        },
      ],
    });
    mocks.getOverview.mockResolvedValue({
      ...(await mocks.getOverview()),
      enabled: false,
      readiness: {
        scope: "application_egress",
        verdict: "ready",
        readyToEnable: true,
        codexOAuthAllowed: false,
        blockers: [],
        warnings: [],
        notEvaluated: [],
      },
    });
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:disabled-runtime",
        authId: "codex-disabled-runtime.json",
        accountLabel: "disabled-runtime@example.com",
        endpointId: "hk",
        endpointName: "HK Egress",
        bound: true,
      },
    ]);

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    const summary = await screen.findByLabelText(
      "Proxy egress endpoint: HK Egress · SOCKS5 · Healthy · Current · unavailable · Latency 88 ms",
    );
    expect(summary).toHaveTextContent("Healthy");
    expect(summary).toHaveTextContent("Current · unavailable");
    expect(summary).not.toHaveTextContent("Runtime ready");
  });

  test("refreshes egress status together with the auth files list", async () => {
    const user = userEvent.setup();
    mocks.list.mockResolvedValue({ files: [] });

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await waitFor(() => expect(mocks.getOverview).toHaveBeenCalledTimes(1));
    const refreshButton = screen.getByRole("button", { name: "Refresh" });
    await waitFor(() => expect(refreshButton).toBeEnabled());
    await user.click(refreshButton);

    await waitFor(() => expect(mocks.getOverview).toHaveBeenCalledTimes(2));
    expect(mocks.listEndpoints).toHaveBeenCalledTimes(2);
    expect(mocks.listBindings).toHaveBeenCalledTimes(2);
  });

  test("opens OAuth dialog with provider/iFlow/Vertex tabs", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    const openBtn = await screen.findByRole("button", {
      name: "Add OAuth Login",
    });
    await user.click(openBtn);

    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);

    expect(scoped.getByText("Add OAuth Login")).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "Codex OAuth" })).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "Anthropic OAuth" })).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "Qwen OAuth" })).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "iFlow OAuth" })).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "xAI OAuth" })).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "iFlow Cookie Auth" })).toBeInTheDocument();
    expect(scoped.getByRole("tab", { name: "Vertex Credential Import" })).toBeInTheDocument();
  });

  test("starts iFlow OAuth from its provider tab", async () => {
    const user = userEvent.setup();
    mocks.startAuth.mockResolvedValueOnce({
      url: "https://iflow.example/oauth",
      state: "iflow-state",
    });

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));

    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);
    await user.click(scoped.getByRole("tab", { name: "iFlow OAuth" }));
    await user.click(scoped.getByRole("button", { name: "Start authorization" }));

    await waitFor(() => {
      expect(mocks.startAuth).toHaveBeenCalledWith("iflow", {});
    });
  });

  test("places the Codex egress selector below the OAuth provider tabs", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));

    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);
    const tabs = scoped.getByRole("tablist");
    const egressSelect = await scoped.findByRole("combobox", {
      name: "Authorization Egress",
    });

    expect(tabs.compareDocumentPosition(egressSelect) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(
      0,
    );
  });

  test("allows a ready endpoint to start Codex OAuth while global migration readiness is blocked", async () => {
    const user = userEvent.setup();
    mocks.getOverview.mockResolvedValue({
      ...(await mocks.getOverview()),
      readiness: {
        scope: "application_egress",
        verdict: "blocked",
        readyToEnable: false,
        codexOAuthAllowed: true,
        blockers: [
          { code: "missing_account_id", message: "One Codex account has no stable identity." },
          { code: "bound_endpoint_not_ready", message: "One bound endpoint is unhealthy." },
        ],
        warnings: [],
        notEvaluated: [],
      },
    });
    mocks.listEndpoints.mockResolvedValue([
      {
        id: "hk",
        name: "HK Egress",
        protocol: "socks5",
        host: "100.64.0.2",
        port: 1080,
        enabled: true,
        status: "healthy",
        latencyMs: 88,
        publicIp: "203.0.113.8",
        eligible: true,
        runtimeReady: true,
        eligibility: {
          selectable: true,
          eligible: true,
          runtimeReady: true,
          healthFresh: true,
          publicIpMatches: true,
          reasonCodes: [],
          duplicatePublicIp: false,
        },
      },
    ]);

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));

    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);
    const egressSelect = await scoped.findByRole("combobox", {
      name: "Authorization Egress",
    });
    expect(scoped.getByRole("button", { name: "Start authorization" })).toBeDisabled();
    await user.click(egressSelect);
    await user.click(
      await screen.findByRole("option", {
        name: /HK Egress.*203\.0\.113\.8.*healthy/i,
      }),
    );
    await user.click(scoped.getByRole("button", { name: "Start authorization" }));

    await waitFor(() => {
      expect(mocks.startAuth).toHaveBeenCalledWith("codex", { egressId: "hk" });
    });
  });

  test("blocks Codex authorization when no enabled endpoint exists", async () => {
    const user = userEvent.setup();
    mocks.listEndpoints.mockResolvedValue([]);
    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("No enabled egress endpoint is available."),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Start authorization" })).toBeDisabled();
    expect(mocks.startAuth).not.toHaveBeenCalled();
  });

  test("excludes endpoints already occupied by exclusive bindings from new Codex OAuth", async () => {
    const user = userEvent.setup();
    mocks.listBindings.mockResolvedValue([
      {
        identity: "codex:occupied",
        authId: "occupied.json",
        accountLabel: "occupied@example.com",
        endpointId: "hk",
        bound: true,
      },
    ]);
    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("No enabled egress endpoint is available."),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Start authorization" })).toBeDisabled();
  });

  test("blocks Codex when fixed egress runtime is disabled and explains browser versus server egress scope", async () => {
    const user = userEvent.setup();
    mocks.getOverview.mockResolvedValue({
      ...(await mocks.getOverview()),
      enabled: false,
      readiness: {
        scope: "application_egress",
        verdict: "ready",
        readyToEnable: true,
        codexOAuthAllowed: false,
        blockers: [],
        warnings: [],
        notEvaluated: [],
      },
    });
    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));
    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);
    expect(
      scoped.getByText(/Fixed egress runtime is disabled, so Codex authorization is blocked/),
    ).toBeInTheDocument();
    expect(scoped.getByText(/Browser authorization uses the operator network/)).toBeInTheDocument();
    expect(scoped.getByRole("button", { name: "Start authorization" })).toBeDisabled();

    await user.click(scoped.getByRole("tab", { name: "Anthropic OAuth" }));
    expect(scoped.getByRole("button", { name: "Start authorization" })).toBeEnabled();
  });

  test("shows translated callback guidance instead of raw oauth keys after starting authorization", async () => {
    const user = userEvent.setup();
    mocks.startAuth.mockResolvedValueOnce({
      url: "https://example.com/oauth",
      state: "oauth-state",
    });

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));

    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);
    await user.click(scoped.getByRole("combobox", { name: "Authorization Egress" }));
    await user.click(await screen.findByRole("option", { name: /HK Egress/ }));
    await user.click(scoped.getByRole("button", { name: "Start authorization" }));

    expect(await scoped.findByText("Status")).toBeInTheDocument();
    expect(scoped.getByText("Callback URL")).toBeInTheDocument();
    expect(
      scoped.getByText(
        "After authorizing in the browser, the browser address bar contains the callback URL. Copy the full URL and submit it below.",
      ),
    ).toBeInTheDocument();
    expect(scoped.queryByText("oauth.status")).not.toBeInTheDocument();
    expect(scoped.queryByText("oauth.callback")).not.toBeInTheDocument();
  });

  test("keeps the dialog open until OAuth completes and the new auth file is listed", async () => {
    const user = userEvent.setup();
    window.localStorage.setItem("authFilesPage.filesViewMode.v1", JSON.stringify("cards"));
    mocks.startAuth.mockResolvedValueOnce({
      url: "https://example.com/oauth",
      state: "oauth-state",
    });
    mocks.getAuthStatus.mockResolvedValue({ status: "wait" });
    mocks.list
      .mockResolvedValueOnce({ files: [] })
      .mockResolvedValueOnce({ files: [] })
      .mockResolvedValueOnce({
        files: [
          {
            name: "codex-new.json",
            type: "codex",
            size: 2048,
            modified: Date.now(),
            disabled: false,
          },
        ],
      });

    render(
      <MemoryRouter initialEntries={["/auth-files"]}>
        <ThemeProvider>
          <ToastProvider>
            <Routes>
              <Route path="/auth-files" element={<AuthFilesPage />} />
            </Routes>
          </ToastProvider>
        </ThemeProvider>
      </MemoryRouter>,
    );

    await user.click(await screen.findByRole("button", { name: "Add OAuth Login" }));

    const dialog = await screen.findByRole("dialog");
    const scoped = within(dialog);
    await user.click(scoped.getByRole("combobox", { name: "Authorization Egress" }));
    await user.click(await screen.findByRole("option", { name: /HK Egress/ }));
    await user.click(scoped.getByRole("button", { name: "Start authorization" }));
    await waitFor(() => expect(mocks.startAuth).toHaveBeenCalledTimes(1));

    await user.type(
      scoped.getByPlaceholderText("Paste the full callback URL from browser"),
      "http://localhost:1455/auth/callback?code=test-code&state=test-state",
    );
    await user.click(scoped.getByRole("button", { name: "Submit callback" }));

    await waitFor(() => expect(mocks.submitCallback).toHaveBeenCalledTimes(1));
    await new Promise((resolve) => window.setTimeout(resolve, 250));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.queryByText("codex-new.json")).not.toBeInTheDocument();

    mocks.getAuthStatus.mockResolvedValueOnce({ status: "ok" });
    await waitFor(() => expect(mocks.list).toHaveBeenCalledTimes(3), {
      timeout: 5000,
    });

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(await screen.findByTestId("auth-files-cards")).toHaveTextContent("codex-new.json");
    await waitFor(() => expect(mocks.listBindings).toHaveBeenCalledTimes(2));
  }, 12000);
});
