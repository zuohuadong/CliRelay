import type { ComponentProps } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HashRouter } from "react-router-dom";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { AuthFileDetailModal } from "@/modules/auth-files/components/AuthFileDetailModal";
import i18n from "@/i18n";

type DetailModalProps = ComponentProps<typeof AuthFileDetailModal>;

const chartOptions = vi.hoisted(() => [] as any[]);

vi.mock("@/modules/ui/charts/EChart", () => ({
  EChart: ({ option, className }: { option: any; className?: string }) => {
    chartOptions.push(option);
    return (
      <div
        className={className}
        data-testid="auth-file-trend-chart"
        data-x-axis={JSON.stringify(option?.xAxis?.data ?? [])}
        data-series={JSON.stringify(option?.series ?? [])}
      >
        chart
      </div>
    );
  },
}));

const basePrefixProxyEditor: DetailModalProps["prefixProxyEditor"] = {
  open: true,
  fileName: "codex.json",
  loading: false,
  saving: false,
  error: null,
  json: { prefix: "team-a", proxy_url: "http://127.0.0.1:7890" },
  prefix: "team-a",
  proxyUrl: "http://127.0.0.1:7890",
  egressMode: "fixed_endpoint",
  subscriptionStartedAt: "2026-04-01T08:30",
  subscriptionPeriod: "monthly",
};

const renderDetailModal = (overrides: Partial<DetailModalProps> = {}) => {
  const props: DetailModalProps = {
    open: true,
    detailFile: {
      name: "codex.json",
      label: "Codex Primary",
      type: "codex",
      size: 256,
      account_type: "oauth",
    },
    detailLoading: false,
    detailText: '{"token":"abc","nested":{"enabled":true}}',
    detailTab: "usage",
    setDetailOpen: vi.fn(),
    setDetailTab: vi.fn(),
    detailTrendWindow: "5h",
    setDetailTrendWindow: vi.fn(),
    detailTrendLoading: false,
    detailTrendError: null,
    detailTrend: {
      auth_index: "auth-1",
      days: 7,
      hours: 5,
      request_total: 3,
      cycle_request_total: 2,
      cycle_start: "2026-04-27T16:01:21Z",
      daily_usage: [
        { date: "2026-04-24", requests: 0 },
        { date: "2026-04-25", requests: 0 },
        { date: "2026-04-26", requests: 0 },
        { date: "2026-04-27", requests: 1 },
        { date: "2026-04-28", requests: 0 },
        { date: "2026-04-29", requests: 0 },
        { date: "2026-04-30", requests: 2 },
      ],
      hourly_usage: [{ hour: "2026-04-30 16:00", requests: 1 }],
      quota_series: [
        {
          quota_key: "code_5h",
          quota_label: "m_quota.code_5h",
          window_seconds: 18000,
          points: [{ timestamp: "2026-04-30T16:01:47Z", percent: 92 }],
        },
      ],
    },
    refreshDetailTrend: vi.fn(async () => undefined),
    loadModelsForDetail: vi.fn(async () => undefined),
    loadModelOwnerGroups: vi.fn(async () => undefined),
    modelsLoading: false,
    modelsError: null,
    modelsList: [
      { id: "gpt-5.1", display_name: "GPT 5.1", owned_by: "openai" },
      { id: "gpt-5.1-mini", owned_by: "openai" },
    ],
    modelsFileType: "codex",
    modelOwnerGroupsLoading: false,
    mappedModelOwnerGroup: null,
    mappedModelOwnerValue: "",
    excluded: {},
    prefixProxyEditor: basePrefixProxyEditor,
    setPrefixProxyEditor: vi.fn(),
    prefixProxyDirty: true,
    savePrefixProxy: vi.fn(async () => undefined),
    egressBinding: {
      identity: "codex:abc",
      authId: "codex.json",
      accountLabel: "Codex Primary",
      endpointId: "primary",
      endpointName: "Primary egress",
      bound: true,
    },
    egressEndpoints: [
      {
        id: "primary",
        name: "Primary egress",
        protocol: "socks5",
        host: "10.77.0.2",
        port: 1080,
        enabled: true,
        status: "healthy",
        latencyMs: 32,
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
      {
        id: "secondary",
        name: "Secondary egress",
        protocol: "socks5",
        host: "10.77.0.3",
        port: 1080,
        enabled: true,
        status: "healthy",
        latencyMs: 48,
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
    ],
    egressBindingSaving: false,
    previewEgressBinding: vi.fn(async (assignment) => ({
      expectedRevision: "rev-2",
      assignments: [assignment],
      changeCount: 1,
      affectedAccounts: ["Codex Primary"],
      blockers: [],
      warnings: [],
      valid: true,
    })),
    applyEgressBinding: vi.fn(async () => undefined),
    channelEditor: {
      open: true,
      fileName: "codex.json",
      label: "Codex Primary",
      saving: false,
      error: null,
    },
    setChannelEditor: vi.fn(),
    saveChannelEditor: vi.fn(async () => true),
    ...overrides,
  };

  render(
    <HashRouter>
      <AuthFileDetailModal {...props} />
    </HashRouter>,
  );
  return props;
};

describe("AuthFileDetailModal", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("en");
    window.location.hash = "#/auth-files";
    window.localStorage.clear();
    chartOptions.length = 0;
  });

  test("uses usage trend as the primary view for Codex files", () => {
    renderDetailModal();

    expect(screen.queryByTestId("auth-file-json-reader")).not.toBeInTheDocument();
    expect(screen.queryByText(/"token"/)).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Content" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Info" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Channel" })).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Usage" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Fields" })).toBeInTheDocument();
    expect(screen.getByText("Last 7 days requests")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(screen.getByText("Current weekly cycle")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "View: codex.json" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download" })).toBeEnabled();
  });

  test("keeps the rendered trend visible while a background refresh is running", () => {
    renderDetailModal({ detailTrendLoading: true });

    expect(screen.getByText("Quota and request trends")).toBeInTheDocument();
    expect(screen.getByText("Last 7 days requests")).toBeInTheDocument();
    expect(screen.queryByText("Loading...")).not.toBeInTheDocument();
  });

  test("does not render quota sample summary cards below the trend chart", () => {
    renderDetailModal();

    expect(screen.queryByTestId("auth-file-quota-series-list")).not.toBeInTheDocument();
    expect(screen.queryByText(/samples/)).not.toBeInTheDocument();
    expect(screen.queryByText(/resets/)).not.toBeInTheDocument();
  });

  test("five-hour trend uses only the latest five hourly buckets and maps quota timestamps to local hours", () => {
    const localQuotaAt15 = new Date(2026, 4, 1, 15, 9, 0).toISOString();
    const oldQuotaAt22 = new Date(2026, 3, 30, 22, 15, 0).toISOString();

    renderDetailModal({
      detailTrend: {
        auth_index: "auth-1",
        days: 7,
        hours: 5,
        request_total: 12,
        cycle_request_total: 12,
        cycle_start: "2026-04-28T05:34:34Z",
        daily_usage: [],
        hourly_usage: [
          { hour: "2026-05-01 11:00", requests: 0 },
          { hour: "2026-05-01 12:00", requests: 2 },
          { hour: "2026-05-01 13:00", requests: 1 },
          { hour: "2026-05-01 14:00", requests: 1 },
          { hour: "2026-05-01 15:00", requests: 10 },
        ],
        quota_series: [
          {
            quota_key: "code_5h",
            quota_label: "m_quota.code_5h",
            window_seconds: 18000,
            points: [
              { timestamp: oldQuotaAt22, percent: 100 },
              { timestamp: localQuotaAt15, percent: 94 },
            ],
          },
        ],
      },
    });

    const chart = screen.getByTestId("auth-file-trend-chart");
    expect(JSON.parse(chart.dataset.xAxis ?? "[]")).toEqual([
      "05-01 11:00",
      "05-01 12:00",
      "05-01 13:00",
      "05-01 14:00",
      "05-01 15:00",
    ]);

    const series = JSON.parse(chart.dataset.series ?? "[]");
    expect(series[0].data).toEqual([0, 2, 1, 1, 10]);
    expect(series[1].data).toEqual([null, null, null, null, 94]);
  });

  test("renders models as a compact list without raw field labels", () => {
    renderDetailModal({ detailTab: "models" });

    const list = screen.getByTestId("auth-file-models-list");
    expect(within(list).getByText("gpt-5.1")).toBeInTheDocument();
    expect(within(list).getByText("GPT 5.1")).toBeInTheDocument();
    expect(within(list).getAllByText("openai")).toHaveLength(2);
    expect(screen.getByText("2 items")).toBeInTheDocument();
    expect(screen.queryByText(/display_name:/)).not.toBeInTheDocument();
    expect(screen.queryByText(/owned_by:/)).not.toBeInTheDocument();
  });

  test("keeps field editing controls and exposes an actionable fixed-egress selector", () => {
    renderDetailModal({ detailTab: "fields" });

    const body = screen.getByTestId("auth-file-detail-body");
    const scroller = screen.getByTestId("auth-file-detail-scroll");
    const grid = screen.getByTestId("auth-file-fields-grid");
    expect(body.className).toContain("!overflow-hidden");
    expect(scroller.className).toContain("overflow-y-auto");
    expect(scroller).not.toContainElement(screen.getByRole("tablist"));
    expect(grid.className).not.toMatch(/\bborder\b/);
    expect(grid.className).not.toContain("divide-y");
    expect(within(grid).getByPlaceholderText("e.g. team-a")).toHaveValue("team-a");
    expect(
      within(grid).getByRole("combobox", { name: "Proxy egress endpoint" }),
    ).toBeInTheDocument();
    expect(
      within(grid).queryByPlaceholderText("e.g. http://127.0.0.1:7890"),
    ).not.toBeInTheDocument();
    expect(within(grid).getByLabelText(/Subscription start/)).toBeInTheDocument();
    expect(screen.queryByTestId("auth-file-fields-preview")).not.toBeInTheDocument();
    expect(screen.queryByText(/"prefix"/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  test("shows the ordinary proxy editor when a Codex auth uses shared-proxy mode", () => {
    renderDetailModal({
      detailTab: "fields",
      prefixProxyEditor: {
        ...basePrefixProxyEditor,
        egressMode: "shared_proxy",
      },
    });

    const grid = screen.getByTestId("auth-file-fields-grid");
    expect(within(grid).getByRole("combobox", { name: "Egress mode" })).toBeInTheDocument();
    expect(within(grid).getByText("Shared proxy")).toBeInTheDocument();
    expect(within(grid).getByPlaceholderText("e.g. http://127.0.0.1:7890")).toHaveValue(
      "http://127.0.0.1:7890",
    );
    expect(
      within(grid).queryByRole("combobox", { name: "Proxy egress endpoint" }),
    ).not.toBeInTheDocument();
  });

  test("routes egress management through HashRouter", async () => {
    const user = userEvent.setup();
    renderDetailModal({ detailTab: "fields" });

    const link = screen.getByRole("link", { name: "Manage in Egress Bindings" });
    expect(link).toHaveAttribute("href", "#/egress");
    await user.click(link);
    await waitFor(() => expect(window.location.hash).toBe("#/egress"));
  });

  test("previews and applies a fixed endpoint change, including explicit unbind", async () => {
    const user = userEvent.setup();
    const previewEgressBinding = vi.fn(async (assignment) => ({
      expectedRevision: "rev-2",
      assignments: [assignment],
      changeCount: 1,
      affectedAccounts: ["Codex Primary"],
      blockers: [],
      warnings: [],
      valid: true,
    }));
    const applyEgressBinding = vi.fn(async () => undefined);
    renderDetailModal({
      detailTab: "fields",
      previewEgressBinding,
      applyEgressBinding,
    });

    await user.click(screen.getByRole("combobox", { name: "Proxy egress endpoint" }));
    await user.click(
      await screen.findByRole("option", { name: /Secondary egress.*203\.0\.113\.9/i }),
    );
    await user.click(screen.getByRole("button", { name: "Preview egress change" }));
    await waitFor(() =>
      expect(previewEgressBinding).toHaveBeenCalledWith({
        identity: "codex:abc",
        endpointId: "secondary",
      }),
    );

    const confirm = await screen.findByRole("dialog", { name: "Apply proxy egress change" });
    await user.click(within(confirm).getByRole("button", { name: "Apply binding" }));
    await waitFor(() =>
      expect(applyEgressBinding).toHaveBeenCalledWith(
        [{ identity: "codex:abc", endpointId: "secondary" }],
        "rev-2",
      ),
    );

    await user.click(screen.getByRole("combobox", { name: "Proxy egress endpoint" }));
    await user.click(await screen.findByRole("option", { name: "No endpoint" }));
    await user.click(screen.getByRole("button", { name: "Preview egress change" }));
    await waitFor(() =>
      expect(previewEgressBinding).toHaveBeenLastCalledWith({
        identity: "codex:abc",
        endpointId: "",
      }),
    );
  });

  test("lets Antigravity select, switch, and unbind a proxy endpoint without Codex modes", async () => {
    const user = userEvent.setup();
    const previewEgressBinding = vi.fn(async (assignment) => ({
      expectedRevision: "rev-3",
      assignments: [assignment],
      changeCount: 1,
      affectedAccounts: ["Antigravity Primary"],
      blockers: [],
      warnings: [],
      valid: true,
    }));
    const applyEgressBinding = vi.fn(async () => undefined);
    renderDetailModal({
      detailTab: "fields",
      detailFile: {
        name: "antigravity.json",
        label: "Antigravity Primary",
        type: "antigravity",
        provider: "antigravity",
        size: 256,
        account_type: "oauth",
      },
      prefixProxyEditor: {
        ...basePrefixProxyEditor,
        fileName: "antigravity.json",
        json: { type: "antigravity", id: "antigravity-account" },
        prefix: "",
        proxyUrl: "",
      },
      egressBinding: {
        identity: "antigravity:abc",
        provider: "antigravity",
        authId: "antigravity.json",
        accountLabel: "Antigravity Primary",
        endpointId: "primary",
        endpointName: "Primary egress",
        bound: true,
      },
      previewEgressBinding,
      applyEgressBinding,
    });

    expect(screen.queryByRole("combobox", { name: "Egress mode" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: "Proxy egress endpoint" }));
    await user.click(
      await screen.findByRole("option", { name: /Secondary egress.*203\.0\.113\.9/i }),
    );
    await user.click(screen.getByRole("button", { name: "Preview egress change" }));
    await waitFor(() =>
      expect(previewEgressBinding).toHaveBeenCalledWith({
        identity: "antigravity:abc",
        endpointId: "secondary",
      }),
    );

    const confirm = await screen.findByRole("dialog", { name: "Apply proxy egress change" });
    await user.click(within(confirm).getByRole("button", { name: "Apply binding" }));
    await waitFor(() =>
      expect(applyEgressBinding).toHaveBeenCalledWith(
        [{ identity: "antigravity:abc", endpointId: "secondary" }],
        "rev-3",
      ),
    );

    await user.click(screen.getByRole("combobox", { name: "Proxy egress endpoint" }));
    await user.click(await screen.findByRole("option", { name: "No endpoint" }));
    await user.click(screen.getByRole("button", { name: "Preview egress change" }));
    await waitFor(() =>
      expect(previewEgressBinding).toHaveBeenLastCalledWith({
        identity: "antigravity:abc",
        endpointId: "",
      }),
    );
  });

  test("keeps the OAuth channel rename action available in fields", () => {
    const saveChannelEditor = vi.fn(async () => true);
    const props = renderDetailModal({
      detailTab: "fields",
      channelEditor: {
        open: true,
        fileName: "codex.json",
        label: "Codex Team A",
        saving: false,
        error: null,
      },
      saveChannelEditor,
    });

    fireEvent.change(screen.getByPlaceholderText("e.g. Gemini Primary"), {
      target: { value: "Codex Team B" },
    });
    expect(props.setChannelEditor).toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(saveChannelEditor).toHaveBeenCalled();
  });

  test("shows the channel alias editor for Kimi auth files without account_type metadata", () => {
    renderDetailModal({
      detailTab: "fields",
      detailFile: {
        name: "kimi-1770000000000.json",
        label: "Kimi Team A",
        type: "kimi",
        provider: "kimi",
        size: 256,
      },
      prefixProxyEditor: {
        ...basePrefixProxyEditor,
        fileName: "kimi-1770000000000.json",
        json: { type: "kimi", refresh_token: "kimi-refresh-token" },
        prefix: "",
        proxyUrl: "",
      },
      channelEditor: {
        open: true,
        fileName: "kimi-1770000000000.json",
        label: "Kimi Team A",
        saving: false,
        error: null,
      },
    });

    expect(screen.getByText("Channel name")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("e.g. Gemini Primary")).toHaveValue("Kimi Team A");
  });
});
