import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CheckCircle2,
  Filter,
  Link2,
  Network,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  Trash2,
  Unplug,
} from "lucide-react";
import {
  egressApi,
  type EgressBinding,
  type EgressBindingAssignment,
  type EgressBindingPreview,
  type EgressEndpoint,
  type EgressEndpointAction,
  type EgressEndpointImpact,
  type EgressEndpointInput,
  type EgressOverview,
} from "@/lib/http/apis/egress";
import { EndpointModal } from "@/modules/egress/EndpointModal";
import { EndpointSelect } from "@/modules/egress/EndpointSelect";
import { Button } from "@/modules/ui/Button";
import { Card } from "@/modules/ui/Card";
import { ConfirmModal } from "@/modules/ui/ConfirmModal";
import { TextInput } from "@/modules/ui/Input";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/modules/ui/Tabs";
import { useToast } from "@/modules/ui/ToastProvider";
import { VirtualTable, type VirtualTableColumn } from "@/modules/ui/VirtualTable";

type EgressTab = "overview" | "endpoints" | "bindings";
type BindingFilter = "all" | "unbound" | "issues";

const EMPTY_OVERVIEW: EgressOverview = {
  enabled: false,
  revision: "",
  scope: "application_egress",
  policy: {
    bindingMode: "exclusive",
    failureMode: "fail_closed",
    readinessScope: "application_egress",
    hostKillSwitchEnforced: false,
  },
  readiness: {
    scope: "application_egress",
    verdict: "blocked",
    readyToEnable: false,
    codexOAuthAllowed: false,
    blockers: [],
    warnings: [],
    notEvaluated: [],
  },
  counts: {
    endpoints: 0,
    enabledEndpoints: 0,
    bindings: 0,
    codexAuths: 0,
    boundCodexAuths: 0,
    unboundCodexAuths: 0,
    missingAccountId: 0,
    boundEndpointNotReady: 0,
  },
};

interface PendingEndpointAction {
  endpoint: EgressEndpoint;
  action: EgressEndpointAction;
  impact: EgressEndpointImpact;
  input?: EgressEndpointInput;
}

const formatDateTime = (value: string | undefined): string => {
  if (!value) return "--";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
};

function StatusBadge({
  tone,
  children,
}: {
  tone: "green" | "amber" | "rose" | "sky" | "slate";
  children: string;
}) {
  const classes = {
    green: "bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300",
    amber: "bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-200",
    rose: "bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300",
    sky: "bg-sky-50 text-sky-700 dark:bg-sky-500/10 dark:text-sky-300",
    slate: "bg-slate-100 text-slate-600 dark:bg-neutral-900 dark:text-slate-300",
  }[tone];
  return (
    <span
      className={`inline-flex shrink-0 whitespace-nowrap rounded-full px-2 py-0.5 text-[11px] font-semibold ${classes}`}
    >
      {children}
    </span>
  );
}

export function EgressPage() {
  const { t } = useTranslation();
  const { notify } = useToast();
  const [tab, setTab] = useState<EgressTab>("overview");
  const [overview, setOverview] = useState<EgressOverview>(EMPTY_OVERVIEW);
  const [endpoints, setEndpoints] = useState<EgressEndpoint[]>([]);
  const [bindings, setBindings] = useState<EgressBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [checkingId, setCheckingId] = useState("");
  const [saving, setSaving] = useState(false);
  const [endpointModalOpen, setEndpointModalOpen] = useState(false);
  const [editingEndpoint, setEditingEndpoint] = useState<EgressEndpoint | null>(null);
  const [pendingEndpointAction, setPendingEndpointAction] = useState<PendingEndpointAction | null>(
    null,
  );
  const [bindingSearch, setBindingSearch] = useState("");
  const [bindingFilter, setBindingFilter] = useState<BindingFilter>("all");
  const [pendingAssignments, setPendingAssignments] = useState<Record<string, string>>({});
  const [bindingPreview, setBindingPreview] = useState<EgressBindingPreview | null>(null);
  const [previewingBindings, setPreviewingBindings] = useState(false);

  const reasonLabel = useCallback(
    (code: string) =>
      t(`egress.reasons.${code}`, {
        defaultValue: t("egress.reasons.unknown"),
      }),
    [t],
  );

  const refreshLists = useCallback(async () => {
    const [nextOverview, nextEndpoints, nextBindings] = await Promise.all([
      egressApi.getOverview(),
      egressApi.listEndpoints(),
      egressApi.listBindings(),
    ]);
    setOverview(nextOverview);
    setEndpoints(nextEndpoints);
    setBindings(nextBindings);
  }, []);

  const loadAll = useCallback(async () => {
    setLoading(true);
    try {
      await refreshLists();
    } catch (error) {
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.load_failed"),
      });
    } finally {
      setLoading(false);
    }
  }, [notify, refreshLists, t]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const saveEndpoint = useCallback(
    async (input: EgressEndpointInput) => {
      setSaving(true);
      try {
        if (editingEndpoint?.enabled && !input.enabled) {
          const impact = await egressApi.endpointImpact(editingEndpoint.id, "disable");
          setPendingEndpointAction({ endpoint: editingEndpoint, action: "disable", impact, input });
          return;
        }
        if (editingEndpoint) await egressApi.updateEndpoint(editingEndpoint.id, input);
        else await egressApi.createEndpoint(input);
        await refreshLists();
        setEndpointModalOpen(false);
        setEditingEndpoint(null);
        notify({ type: "success", message: t("egress.endpoints.saved") });
      } catch (error) {
        notify({
          type: "error",
          message: error instanceof Error ? error.message : t("egress.endpoints.save_failed"),
        });
      } finally {
        setSaving(false);
      }
    },
    [editingEndpoint, notify, refreshLists, t],
  );

  const checkEndpoint = useCallback(
    async (endpoint: EgressEndpoint) => {
      setCheckingId(endpoint.id);
      let checked = false;
      try {
        await egressApi.checkEndpoint(endpoint.id);
        checked = true;
      } catch (error) {
        notify({
          type: "error",
          message: error instanceof Error ? error.message : t("egress.endpoints.check_failed"),
        });
      } finally {
        try {
          await refreshLists();
          if (checked) {
            notify({ type: "success", message: t("egress.endpoints.checked") });
          }
        } catch (error) {
          notify({
            type: "error",
            message: error instanceof Error ? error.message : t("egress.load_failed"),
          });
        } finally {
          setCheckingId("");
        }
      }
    },
    [notify, refreshLists, t],
  );

  const requestEndpointAction = useCallback(
    async (endpoint: EgressEndpoint, action: EgressEndpointAction) => {
      setSaving(true);
      try {
        const impact = await egressApi.endpointImpact(endpoint.id, action);
        setPendingEndpointAction({ endpoint, action, impact });
      } catch (error) {
        notify({
          type: "error",
          message: error instanceof Error ? error.message : t("egress.endpoints.impact_failed"),
        });
      } finally {
        setSaving(false);
      }
    },
    [notify, t],
  );

  const confirmEndpointAction = useCallback(async () => {
    if (!pendingEndpointAction) return;
    setSaving(true);
    let actionApplied = false;
    try {
      const { endpoint, action, impact, input } = pendingEndpointAction;
      await egressApi.endpointAction(endpoint.id, action, impact.expectedRevision, true);
      actionApplied = true;
      if (action === "disable" && input) {
        const updateInput: Partial<EgressEndpointInput> = { ...input };
        delete updateInput.enabled;
        await egressApi.updateEndpoint(endpoint.id, updateInput);
      }
      await refreshLists();
      setPendingEndpointAction(null);
      setEndpointModalOpen(false);
      setEditingEndpoint(null);
      notify({
        type: "success",
        message: t(action === "delete" ? "egress.endpoints.deleted" : "egress.endpoints.disabled"),
      });
    } catch (error) {
      if (actionApplied) {
        await refreshLists();
        setPendingEndpointAction(null);
        setEndpointModalOpen(false);
        setEditingEndpoint(null);
      }
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.endpoints.action_failed"),
      });
    } finally {
      setSaving(false);
    }
  }, [notify, pendingEndpointAction, refreshLists, t]);

  const stageBinding = useCallback((binding: EgressBinding, endpointId: string) => {
    if (!binding.identity) return;
    setPendingAssignments((current) => {
      const next = { ...current };
      if (endpointId === binding.endpointId) delete next[binding.identity];
      else next[binding.identity] = endpointId;
      return next;
    });
  }, []);

  const bindingAssignments = useMemo<EgressBindingAssignment[]>(
    () =>
      Object.entries(pendingAssignments).map(([identity, endpointId]) => ({
        identity,
        endpointId,
      })),
    [pendingAssignments],
  );

  const filteredBindings = useMemo(() => {
    const search = bindingSearch.trim().toLowerCase();
    return bindings.filter((binding) => {
      const matchesSearch =
        !search ||
        [binding.provider, binding.accountLabel, binding.authId, binding.identity]
          .join(" ")
          .toLowerCase()
          .includes(search);
      const boundEndpoint = endpoints.find((endpoint) => endpoint.id === binding.endpointId);
      const matchesFilter =
        bindingFilter === "all" ||
        (bindingFilter === "unbound" && !binding.bound) ||
        (bindingFilter === "issues" &&
          Boolean(
            binding.error || !binding.identity || (binding.bound && !boundEndpoint?.runtimeReady),
          ));
      return matchesSearch && matchesFilter;
    });
  }, [bindingFilter, bindingSearch, bindings, endpoints]);

  const previewBindingChanges = useCallback(async () => {
    if (bindingAssignments.length === 0) return;
    setPreviewingBindings(true);
    try {
      setBindingPreview(await egressApi.previewBindings(bindingAssignments));
    } catch (error) {
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.bindings.preview_failed"),
      });
    } finally {
      setPreviewingBindings(false);
    }
  }, [bindingAssignments, notify, t]);

  const applyBindingChanges = useCallback(async () => {
    if (!bindingPreview || !bindingPreview.valid) return;
    setSaving(true);
    try {
      await egressApi.applyBindings(
        bindingAssignments,
        bindingPreview.expectedRevision || overview.revision,
        true,
      );
      await refreshLists();
      setPendingAssignments({});
      setBindingPreview(null);
      notify({ type: "success", message: t("egress.bindings.applied") });
    } catch (error) {
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.bindings.apply_failed"),
      });
    } finally {
      setSaving(false);
    }
  }, [bindingAssignments, bindingPreview, notify, overview.revision, refreshLists, t]);

  const endpointColumns = useMemo<VirtualTableColumn<EgressEndpoint>[]>(
    () => [
      {
        key: "name",
        label: t("egress.endpoints.name"),
        width: "w-44",
        render: (endpoint) => (
          <div>
            <p className="font-semibold text-slate-950 dark:text-white">{endpoint.name}</p>
            <p className="font-mono text-[11px] text-slate-500">{endpoint.id}</p>
          </div>
        ),
      },
      {
        key: "address",
        label: t("egress.endpoints.address"),
        width: "w-56",
        render: (endpoint) => (
          <div>
            <p className="font-mono text-xs">
              {endpoint.protocol}://{endpoint.host}:{endpoint.port}
            </p>
            <p className="mt-1 text-[11px] text-slate-500">
              {endpoint.hasCredentials
                ? t("egress.endpoints.credentials_configured")
                : t("egress.endpoints.no_credentials")}
            </p>
          </div>
        ),
      },
      {
        key: "status",
        label: t("egress.endpoints.status"),
        width: "w-52",
        render: (endpoint) => (
          <div className="space-y-1">
            <StatusBadge
              tone={
                endpoint.runtimeReady ? "green" : endpoint.status === "unhealthy" ? "rose" : "slate"
              }
            >
              {t(
                endpoint.runtimeReady
                  ? "egress.endpoints.runtime_ready"
                  : `egress.endpoints.${endpoint.status}`,
              )}
            </StatusBadge>
            {typeof endpoint.latencyMs === "number" ? (
              <p className="text-[11px] text-slate-500">{endpoint.latencyMs} ms</p>
            ) : null}
            {endpoint.error ? (
              <p className="max-w-52 break-words text-[11px] text-rose-600 dark:text-rose-300">
                {endpoint.error}
              </p>
            ) : null}
            {endpoint.eligibility.reasonCodes.length > 0 ? (
              <ul className="max-w-52 space-y-0.5 text-[11px] text-rose-600 dark:text-rose-300">
                {endpoint.eligibility.reasonCodes.map((code) => (
                  <li key={code}>{reasonLabel(code)}</li>
                ))}
              </ul>
            ) : null}
          </div>
        ),
      },
      {
        key: "publicIp",
        label: t("egress.endpoints.public_ip"),
        width: "w-48",
        render: (endpoint) => (
          <div className="space-y-1 font-mono text-[11px]">
            <p>
              <span className="font-sans text-slate-500">
                {t("egress.endpoints.expected_short")}:
              </span>{" "}
              {endpoint.expectedPublicIp || "--"}
            </p>
            <p>
              <span className="font-sans text-slate-500">
                {t("egress.endpoints.observed_short")}:
              </span>{" "}
              {endpoint.publicIp || t("egress.endpoints.not_checked")}
            </p>
          </div>
        ),
      },
      {
        key: "enabled",
        label: t("egress.endpoints.availability"),
        width: "w-24",
        render: (endpoint) => (
          <StatusBadge tone={endpoint.enabled ? "green" : "amber"}>
            {t(
              endpoint.enabled
                ? "egress.endpoints.enabled_short"
                : "egress.endpoints.disabled_short",
            )}
          </StatusBadge>
        ),
      },
      {
        key: "actions",
        label: t("egress.actions"),
        width: "w-28",
        headerClassName: "text-right",
        cellClassName: "text-right",
        render: (endpoint) => (
          <div className="flex justify-end gap-1">
            <Button
              size="xs"
              aria-label={t("egress.endpoints.check_label", { name: endpoint.name })}
              onClick={() => void checkEndpoint(endpoint)}
              disabled={checkingId === endpoint.id}
            >
              {checkingId === endpoint.id ? (
                <RefreshCw size={14} className="animate-spin" />
              ) : (
                <CheckCircle2 size={14} />
              )}
            </Button>
            <Button
              size="xs"
              aria-label={t("egress.endpoints.edit_label", { name: endpoint.name })}
              onClick={() => {
                setEditingEndpoint(endpoint);
                setEndpointModalOpen(true);
              }}
            >
              <Pencil size={14} />
            </Button>
            <Button
              size="xs"
              variant="ghost"
              aria-label={t("egress.endpoints.delete_label", { name: endpoint.name })}
              onClick={() => void requestEndpointAction(endpoint, "delete")}
            >
              <Trash2 size={14} />
            </Button>
          </div>
        ),
      },
    ],
    [checkEndpoint, checkingId, reasonLabel, requestEndpointAction, t],
  );

  const bindingColumns = useMemo<VirtualTableColumn<EgressBinding>[]>(
    () => [
      {
        key: "account",
        label: t("egress.bindings.account"),
        width: "w-64",
        render: (binding) => (
          <div>
            <div className="flex min-w-0 flex-wrap items-center gap-1.5">
              <p className="min-w-0 truncate font-semibold text-slate-950 dark:text-white">
                {binding.accountLabel}
              </p>
              <StatusBadge tone={binding.provider === "antigravity" ? "sky" : "slate"}>
                {binding.provider === "antigravity" ? "Antigravity" : "Codex"}
              </StatusBadge>
              {binding.planType ? (
                <StatusBadge tone="amber">{`${t("codex_quota.plan_label")}: ${binding.planType.toUpperCase()}`}</StatusBadge>
              ) : null}
            </div>
            <p className="font-mono text-[11px] text-slate-500">{binding.authId}</p>
          </div>
        ),
      },
      {
        key: "endpoint",
        label: t("egress.bindings.endpoint"),
        width: "w-72",
        render: (binding) => (
          <EndpointSelect
            value={pendingAssignments[binding.identity] ?? binding.endpointId}
            endpoints={endpoints}
            ariaLabel={t("egress.bindings.endpoint_for", { account: binding.accountLabel })}
            disabled={!binding.identity}
            allowEmpty
            onChange={(value) => stageBinding(binding, value)}
          />
        ),
      },
      {
        key: "updated",
        label: t("egress.bindings.updated_at"),
        width: "w-44",
        render: (binding) => <span className="text-xs">{formatDateTime(binding.updatedAt)}</span>,
      },
      {
        key: "identity",
        label: t("egress.bindings.identity"),
        width: "w-72",
        render: (binding) => (
          <div>
            <span className="font-mono text-[11px]">{binding.identity || "--"}</span>
            {binding.error ? (
              <p className="mt-1 text-xs text-rose-600 dark:text-rose-300">{binding.error}</p>
            ) : null}
          </div>
        ),
      },
      {
        key: "actions",
        label: t("egress.actions"),
        width: "w-20",
        headerClassName: "text-right",
        cellClassName: "text-right",
        render: (binding) => (
          <Button
            size="xs"
            variant="ghost"
            aria-label={t("egress.bindings.unbind_label", { account: binding.accountLabel })}
            disabled={
              !binding.identity || !(pendingAssignments[binding.identity] ?? binding.endpointId)
            }
            onClick={() => stageBinding(binding, "")}
          >
            <Unplug size={14} />
          </Button>
        ),
      },
    ],
    [endpoints, pendingAssignments, stageBinding, t],
  );

  const runtimeLabel = t(overview.enabled ? "egress.runtime.enabled" : "egress.runtime.disabled");
  const readinessTone = overview.readiness.verdict === "ready" ? "green" : "rose";

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col gap-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-2xl font-semibold tracking-tight text-slate-950 dark:text-white">
            {t("egress.title")}
          </h2>
          <p className="mt-1 max-w-3xl text-sm text-slate-600 dark:text-white/65">
            {t("egress.description")}
          </p>
        </div>
        <Button onClick={() => void loadAll()} disabled={loading}>
          <RefreshCw size={15} className={loading ? "animate-spin" : ""} />
          {t("common.refresh")}
        </Button>
      </div>

      <Card padding="compact">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <Tabs value={tab} onValueChange={(value) => setTab(value as EgressTab)} size="sm">
            <TabsList className="max-w-full overflow-x-auto">
              <TabsTrigger value="overview">
                <ShieldCheck size={14} />
                {t("egress.tabs.overview")}
              </TabsTrigger>
              <TabsTrigger value="endpoints">
                <Network size={14} />
                {t("egress.tabs.endpoints")}
              </TabsTrigger>
              <TabsTrigger value="bindings">
                <Link2 size={14} />
                {t("egress.tabs.bindings")}
              </TabsTrigger>
            </TabsList>
          </Tabs>
          <div className="flex flex-wrap items-center gap-2 text-xs sm:justify-end">
            <StatusBadge tone={overview.enabled ? "green" : "amber"}>{runtimeLabel}</StatusBadge>
            <StatusBadge tone={readinessTone}>
              {t(`egress.readiness.${overview.readiness.verdict}`)}
            </StatusBadge>
          </div>
        </div>
      </Card>

      <Tabs value={tab} onValueChange={(value) => setTab(value as EgressTab)}>
        <TabsContent value="overview" className="space-y-4">
          <div
            data-testid="egress-runtime-status"
            className={`flex items-start gap-3 rounded-2xl border px-4 py-3 ${
              overview.readiness.verdict === "blocked"
                ? "border-rose-200 bg-rose-50 text-rose-950 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-100"
                : "border-emerald-200 bg-emerald-50 text-emerald-950 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-100"
            }`}
          >
            <ShieldCheck size={20} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm font-semibold">{t("egress.overview.fixed_title")}</p>
                <StatusBadge tone={readinessTone}>
                  {t(`egress.readiness.${overview.readiness.verdict}`)}
                </StatusBadge>
              </div>
              <p className="mt-1 text-xs leading-5 opacity-85 sm:text-sm">
                {t("egress.overview.fixed_description")}
              </p>
              <p className="mt-1 text-xs leading-5 opacity-75">
                {t("egress.overview.fail_closed_description")}
              </p>
              {overview.readiness.blockers.length > 0 ? (
                <ul className="mt-3 grid gap-1 text-xs sm:grid-cols-2">
                  {overview.readiness.blockers.map((issue) => (
                    <li key={`${issue.code}:${issue.message}`} className="font-medium">
                      • {reasonLabel(issue.code)}
                    </li>
                  ))}
                </ul>
              ) : null}
              {overview.readiness.warnings.length > 0 ? (
                <ul className="mt-2 grid gap-1 text-xs opacity-80 sm:grid-cols-2">
                  {overview.readiness.warnings.map((issue) => (
                    <li key={`${issue.code}:${issue.message}`}>• {reasonLabel(issue.code)}</li>
                  ))}
                </ul>
              ) : null}
            </div>
          </div>

          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            {[
              ["codex_auths", overview.counts.codexAuths],
              ["bound_codex_auths", overview.counts.boundCodexAuths],
              ["unbound_codex_auths", overview.counts.unboundCodexAuths],
              ["bound_endpoint_not_ready", overview.counts.boundEndpointNotReady],
            ].map(([key, value]) => (
              <Card key={String(key)} padding="compact">
                <p className="text-xs font-medium text-slate-500">{t(`egress.metrics.${key}`)}</p>
                <p className="mt-2 text-2xl font-semibold text-slate-950 dark:text-white">
                  {value}
                </p>
              </Card>
            ))}
          </div>

          <Card
            title={t("egress.overview.inventory_title")}
            description={t("egress.overview.inventory_description")}
          >
            <dl className="grid gap-x-6 gap-y-4 text-sm sm:grid-cols-3">
              <div>
                <dt className="text-xs text-slate-500">{t("egress.metrics.endpoints")}</dt>
                <dd className="mt-1 font-semibold text-slate-950 dark:text-white">
                  {overview.counts.enabledEndpoints} / {overview.counts.endpoints}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-slate-500">{t("egress.metrics.bindings")}</dt>
                <dd className="mt-1 font-semibold text-slate-950 dark:text-white">
                  {overview.counts.bindings}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-slate-500">{t("egress.metrics.missing_account_id")}</dt>
                <dd className="mt-1 font-semibold text-slate-950 dark:text-white">
                  {overview.counts.missingAccountId}
                </dd>
              </div>
            </dl>
          </Card>
        </TabsContent>

        <TabsContent value="endpoints">
          <Card
            title={t("egress.endpoints.title")}
            description={t("egress.endpoints.description")}
            padding="none"
            actions={
              <Button
                size="sm"
                variant="primary"
                onClick={() => {
                  setEditingEndpoint(null);
                  setEndpointModalOpen(true);
                }}
              >
                <Plus size={14} />
                {t("egress.endpoints.add")}
              </Button>
            }
          >
            <div className="space-y-3 p-3 md:hidden">
              {endpoints.map((endpoint) => (
                <div
                  key={endpoint.id}
                  className="min-w-0 rounded-xl border border-slate-200 p-3 dark:border-neutral-800"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="truncate font-semibold text-slate-950 dark:text-white">
                        {endpoint.name}
                      </p>
                      <p className="mt-1 break-all font-mono text-xs text-slate-500">
                        {endpoint.protocol}://{endpoint.host}:{endpoint.port}
                      </p>
                    </div>
                    <StatusBadge
                      tone={
                        endpoint.runtimeReady
                          ? "green"
                          : endpoint.status === "unhealthy"
                            ? "rose"
                            : "slate"
                      }
                    >
                      {t(
                        endpoint.runtimeReady
                          ? "egress.endpoints.runtime_ready"
                          : `egress.endpoints.${endpoint.status}`,
                      )}
                    </StatusBadge>
                  </div>
                  <dl className="mt-3 grid grid-cols-2 gap-3 text-xs">
                    <div className="min-w-0">
                      <dt className="text-slate-500">{t("egress.endpoints.expected_short")}</dt>
                      <dd className="mt-1 break-all font-mono text-slate-900 dark:text-white">
                        {endpoint.expectedPublicIp || "--"}
                      </dd>
                    </div>
                    <div className="min-w-0">
                      <dt className="text-slate-500">{t("egress.endpoints.observed_short")}</dt>
                      <dd className="mt-1 break-all font-mono text-slate-900 dark:text-white">
                        {endpoint.publicIp || t("egress.endpoints.not_checked")}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-slate-500">{t("egress.endpoints.latency")}</dt>
                      <dd className="mt-1 text-slate-900 dark:text-white">
                        {typeof endpoint.latencyMs === "number" ? `${endpoint.latencyMs} ms` : "--"}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-slate-500">{t("egress.endpoints.credentials")}</dt>
                      <dd className="mt-1 text-slate-900 dark:text-white">
                        {endpoint.hasCredentials
                          ? t("egress.endpoints.credentials_configured")
                          : "--"}
                      </dd>
                    </div>
                  </dl>
                  {endpoint.error ? (
                    <p className="mt-3 break-words text-xs text-rose-600 dark:text-rose-300">
                      {endpoint.error}
                    </p>
                  ) : null}
                  <div className="mt-3 flex justify-end gap-1">
                    <Button
                      size="xs"
                      aria-label={t("egress.endpoints.check_label", { name: endpoint.name })}
                      onClick={() => void checkEndpoint(endpoint)}
                    >
                      <CheckCircle2 size={14} />
                    </Button>
                    <Button
                      size="xs"
                      aria-label={t("egress.endpoints.edit_label", { name: endpoint.name })}
                      onClick={() => {
                        setEditingEndpoint(endpoint);
                        setEndpointModalOpen(true);
                      }}
                    >
                      <Pencil size={14} />
                    </Button>
                    <Button
                      size="xs"
                      variant="ghost"
                      aria-label={t("egress.endpoints.delete_label", { name: endpoint.name })}
                      onClick={() => void requestEndpointAction(endpoint, "delete")}
                    >
                      <Trash2 size={14} />
                    </Button>
                  </div>
                </div>
              ))}
              {!loading && endpoints.length === 0 ? (
                <p className="py-6 text-center text-sm text-slate-500">
                  {t("egress.endpoints.empty")}
                </p>
              ) : null}
            </div>
            <div className="hidden md:block">
              <VirtualTable
                rows={endpoints}
                columns={endpointColumns}
                rowKey={(endpoint) => endpoint.id}
                loading={loading}
                virtualize={false}
                naturalFlow
                minWidth="min-w-[920px]"
                height="h-auto"
                minHeight="min-h-[180px]"
                caption={t("egress.endpoints.title")}
                emptyText={t("egress.endpoints.empty")}
                showAllLoadedMessage={false}
              />
            </div>
          </Card>
        </TabsContent>

        <TabsContent value="bindings">
          <Card
            title={t("egress.bindings.title")}
            description={t("egress.bindings.description")}
            padding="none"
          >
            <div className="flex flex-col gap-3 border-b border-slate-200 p-3 dark:border-neutral-800 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex min-w-0 flex-1 flex-col gap-2 sm:flex-row">
                <TextInput
                  type="search"
                  aria-label={t("egress.bindings.search_label")}
                  value={bindingSearch}
                  onChange={(event) => setBindingSearch(event.currentTarget.value)}
                  placeholder={t("egress.bindings.search_placeholder")}
                  startAdornment={<Search size={14} className="text-slate-400" />}
                  className="min-w-0 sm:max-w-sm"
                />
                <div
                  className="flex flex-wrap items-center gap-1"
                  aria-label={t("egress.bindings.filter_label")}
                >
                  <Filter size={14} className="mr-1 text-slate-400" />
                  {(["all", "unbound", "issues"] as const).map((filter) => (
                    <Button
                      key={filter}
                      size="xs"
                      variant={bindingFilter === filter ? "primary" : "secondary"}
                      onClick={() => setBindingFilter(filter)}
                    >
                      {t(`egress.bindings.filters.${filter}`)}
                    </Button>
                  ))}
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs font-semibold text-slate-600 dark:text-white/65">
                  {t("egress.bindings.pending", { count: bindingAssignments.length })}
                </span>
                <Button
                  size="sm"
                  variant="primary"
                  disabled={bindingAssignments.length === 0 || previewingBindings}
                  onClick={() => void previewBindingChanges()}
                >
                  {t("egress.bindings.preview", { count: bindingAssignments.length })}
                </Button>
              </div>
            </div>
            <div className="space-y-3 p-3 md:hidden">
              {filteredBindings.map((binding) => (
                <div
                  key={binding.identity || binding.authId}
                  className="min-w-0 rounded-xl border border-slate-200 p-3 dark:border-neutral-800"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                        <p className="min-w-0 truncate font-semibold text-slate-950 dark:text-white">
                          {binding.accountLabel}
                        </p>
                        <StatusBadge tone={binding.provider === "antigravity" ? "sky" : "slate"}>
                          {binding.provider === "antigravity" ? "Antigravity" : "Codex"}
                        </StatusBadge>
                        {binding.planType ? (
                          <StatusBadge tone="amber">{`${t("codex_quota.plan_label")}: ${binding.planType.toUpperCase()}`}</StatusBadge>
                        ) : null}
                      </div>
                      <p className="mt-1 truncate font-mono text-[11px] text-slate-500">
                        {binding.identity || binding.authId}
                      </p>
                    </div>
                    {(pendingAssignments[binding.identity] ?? binding.endpointId) ? (
                      <StatusBadge tone="green">{t("egress.bound")}</StatusBadge>
                    ) : (
                      <StatusBadge tone="amber">{t("egress.bindings.unbound")}</StatusBadge>
                    )}
                  </div>
                  {binding.error ? (
                    <p className="mt-2 text-xs text-rose-600 dark:text-rose-300">{binding.error}</p>
                  ) : null}
                  <div className="mt-3 flex min-w-0 items-center gap-2">
                    <EndpointSelect
                      value={pendingAssignments[binding.identity] ?? binding.endpointId}
                      endpoints={endpoints}
                      ariaLabel={t("egress.bindings.endpoint_for", {
                        account: binding.accountLabel,
                      })}
                      disabled={!binding.identity}
                      allowEmpty
                      onChange={(value) => stageBinding(binding, value)}
                    />
                    <Button
                      size="xs"
                      variant="ghost"
                      aria-label={t("egress.bindings.unbind_label", {
                        account: binding.accountLabel,
                      })}
                      disabled={
                        !binding.identity ||
                        !(pendingAssignments[binding.identity] ?? binding.endpointId)
                      }
                      onClick={() => stageBinding(binding, "")}
                    >
                      <Unplug size={14} />
                    </Button>
                  </div>
                </div>
              ))}
              {!loading && filteredBindings.length === 0 ? (
                <p className="py-6 text-center text-sm text-slate-500">
                  {t("egress.bindings.empty")}
                </p>
              ) : null}
            </div>
            <div className="hidden md:block">
              <VirtualTable
                rows={filteredBindings}
                columns={bindingColumns}
                rowKey={(binding) => binding.identity || binding.authId}
                loading={loading}
                virtualize={false}
                naturalFlow
                minWidth="min-w-[960px]"
                height="h-auto"
                minHeight="min-h-[180px]"
                caption={t("egress.bindings.title")}
                emptyText={t("egress.bindings.empty")}
                showAllLoadedMessage={false}
              />
            </div>
          </Card>
        </TabsContent>
      </Tabs>

      <EndpointModal
        open={endpointModalOpen}
        endpoint={editingEndpoint}
        saving={saving}
        onClose={() => {
          setEndpointModalOpen(false);
          setEditingEndpoint(null);
        }}
        onSave={(input) => void saveEndpoint(input)}
      />
      <ConfirmModal
        open={pendingEndpointAction !== null}
        title={t(
          pendingEndpointAction?.action === "disable"
            ? "egress.endpoints.disable_title"
            : "egress.endpoints.delete_title",
        )}
        description={t("egress.endpoints.impact_description", {
          name: pendingEndpointAction?.endpoint.name ?? "",
          count: pendingEndpointAction?.impact.affectedBindings ?? 0,
          reason: pendingEndpointAction?.impact.reason ?? "",
        })}
        confirmText={t(
          pendingEndpointAction?.impact.blocked
            ? "common.close"
            : pendingEndpointAction?.action === "disable"
              ? "common.disable"
              : "common.delete",
        )}
        busy={saving}
        onConfirm={() => {
          if (pendingEndpointAction?.impact.blocked) setPendingEndpointAction(null);
          else void confirmEndpointAction();
        }}
        onClose={() => setPendingEndpointAction(null)}
      />
      <ConfirmModal
        open={bindingPreview !== null}
        title={t("egress.bindings.apply_title")}
        description={t("egress.bindings.apply_description", {
          count: bindingPreview?.changeCount ?? bindingAssignments.length,
          accounts:
            bindingPreview?.affectedAccounts.join(", ") ||
            bindingAssignments.map((assignment) => assignment.identity).join(", "),
          issues: bindingPreview?.blockers.map((blocker) => blocker.message).join("; ") || "",
        })}
        confirmText={t(bindingPreview?.valid ? "egress.bindings.apply_confirm" : "common.close")}
        variant="primary"
        busy={saving}
        onConfirm={() => {
          if (bindingPreview?.valid) void applyBindingChanges();
          else setBindingPreview(null);
        }}
        onClose={() => setBindingPreview(null)}
      />
    </div>
  );
}
