import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  EgressEndpoint,
  EgressEndpointInput,
  EgressProtocol,
} from "@/lib/http/apis/egress";
import { Button } from "@/modules/ui/Button";
import { TextInput } from "@/modules/ui/Input";
import { Modal } from "@/modules/ui/Modal";
import { Select } from "@/modules/ui/Select";
import { ToggleSwitch } from "@/modules/ui/ToggleSwitch";

interface EndpointDraft {
  name: string;
  protocol: EgressProtocol;
  host: string;
  port: string;
  enabled: boolean;
  expectedPublicIp: string;
  username: string;
  password: string;
  clearCredentials: boolean;
}

const isValidIPv4 = (value: string): boolean => {
  const octets = value.split(".");
  return (
    octets.length === 4 &&
    octets.every(
      (octet) =>
        /^(0|[1-9]\d{0,2})$/.test(octet) && Number(octet) >= 0 && Number(octet) <= 255,
    )
  );
};

const isValidIPv6 = (value: string): boolean => {
  let address = value;
  const hasOpeningBracket = address.startsWith("[");
  const hasClosingBracket = address.endsWith("]");
  if (hasOpeningBracket !== hasClosingBracket) return false;
  if (hasOpeningBracket) address = address.slice(1, -1);
  if (!address.includes(":")) return false;

  const compressedParts = address.split("::");
  if (compressedParts.length > 2) return false;
  const hasCompression = compressedParts.length === 2;
  const segments = hasCompression
    ? [
        ...(compressedParts[0] ? compressedParts[0].split(":") : []),
        ...(compressedParts[1] ? compressedParts[1].split(":") : []),
      ]
    : address.split(":");
  if (segments.some((segment) => !segment)) return false;

  let units = 0;
  for (const [index, segment] of segments.entries()) {
    if (segment.includes(".")) {
      if (index !== segments.length - 1 || !isValidIPv4(segment)) return false;
      units += 2;
      continue;
    }
    if (!/^[0-9a-f]{1,4}$/i.test(segment)) return false;
    units += 1;
  }
  return hasCompression ? units < 8 : units === 8;
};

const isValidIPAddress = (value: string): boolean => isValidIPv4(value) || isValidIPv6(value);

const emptyDraft = (): EndpointDraft => ({
  name: "",
  protocol: "socks5",
  host: "",
  port: "1080",
  enabled: true,
  expectedPublicIp: "",
  username: "",
  password: "",
  clearCredentials: false,
});

const endpointDraft = (endpoint: EgressEndpoint): EndpointDraft => ({
  name: endpoint.name,
  protocol: endpoint.protocol,
  host: endpoint.host,
  port: String(endpoint.port),
  enabled: endpoint.enabled,
  expectedPublicIp: endpoint.expectedPublicIp ?? "",
  username: endpoint.username ?? "",
  password: "",
  clearCredentials: false,
});

export function EndpointModal({
  open,
  endpoint,
  saving,
  onClose,
  onSave,
}: {
  open: boolean;
  endpoint: EgressEndpoint | null;
  saving: boolean;
  onClose: () => void;
  onSave: (input: EgressEndpointInput) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<EndpointDraft>(() => emptyDraft());
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setDraft(endpoint ? endpointDraft(endpoint) : emptyDraft());
    setError("");
  }, [endpoint, open]);

  const save = () => {
    const port = Number(draft.port);
    if (!draft.name.trim() || !draft.host.trim() || port < 1 || port > 65535) {
      setError(t("egress.endpoints.validation_required"));
      return;
    }
    const expectedPublicIp = draft.expectedPublicIp.trim();
    if (draft.enabled && !expectedPublicIp) {
      setError(t("egress.endpoints.validation_expected_public_ip_required"));
      return;
    }
    if (expectedPublicIp && !isValidIPAddress(expectedPublicIp)) {
      setError(t("egress.endpoints.validation_expected_public_ip_invalid"));
      return;
    }
    onSave({
      name: draft.name.trim(),
      protocol: draft.protocol,
      host: draft.host.trim(),
      port,
      enabled: draft.enabled,
      expectedPublicIp,
      ...(draft.clearCredentials
        ? { clearCredentials: true }
        : {
            ...(draft.username.trim() ? { username: draft.username.trim() } : {}),
            ...(draft.password ? { password: draft.password } : {}),
          }),
    });
  };

  const isEdit = endpoint !== null;
  return (
    <Modal
      open={open}
      title={t(isEdit ? "egress.endpoints.edit_title" : "egress.endpoints.add_title")}
      description={t("egress.endpoints.modal_description")}
      maxWidth="max-w-2xl"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" onClick={save} disabled={saving}>
            {t("egress.endpoints.save")}
          </Button>
        </>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
          <span>{t("egress.endpoints.name")}</span>
          <TextInput
            aria-label={t("egress.endpoints.name")}
            value={draft.name}
            onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
          />
        </label>
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
          <span>{t("egress.endpoints.protocol")}</span>
          <Select
            value={draft.protocol}
            onChange={(value) =>
              setDraft((current) => ({ ...current, protocol: value as EgressProtocol }))
            }
            options={[
              { value: "socks5", label: "SOCKS5" },
              { value: "http", label: "HTTP CONNECT" },
            ]}
            aria-label={t("egress.endpoints.protocol")}
          />
        </label>
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
          <span>{t("egress.endpoints.host")}</span>
          <TextInput
            aria-label={t("egress.endpoints.host")}
            value={draft.host}
            onChange={(event) => setDraft((current) => ({ ...current, host: event.target.value }))}
          />
        </label>
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
          <span>{t("egress.endpoints.port")}</span>
          <TextInput
            aria-label={t("egress.endpoints.port")}
            inputMode="numeric"
            value={draft.port}
            onChange={(event) => setDraft((current) => ({ ...current, port: event.target.value }))}
          />
        </label>
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75 sm:col-span-2">
          <span>{t("egress.endpoints.expected_public_ip")}</span>
          <TextInput
            aria-label={t("egress.endpoints.expected_public_ip")}
            autoComplete="off"
            spellCheck={false}
            placeholder="203.0.113.8 or 2001:db8::8"
            value={draft.expectedPublicIp}
            onChange={(event) =>
              setDraft((current) => ({ ...current, expectedPublicIp: event.target.value }))
            }
          />
          <span className="block font-normal text-slate-500 dark:text-white/50">
            {t("egress.endpoints.expected_public_ip_hint")}
          </span>
        </label>
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
          <span>{t("egress.endpoints.username")}</span>
          <TextInput
            aria-label={t("egress.endpoints.username")}
            autoComplete="off"
            disabled={draft.clearCredentials}
            value={draft.username}
            onChange={(event) =>
              setDraft((current) => ({ ...current, username: event.target.value }))
            }
          />
        </label>
        <label className="space-y-2 text-xs font-semibold text-slate-700 dark:text-white/75">
          <span>{t("egress.endpoints.password")}</span>
          <TextInput
            aria-label={t("egress.endpoints.password")}
            type="password"
            autoComplete="new-password"
            disabled={draft.clearCredentials}
            value={draft.password}
            onChange={(event) =>
              setDraft((current) => ({ ...current, password: event.target.value }))
            }
          />
          {isEdit && endpoint.hasCredentials && !draft.clearCredentials ? (
            <span className="block font-normal text-slate-500 dark:text-white/50">
              {t("egress.endpoints.password_keep")}
            </span>
          ) : null}
        </label>
        {isEdit && endpoint.hasCredentials ? (
          <div className="sm:col-span-2 rounded-xl border border-slate-200 p-3 dark:border-neutral-800">
            <ToggleSwitch
              checked={draft.clearCredentials}
              onCheckedChange={(clearCredentials) =>
                setDraft((current) => ({
                  ...current,
                  clearCredentials,
                  username: clearCredentials ? "" : endpoint.username ?? "",
                  password: "",
                }))
              }
              label={t("egress.endpoints.clear_credentials")}
              description={t("egress.endpoints.clear_credentials_hint")}
            />
          </div>
        ) : null}
        <div className="sm:col-span-2 rounded-xl border border-slate-200 p-3 dark:border-neutral-800">
          <ToggleSwitch
            checked={draft.enabled}
            onCheckedChange={(enabled) => setDraft((current) => ({ ...current, enabled }))}
            label={t("egress.endpoints.enabled")}
            description={t("egress.endpoints.enabled_hint")}
          />
        </div>
      </div>
      {error ? <p className="mt-4 text-sm text-rose-600 dark:text-rose-300">{error}</p> : null}
    </Modal>
  );
}
