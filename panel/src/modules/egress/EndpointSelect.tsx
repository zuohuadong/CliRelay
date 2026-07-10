import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Clock3, Globe2 } from "lucide-react";
import type { EgressEndpoint } from "@/lib/http/apis/egress";
import { Select, type SelectOption } from "@/modules/ui/Select";

export function EndpointSelect({
  value,
  endpoints,
  onChange,
  ariaLabel,
  disabled = false,
  allowEmpty = false,
}: {
  value: string;
  endpoints: EgressEndpoint[];
  onChange: (value: string) => void;
  ariaLabel: string;
  disabled?: boolean;
  allowEmpty?: boolean;
}) {
  const { t } = useTranslation();
  const options = useMemo<SelectOption[]>(() => {
    const selectable = endpoints.filter((endpoint) => endpoint.runtimeReady || endpoint.id === value);
    const items: SelectOption[] = selectable.map((endpoint) => ({
      value: endpoint.id,
      label: (
        <span className="flex min-w-0 items-start gap-2">
          <Globe2 size={14} className="mt-0.5 shrink-0 text-slate-400" />
          <span className="min-w-0 flex-1">
            <span className="block truncate font-medium">{endpoint.name}</span>
            <span className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 font-mono text-[11px] text-slate-500 dark:text-white/50">
              <span>{endpoint.publicIp || endpoint.expectedPublicIp || t("egress.endpoints.not_checked")}</span>
              <span>{typeof endpoint.latencyMs === "number" ? `${endpoint.latencyMs} ms` : "--"}</span>
              <span>{t(`egress.endpoints.${endpoint.status}`)}</span>
              <span className="inline-flex items-center gap-1">
                <Clock3 size={11} />
                {endpoint.lastCheckedAt ? new Date(endpoint.lastCheckedAt).toLocaleString() : "--"}
              </span>
            </span>
          </span>
          {!endpoint.runtimeReady ? (
            <span className="shrink-0 text-[10px] font-semibold text-amber-600 dark:text-amber-300">
              {t("egress.endpoints.current_unavailable")}
            </span>
          ) : null}
        </span>
      ),
      triggerLabel: endpoint.name,
    }));
    if (allowEmpty) items.unshift({ value: "", label: t("egress.bindings.unbound") });
    return items;
  }, [allowEmpty, endpoints, t, value]);

  return (
    <Select
      value={value}
      onChange={onChange}
      options={options}
      aria-label={ariaLabel}
      placeholder={t("egress.select_endpoint")}
      disabled={disabled || options.length === 0}
      className="w-full min-w-0 sm:min-w-48"
    />
  );
}
