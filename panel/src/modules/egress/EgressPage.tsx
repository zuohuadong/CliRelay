import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CheckCircle2,
  Clipboard,
  CloudOff,
  Filter,
  KeyRound,
  Link2,
  Network,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Server,
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
  type EgressEnrollment,
  type EgressNode,
  type EgressOverview,
} from "@/lib/http/apis/egress";
import { Button } from "@/modules/ui/Button";
import { Card } from "@/modules/ui/Card";
import { ConfirmModal } from "@/modules/ui/ConfirmModal";
import { TextInput } from "@/modules/ui/Input";
import { Modal } from "@/modules/ui/Modal";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/modules/ui/Tabs";
import { useToast } from "@/modules/ui/ToastProvider";
import { VirtualTable, type VirtualTableColumn } from "@/modules/ui/VirtualTable";
import { EndpointModal } from "@/modules/egress/EndpointModal";
import { EndpointSelect } from "@/modules/egress/EndpointSelect";

type EgressTab = "overview" | "nodes" | "endpoints" | "bindings";

const EMPTY_OVERVIEW: EgressOverview = {
  enabled: false,
  revision: "",
  policy: {
    bindingMode: "exclusive",
    nodeFreshnessTtlSeconds: 0,
    endpointCheckTtlSeconds: 0,
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
  headscale: {
    configured: false,
    reachable: false,
    url: "",
    apiKeyConfigured: false,
    serviceTag: "",
  },
  localEndpointEnabled: false,
  counts: {
    nodes: 0,
    onlineNodes: 0,
    endpoints: 0,
    enabledEndpoints: 0,
    bindings: 0,
    accounts: 0,
    routableAccounts: 0,
    unboundAccounts: 0,
    missingIdentityAccounts: 0,
  },
};

type BindingFilter = "all" | "unbound" | "issues";

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
  tone: "green" | "amber" | "rose" | "slate";
  children: string;
}) {
  const classes = {
    green: "bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300",
    amber: "bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-200",
    rose: "bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300",
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
  const [nodes, setNodes] = useState<EgressNode[]>([]);
  const [endpoints, setEndpoints] = useState<EgressEndpoint[]>([]);
  const [bindings, setBindings] = useState<EgressBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [checkingId, setCheckingId] = useState("");
  const [saving, setSaving] = useState(false);
  const [endpointModalOpen, setEndpointModalOpen] = useState(false);
  const [editingEndpoint, setEditingEndpoint] = useState<EgressEndpoint | null>(null);
  const [pendingEndpointAction, setPendingEndpointAction] = useState<PendingEndpointAction | null>(
    null,
  );
  const [bindingSearch, setBindingSearch] = useState("");
  const [bindingFilter, setBindingFilter] = useState<BindingFilter>("all");
  const [selectedIdentities, setSelectedIdentities] = useState<string[]>([]);
  const [pendingAssignments, setPendingAssignments] = useState<Record<string, string>>({});
  const [bindingPreview, setBindingPreview] = useState<EgressBindingPreview | null>(null);
  const [previewingBindings, setPreviewingBindings] = useState(false);
  const [enrollmentOpen, setEnrollmentOpen] = useState(false);
  const [enrollmentName, setEnrollmentName] = useState("");
  const [enrollment, setEnrollment] = useState<EgressEnrollment | null>(null);
  const [enrollmentLoading, setEnrollmentLoading] = useState(false);

  const reasonLabel = useCallback(
    (code: string) =>
      t(`egress.reasons.${code}`, {
        defaultValue: t("egress.reasons.unknown"),
      }),
    [t],
  );

  const loadAll = useCallback(async () => {
    setLoading(true);
    try {
      const [nextOverview, nextNodes, nextEndpoints, nextBindings] = await Promise.all([
        egressApi.getOverview(),
        egressApi.listNodes(),
        egressApi.listEndpoints(),
        egressApi.listBindings(),
      ]);
      setOverview(nextOverview);
      setNodes(nextNodes);
      setEndpoints(nextEndpoints);
      setBindings(nextBindings);
    } catch (error) {
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.load_failed"),
      });
    } finally {
      setLoading(false);
    }
  }, [notify, t]);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  const refreshLists = useCallback(async () => {
    const [nextOverview, nextNodes, nextEndpoints, nextBindings] = await Promise.all([
      egressApi.getOverview(),
      egressApi.listNodes(),
      egressApi.listEndpoints(),
      egressApi.listBindings(),
    ]);
    setOverview(nextOverview);
    setNodes(nextNodes);
    setEndpoints(nextEndpoints);
    setBindings(nextBindings);
  }, []);

  const syncNodes = useCallback(async () => {
    setSyncing(true);
    try {
      await egressApi.syncNodes();
      await refreshLists();
      notify({ type: "success", message: t("egress.nodes.synced") });
    } catch (error) {
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.nodes.sync_failed"),
      });
    } finally {
      setSyncing(false);
    }
  }, [notify, refreshLists, t]);

  const saveEndpoint = useCallback(
    async (input: EgressEndpointInput) => {
      setSaving(true);
      try {
        if (editingEndpoint?.enabled && !input.enabled) {
          const impact = await egressApi.endpointImpact(editingEndpoint.id, "disable");
          setPendingEndpointAction({
            endpoint: editingEndpoint,
            action: "disable",
            impact,
            input,
          });
          return;
        }
        if (editingEndpoint) {
          await egressApi.updateEndpoint(editingEndpoint.id, input);
        } else {
          await egressApi.createEndpoint(input);
        }
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
      try {
        await egressApi.checkEndpoint(endpoint.id);
        await refreshLists();
        notify({ type: "success", message: t("egress.endpoints.checked") });
      } catch (error) {
        notify({
          type: "error",
          message: error instanceof Error ? error.message : t("egress.endpoints.check_failed"),
        });
      } finally {
        setCheckingId("");
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

  const generateEnrollment = useCallback(async () => {
    setEnrollmentLoading(true);
    try {
      setEnrollment(await egressApi.createEnrollment({ name: enrollmentName.trim() }));
    } catch (error) {
      notify({
        type: "error",
        message: error instanceof Error ? error.message : t("egress.nodes.enrollment_failed"),
      });
    } finally {
      setEnrollmentLoading(false);
    }
  }, [enrollmentName, notify, t]);

  const copyText = useCallback(
    async (value: string) => {
      await navigator.clipboard?.writeText(value);
      notify({ type: "success", message: t("common.copied") });
    },
    [notify, t],
  );

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
        [binding.accountLabel, binding.authId, binding.identity]
          .join(" ")
          .toLowerCase()
          .includes(search);
      const matchesFilter =
        bindingFilter === "all" ||
        (bindingFilter === "unbound" && !binding.bound) ||
        (bindingFilter === "issues" &&
          Boolean(
            binding.error ||
            !binding.identity ||
            (binding.bound &&
              !endpoints.find((endpoint) => endpoint.id === binding.endpointId)?.eligibility
                ?.selectable),
          ));
      return matchesSearch && matchesFilter;
    });
  }, [bindingFilter, bindingSearch, bindings, endpoints]);

  const autoAssignUniqueEndpoints = useCallback(() => {
    const targetIdentities =
      selectedIdentities.length > 0
        ? selectedIdentities
        : filteredBindings.filter((binding) => binding.identity).map((binding) => binding.identity);
    const targets = targetIdentities
      .map((identity) => bindings.find((binding) => binding.identity === identity))
      .filter((binding): binding is EgressBinding => Boolean(binding?.identity));
    const targetSet = new Set(targetIdentities);
    const occupiedEndpointIDs = new Set(
      bindings
        .filter((binding) => binding.bound && !targetSet.has(binding.identity))
        .map((binding) => binding.endpointId),
    );
    const seenPublicIPs = new Set<string>();
    const available = endpoints.filter((endpoint) => {
      const publicIP = endpoint.publicIp || endpoint.expectedPublicIp || endpoint.id;
      if (!endpoint.eligibility?.selectable || occupiedEndpointIDs.has(endpoint.id)) return false;
      if (seenPublicIPs.has(publicIP)) return false;
      seenPublicIPs.add(publicIP);
      return true;
    });
    setPendingAssignments((current) => {
      const next = { ...current };
      targets.forEach((binding, index) => {
        const endpoint = available[index];
        if (endpoint && endpoint.id !== binding.endpointId) next[binding.identity] = endpoint.id;
      });
      return next;
    });
  }, [bindings, endpoints, filteredBindings, selectedIdentities]);

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
      setSelectedIdentities([]);
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

  const nodeColumns = useMemo<VirtualTableColumn<EgressNode>[]>(
    () => [
      {
        key: "name",
        label: t("egress.nodes.name"),
        width: "w-56",
        render: (node) => (
          <div>
            <p className="font-semibold text-slate-950 dark:text-white">{node.name}</p>
            <p className="font-mono text-[11px] text-slate-500">{node.id}</p>
          </div>
        ),
      },
      {
        key: "ips",
        label: t("egress.nodes.addresses"),
        width: "w-36",
        render: (node) => (
          <span className="font-mono text-xs">{node.ipAddresses.join(", ") || "--"}</span>
        ),
      },
      {
        key: "status",
        label: t("egress.nodes.status"),
        width: "w-28",
        render: (node) => {
          const healthy = node.online && node.fresh;
          return (
            <StatusBadge tone={healthy ? "green" : node.online ? "amber" : "slate"}>
              {t(
                healthy
                  ? "egress.nodes.online"
                  : node.online
                    ? "egress.nodes.stale"
                    : "egress.nodes.offline",
              )}
            </StatusBadge>
          );
        },
      },
      {
        key: "tags",
        label: t("egress.nodes.tags"),
        width: "w-56",
        render: (node) => (
          <span className="font-mono text-[11px]">{node.tags.join(", ") || "--"}</span>
        ),
      },
      {
        key: "lastSeen",
        label: t("egress.nodes.last_seen"),
        width: "w-48",
        render: (node) => (
          <div className="space-y-1 text-xs">
            <p>{formatDateTime(node.lastSeen)}</p>
            <p className="text-[11px] text-slate-500">
              {t("egress.nodes.synced_at")}: {formatDateTime(node.syncedAt)} · {node.syncAgeSeconds}
              s
            </p>
          </div>
        ),
      },
    ],
    [t],
  );

  const endpointColumns = useMemo<VirtualTableColumn<EgressEndpoint>[]>(
    () => [
      {
        key: "name",
        label: t("egress.endpoints.name"),
        width: "w-40",
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
        width: "w-48",
        render: (endpoint) => (
          <div>
            <p className="font-mono text-xs">
              {endpoint.protocol}://{endpoint.host}:{endpoint.port}
            </p>
            <p className="mt-1 text-[11px] text-slate-500">
              {endpoint.isLocal ? t("egress.endpoints.origin_server") : endpoint.nodeId}
            </p>
          </div>
        ),
      },
      {
        key: "credentials",
        label: t("egress.endpoints.credentials"),
        width: "w-40",
        render: (endpoint) =>
          endpoint.hasCredentials ? (
            <StatusBadge tone="slate">{t("egress.endpoints.credentials_configured")}</StatusBadge>
          ) : (
            <span className="text-xs text-slate-500">--</span>
          ),
      },
      {
        key: "status",
        label: t("egress.endpoints.status"),
        width: "w-28",
        render: (endpoint) => (
          <div className="space-y-1">
            <StatusBadge
              tone={
                endpoint.status === "healthy"
                  ? "green"
                  : endpoint.status === "unhealthy"
                    ? "rose"
                    : "slate"
              }
            >
              {t(`egress.endpoints.${endpoint.status}`)}
            </StatusBadge>
            {typeof endpoint.latencyMs === "number" ? (
              <p className="text-[11px] text-slate-500">{endpoint.latencyMs} ms</p>
            ) : null}
            {endpoint.error ? (
              <p className="max-w-48 text-[11px] text-rose-600 dark:text-rose-300">
                {endpoint.error}
              </p>
            ) : null}
            {endpoint.eligibility?.reasonCodes.length ? (
              <ul className="max-w-48 space-y-0.5 text-[11px] text-rose-600 dark:text-rose-300">
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
        width: "w-40",
        render: (endpoint) => (
          <div className="space-y-1 font-mono text-[11px]">
            <p>
              <span className="font-sans text-slate-500">
                {t("egress.endpoints.expected_public_ip")}:
              </span>{" "}
              {endpoint.expectedPublicIp || "--"}
            </p>
            <p>
              <span className="font-sans text-slate-500">{t("egress.endpoints.public_ip")}:</span>{" "}
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
              aria-label={t("egress.endpoints.check_label", {
                name: endpoint.name,
              })}
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
              aria-label={t("egress.endpoints.edit_label", {
                name: endpoint.name,
              })}
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
              aria-label={t("egress.endpoints.delete_label", {
                name: endpoint.name,
              })}
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
        key: "select",
        label: "",
        width: "w-12",
        render: (binding) => (
          <input
            type="checkbox"
            aria-label={t("egress.bindings.select_account", {
              account: binding.accountLabel,
            })}
            checked={selectedIdentities.includes(binding.identity)}
            disabled={!binding.identity}
            onChange={(event) =>
              setSelectedIdentities((current) =>
                event.currentTarget.checked
                  ? [...new Set([...current, binding.identity])]
                  : current.filter((identity) => identity !== binding.identity),
              )
            }
            className="h-4 w-4 rounded border-slate-300 accent-slate-900 dark:border-neutral-700 dark:accent-white"
          />
        ),
      },
      {
        key: "account",
        label: t("egress.bindings.account"),
        width: "w-64",
        render: (binding) => (
          <div>
            <p className="font-semibold text-slate-950 dark:text-white">{binding.accountLabel}</p>
            <p className="font-mono text-[11px] text-slate-500">{binding.authId}</p>
          </div>
        ),
      },
      {
        key: "endpoint",
        label: t("egress.bindings.endpoint"),
        width: "w-64",
        render: (binding) => (
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
        width: "w-24",
        headerClassName: "text-right",
        cellClassName: "text-right",
        render: (binding) => (
          <Button
            size="xs"
            variant="ghost"
            aria-label={t("egress.bindings.remove_label", {
              account: binding.accountLabel,
            })}
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
    [endpoints, pendingAssignments, selectedIdentities, stageBinding, t],
  );

  const headscale = overview.headscale;
  const headscaleLabel = !headscale.configured
    ? t("egress.headscale.not_configured")
    : headscale.reachable
      ? t("egress.headscale.healthy")
      : t("egress.headscale.unreachable");
  const headscaleTone = !headscale.configured ? "slate" : headscale.reachable ? "green" : "rose";
  const runtimeLabel = t(
    overview.enabled ? "egress.runtime.enabled" : "egress.runtime.preparation",
  );
  const runtimeDescription = t(
    overview.enabled
      ? "egress.runtime.enabled_description"
      : "egress.runtime.preparation_description",
  );
  const readinessTitle = t(
    overview.enabled
      ? overview.readiness.verdict === "ready"
        ? "egress.readiness.enabled_ready"
        : "egress.readiness.enabled_blocked"
      : overview.readiness.readyToEnable
        ? "egress.readiness.disabled_ready"
        : "egress.readiness.disabled_blocked",
  );
  const readinessTone = overview.readiness.verdict === "ready" ? "green" : "rose";
  const readinessScopeLabel = t(`egress.readiness.scopes.${overview.readiness.scope}`, {
    defaultValue: t("egress.readiness.scopes.unknown"),
  });
  const bindingPolicyLabel = t(`egress.readiness.policies.${overview.policy.bindingMode}`, {
    defaultValue: t("egress.readiness.policies.unknown"),
  });

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
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
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Tabs value={tab} onValueChange={(value) => setTab(value as EgressTab)} size="sm">
            <TabsList>
              <TabsTrigger value="overview">
                <ShieldCheck size={14} />
                {t("egress.tabs.overview")}
              </TabsTrigger>
              <TabsTrigger value="nodes">
                <Server size={14} />
                {t("egress.tabs.nodes")}
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
          <div className="flex flex-wrap items-center justify-end gap-2 text-xs">
            <StatusBadge tone={overview.enabled ? "green" : "amber"}>{runtimeLabel}</StatusBadge>
            <StatusBadge tone={headscaleTone}>{headscaleLabel}</StatusBadge>
            <span className="hidden text-slate-500 sm:inline">{headscale.serviceTag || "--"}</span>
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
                : overview.enabled
                  ? "border-emerald-200 bg-emerald-50 text-emerald-950 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-100"
                  : "border-amber-200 bg-amber-50 text-amber-950 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-100"
            }`}
          >
            <ShieldCheck size={20} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="text-sm font-semibold">{readinessTitle}</p>
                <div className="flex flex-wrap items-center gap-2">
                  <StatusBadge tone={readinessTone}>
                    {t(`egress.readiness.${overview.readiness.verdict}`)}
                  </StatusBadge>
                  <StatusBadge tone={overview.readiness.codexOAuthAllowed ? "green" : "amber"}>
                    {t(
                      overview.readiness.codexOAuthAllowed
                        ? "egress.readiness.oauth_allowed"
                        : "egress.readiness.oauth_blocked",
                    )}
                  </StatusBadge>
                </div>
              </div>
              <p className="mt-1 text-xs leading-5 opacity-80 sm:text-sm">{runtimeDescription}</p>
              <p className="mt-1 font-mono text-[11px] opacity-65">
                {t("egress.readiness.scope", {
                  scope: readinessScopeLabel,
                })}{" "}
                ·{" "}
                {t("egress.readiness.policy", {
                  mode: bindingPolicyLabel,
                })}
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
              {overview.readiness.notEvaluated.length > 0 ? (
                <p className="mt-2 text-xs opacity-70">
                  {t("egress.readiness.not_evaluated", {
                    count: overview.readiness.notEvaluated.length,
                  })}
                </p>
              ) : null}
            </div>
          </div>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <Card padding="compact">
              <p className="text-xs font-medium text-slate-500">{t("egress.metrics.accounts")}</p>
              <p className="mt-2 text-2xl font-semibold text-slate-950 dark:text-white">
                {overview.counts.accounts}
              </p>
              <p className="mt-1 text-xs text-slate-500">{t("egress.metrics.account_total")}</p>
            </Card>
            <Card padding="compact">
              <p className="text-xs font-medium text-slate-500">
                {t("egress.metrics.routable_accounts")}
              </p>
              <p className="mt-2 text-2xl font-semibold text-slate-950 dark:text-white">
                {overview.counts.routableAccounts}
              </p>
              <p className="mt-1 text-xs text-slate-500">{t("egress.metrics.ready_now")}</p>
            </Card>
            <Card padding="compact">
              <p className="text-xs font-medium text-slate-500">
                {t("egress.metrics.unbound_accounts")}
              </p>
              <p className="mt-2 text-2xl font-semibold text-slate-950 dark:text-white">
                {overview.counts.unboundAccounts}
              </p>
              <p className="mt-1 text-xs text-slate-500">{t("egress.metrics.needs_binding")}</p>
            </Card>
            <Card padding="compact">
              <p className="text-xs font-medium text-slate-500">
                {t("egress.metrics.missing_identity_accounts")}
              </p>
              <p className="mt-2 text-2xl font-semibold text-slate-950 dark:text-white">
                {overview.counts.missingIdentityAccounts}
              </p>
              <p className="mt-1 text-xs text-slate-500">{t("egress.metrics.needs_reauth")}</p>
            </Card>
          </div>
          <Card title={t("egress.headscale.title")} description={t("egress.headscale.description")}>
            <dl className="grid gap-x-6 gap-y-4 text-sm sm:grid-cols-2 xl:grid-cols-4">
              <div>
                <dt className="text-xs text-slate-500">{t("egress.headscale.status")}</dt>
                <dd className="mt-1">
                  <StatusBadge tone={headscaleTone}>{headscaleLabel}</StatusBadge>
                </dd>
              </div>
              <div>
                <dt className="text-xs text-slate-500">{t("egress.headscale.url")}</dt>
                <dd className="mt-1 truncate font-mono text-xs text-slate-900 dark:text-white">
                  {headscale.url || "--"}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-slate-500">{t("egress.headscale.api_key")}</dt>
                <dd className="mt-1 text-xs text-slate-900 dark:text-white">
                  {t(
                    headscale.apiKeyConfigured
                      ? "egress.headscale.api_key_configured"
                      : "egress.headscale.api_key_missing",
                  )}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-slate-500">{t("egress.headscale.last_sync")}</dt>
                <dd className="mt-1 text-xs text-slate-900 dark:text-white">
                  {formatDateTime(headscale.lastSyncAt)}
                </dd>
              </div>
            </dl>
            {headscale.error ? (
              <div className="mt-4 flex items-start gap-2 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-500/10 dark:text-rose-300">
                <CloudOff size={16} className="mt-0.5 shrink-0" />
                <span>{headscale.error}</span>
              </div>
            ) : null}
            <div className="mt-4 flex flex-wrap gap-x-6 gap-y-2 border-t border-slate-200 pt-3 text-xs text-slate-500 dark:border-neutral-800">
              <span>
                {t("egress.metrics.nodes")}: {overview.counts.onlineNodes} / {overview.counts.nodes}
              </span>
              <span>
                {t("egress.metrics.endpoints")}: {overview.counts.enabledEndpoints} /{" "}
                {overview.counts.endpoints}
              </span>
              <span>
                {t("egress.metrics.bindings")}: {overview.counts.bindings}
              </span>
            </div>
          </Card>
        </TabsContent>

        <TabsContent value="nodes">
          <Card
            title={t("egress.nodes.title")}
            description={t("egress.nodes.description")}
            padding="none"
            actions={
              <div className="flex gap-2">
                <Button size="sm" onClick={() => void syncNodes()} disabled={syncing}>
                  <RefreshCw size={14} className={syncing ? "animate-spin" : ""} />
                  {t("egress.nodes.sync")}
                </Button>
                <Button
                  size="sm"
                  variant="primary"
                  onClick={() => {
                    setEnrollment(null);
                    setEnrollmentName("");
                    setEnrollmentOpen(true);
                  }}
                >
                  <KeyRound size={14} />
                  {t("egress.nodes.enroll")}
                </Button>
              </div>
            }
          >
            <div className="space-y-3 p-3 md:hidden">
              {nodes.map((node) => (
                <div
                  key={node.id}
                  className="rounded-xl border border-slate-200 p-3 dark:border-neutral-800"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <p className="font-semibold text-slate-950 dark:text-white">{node.name}</p>
                      <p className="mt-1 font-mono text-xs text-slate-500">
                        {node.ipAddresses.join(", ") || "--"}
                      </p>
                    </div>
                    <StatusBadge
                      tone={node.online && node.fresh ? "green" : node.online ? "amber" : "slate"}
                    >
                      {t(
                        node.online && node.fresh
                          ? "egress.nodes.online"
                          : node.online
                            ? "egress.nodes.stale"
                            : "egress.nodes.offline",
                      )}
                    </StatusBadge>
                  </div>
                  <p className="mt-3 truncate font-mono text-[11px] text-slate-500">
                    {node.tags.join(", ") || "--"}
                  </p>
                  <p className="mt-1 text-[11px] text-slate-500">
                    {t("egress.nodes.synced_at")}: {formatDateTime(node.syncedAt)} ·{" "}
                    {node.syncAgeSeconds}s
                  </p>
                </div>
              ))}
              {!loading && nodes.length === 0 ? (
                <p className="py-6 text-center text-sm text-slate-500">{t("egress.nodes.empty")}</p>
              ) : null}
            </div>
            <div className="hidden md:block">
              <VirtualTable
                rows={nodes}
                columns={nodeColumns}
                rowKey={(node) => node.id}
                loading={loading}
                virtualize={false}
                naturalFlow
                minWidth="min-w-[840px]"
                height="h-auto"
                minHeight="min-h-[180px]"
                caption={t("egress.nodes.title")}
                emptyText={t("egress.nodes.empty")}
                showAllLoadedMessage={false}
              />
            </div>
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
                  className="rounded-xl border border-slate-200 p-3 dark:border-neutral-800"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <p className="font-semibold text-slate-950 dark:text-white">
                        {endpoint.name}
                      </p>
                      <p className="mt-1 break-all font-mono text-xs text-slate-500">
                        {endpoint.protocol}://{endpoint.host}:{endpoint.port}
                      </p>
                    </div>
                    <StatusBadge
                      tone={
                        endpoint.status === "healthy"
                          ? "green"
                          : endpoint.status === "unhealthy"
                            ? "rose"
                            : "slate"
                      }
                    >
                      {t(`egress.endpoints.${endpoint.status}`)}
                    </StatusBadge>
                  </div>
                  <dl className="mt-3 grid grid-cols-2 gap-3 text-xs">
                    <div>
                      <dt className="text-slate-500">{t("egress.endpoints.expected_public_ip")}</dt>
                      <dd className="mt-1 font-mono text-slate-900 dark:text-white">
                        {endpoint.expectedPublicIp || "--"}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-slate-500">{t("egress.endpoints.public_ip")}</dt>
                      <dd className="mt-1 font-mono text-slate-900 dark:text-white">
                        {endpoint.publicIp || t("egress.endpoints.not_checked")}
                      </dd>
                    </div>
                    <div>
                      <dt className="text-slate-500">{t("egress.endpoints.availability")}</dt>
                      <dd className="mt-1">
                        <StatusBadge tone={endpoint.enabled ? "green" : "amber"}>
                          {t(
                            endpoint.enabled
                              ? "egress.endpoints.enabled_short"
                              : "egress.endpoints.disabled_short",
                          )}
                        </StatusBadge>
                      </dd>
                    </div>
                  </dl>
                  {endpoint.error ? (
                    <p className="mt-3 text-xs text-rose-600 dark:text-rose-300">
                      {endpoint.error}
                    </p>
                  ) : null}
                  {endpoint.eligibility?.reasonCodes.length ? (
                    <ul className="mt-1 space-y-0.5 text-xs text-rose-600 dark:text-rose-300">
                      {endpoint.eligibility.reasonCodes.map((code) => (
                        <li key={code}>{reasonLabel(code)}</li>
                      ))}
                    </ul>
                  ) : null}
                  <div className="mt-3 flex justify-end gap-1">
                    <Button
                      size="xs"
                      aria-label={t("egress.endpoints.check_label", {
                        name: endpoint.name,
                      })}
                      onClick={() => void checkEndpoint(endpoint)}
                    >
                      <CheckCircle2 size={14} />
                    </Button>
                    <Button
                      size="xs"
                      aria-label={t("egress.endpoints.edit_label", {
                        name: endpoint.name,
                      })}
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
                      aria-label={t("egress.endpoints.delete_label", {
                        name: endpoint.name,
                      })}
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
                minWidth="min-w-[976px]"
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
                  className="sm:max-w-sm"
                />
                <div
                  className="flex items-center gap-1"
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
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={filteredBindings.every((binding) => !binding.identity)}
                  onClick={autoAssignUniqueEndpoints}
                >
                  {t("egress.bindings.auto_assign")}
                </Button>
                <span className="text-xs font-semibold text-slate-600 dark:text-white/65">
                  {t("egress.bindings.pending", {
                    count: bindingAssignments.length,
                  })}
                </span>
                <Button
                  size="sm"
                  variant="primary"
                  disabled={bindingAssignments.length === 0 || previewingBindings}
                  onClick={() => void previewBindingChanges()}
                >
                  {t("egress.bindings.preview", {
                    count: bindingAssignments.length,
                  })}
                </Button>
              </div>
            </div>
            <div className="space-y-3 p-3 md:hidden">
              {filteredBindings.map((binding) => (
                <div
                  key={binding.identity || binding.authId}
                  className="rounded-xl border border-slate-200 p-3 dark:border-neutral-800"
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex min-w-0 items-start gap-2">
                      <input
                        type="checkbox"
                        aria-label={t("egress.bindings.select_account", {
                          account: binding.accountLabel,
                        })}
                        checked={selectedIdentities.includes(binding.identity)}
                        disabled={!binding.identity}
                        onChange={(event) =>
                          setSelectedIdentities((current) =>
                            event.currentTarget.checked
                              ? [...new Set([...current, binding.identity])]
                              : current.filter((identity) => identity !== binding.identity),
                          )
                        }
                        className="mt-1 h-4 w-4 rounded border-slate-300 accent-slate-900 dark:border-neutral-700 dark:accent-white"
                      />
                      <div className="min-w-0">
                        <p className="truncate font-semibold text-slate-950 dark:text-white">
                          {binding.accountLabel}
                        </p>
                        <p className="mt-1 truncate font-mono text-[11px] text-slate-500">
                          {binding.identity || binding.authId}
                        </p>
                      </div>
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
                  <div className="mt-3 flex items-center gap-2">
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
                      aria-label={t("egress.bindings.remove_label", {
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
                minWidth="min-w-[1040px]"
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
        nodes={nodes}
        saving={saving}
        localEndpointEnabled={overview.localEndpointEnabled}
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
      <Modal
        open={enrollmentOpen}
        title={t("egress.nodes.enrollment_title")}
        description={t("egress.nodes.enrollment_description")}
        maxWidth="max-w-2xl"
        onClose={() => setEnrollmentOpen(false)}
        footer={
          !enrollment ? (
            <>
              <Button variant="secondary" onClick={() => setEnrollmentOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                variant="primary"
                onClick={() => void generateEnrollment()}
                disabled={enrollmentLoading}
              >
                <KeyRound size={15} />
                {t("egress.nodes.generate")}
              </Button>
            </>
          ) : (
            <Button onClick={() => setEnrollmentOpen(false)}>{t("common.close")}</Button>
          )
        }
      >
        {!enrollment ? (
          <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
            <span>{t("egress.nodes.node_name")}</span>
            <TextInput
              aria-label={t("egress.nodes.node_name")}
              value={enrollmentName}
              onChange={(event) => setEnrollmentName(event.target.value)}
              placeholder="egress-sg"
            />
          </label>
        ) : (
          <div className="space-y-4">
            <div className="flex items-center justify-between gap-3">
              <div>
                <StatusBadge tone="amber">{t("egress.nodes.shown_once")}</StatusBadge>
                <p className="mt-2 text-xs text-slate-500">
                  {t("egress.nodes.expires", {
                    time: formatDateTime(enrollment.expiresAt),
                  })}
                </p>
              </div>
              <Button size="sm" onClick={() => void copyText(enrollment.key)}>
                <Clipboard size={14} />
                {t("common.copy")}
              </Button>
            </div>
            <pre className="overflow-x-auto rounded-xl bg-slate-950 p-3 font-mono text-xs text-white">
              {enrollment.key}
            </pre>
            <div className="flex items-center justify-between gap-3">
              <p className="text-xs font-semibold text-slate-700 dark:text-white/75">
                {t("egress.nodes.command")}
              </p>
              <Button size="sm" onClick={() => void copyText(enrollment.command)}>
                <Clipboard size={14} />
                {t("common.copy")}
              </Button>
            </div>
            <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-xl bg-slate-100 p-3 font-mono text-xs text-slate-900 dark:bg-neutral-900 dark:text-white">
              {enrollment.command}
            </pre>
            <p className="text-xs text-slate-500">{t("egress.nodes.no_remote_install")}</p>
          </div>
        )}
      </Modal>
    </div>
  );
}
