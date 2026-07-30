import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";
import { Plus, RefreshCw, ShieldCheck, X } from "lucide-react";
import { Button } from "@/modules/ui/Button";
import { Checkbox } from "@/modules/ui/Checkbox";
import { EmptyState } from "@/modules/ui/EmptyState";
import { TextInput } from "@/modules/ui/Input";
import type { AliasRow } from "@/modules/auth-files/helpers/authFilesPageUtils";

interface AuthFilesAliasTabProps {
  aliasLoading: boolean;
  isPending: boolean;
  refreshAlias: () => Promise<void>;
  aliasUnsupported: boolean;
  aliasNewChannel: string;
  setAliasNewChannel: Dispatch<SetStateAction<string>>;
  addAliasChannel: () => void;
  aliasEditing: Record<string, AliasRow[]>;
  setAliasEditing: Dispatch<SetStateAction<Record<string, AliasRow[]>>>;
  openImport: (channel: string) => Promise<void>;
  saveAliasChannel: (channel: string) => Promise<void>;
  deleteAliasChannel: (channel: string) => Promise<void>;
}

export function AuthFilesAliasTab({
  aliasLoading,
  isPending,
  refreshAlias,
  aliasUnsupported,
  aliasNewChannel,
  setAliasNewChannel,
  addAliasChannel,
  aliasEditing,
  setAliasEditing,
  openImport,
  saveAliasChannel,
  deleteAliasChannel,
}: AuthFilesAliasTabProps) {
  const { t } = useTranslation();

  return (
    <div className="mt-4 space-y-4">
      <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div>
          <h2 className="text-lg font-bold text-slate-900 dark:text-white">
            {t("auth_files_page.alias_title")}
          </h2>
          <p className="mt-1 text-sm text-slate-500 dark:text-neutral-400">
            {t("auth_files.model_alias_desc")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void refreshAlias()}
            disabled={aliasLoading || isPending}
          >
            <RefreshCw size={14} className={aliasLoading ? "animate-spin" : ""} />
            {t("auth_files.refresh")}
          </Button>
        </div>
      </div>

      {aliasLoading ? (
        <div className="flex h-32 items-center justify-center text-sm text-slate-500">
          {t("common.loading_ellipsis")}
        </div>
      ) : (
        <div className="space-y-4">
          {aliasUnsupported ? (
            <div className="mb-4">
              <EmptyState
                title={t("auth_files.api_not_supported")}
                description={t("auth_files.no_alias_api")}
              />
            </div>
          ) : null}
          <div className="flex flex-wrap items-center gap-2">
            <TextInput
              value={aliasNewChannel}
              onChange={(e) => setAliasNewChannel(e.currentTarget.value)}
              placeholder={t("auth_files.add_channel_placeholder")}
              disabled={aliasUnsupported}
            />
            <Button
              variant="primary"
              size="sm"
              onClick={addAliasChannel}
              disabled={isPending || aliasUnsupported}
            >
              <Plus size={14} />
              {t("auth_files.add")}
            </Button>
          </div>

          <div className="mt-4 space-y-3">
            {Object.keys(aliasEditing).length === 0 ? (
              <EmptyState
                title={t("auth_files.no_config")}
                description={t("auth_files_page.alias_no_config")}
              />
            ) : (
              Object.keys(aliasEditing)
                .sort((a, b) => a.localeCompare(b))
                .map((channel) => {
                  const rows = aliasEditing[channel] ?? [];
                  const mappingCount = rows.filter((r) => r.name.trim() && r.alias.trim()).length;

                  return (
                    <div
                      key={channel}
                      className="rounded-2xl border border-black/[0.06] bg-white p-4 shadow-[0_1px_2px_rgb(15_23_42_/_0.035)] transition-colors duration-200 ease-out dark:border-white/[0.06] dark:bg-neutral-950/70 dark:shadow-[0_1px_2px_rgb(0_0_0_/_0.22)]"
                    >
                      <div className="flex flex-wrap items-center justify-between gap-3">
                        <div className="min-w-0">
                          <p className="font-mono text-xs text-slate-900 dark:text-white">
                            {channel}
                          </p>
                          <p className="mt-1 text-xs text-slate-500 dark:text-white/55">
                            {t("auth_files.valid_mappings", { count: mappingCount })}
                          </p>
                        </div>
                        <div className="flex items-center gap-2">
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={() => void openImport(channel)}
                            disabled={aliasUnsupported}
                          >
                            <ShieldCheck size={14} />
                            {t("auth_files.import_models")}
                          </Button>
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={() => void saveAliasChannel(channel)}
                            disabled={isPending || aliasUnsupported}
                          >
                            {t("auth_files.save")}
                          </Button>
                          <Button
                            variant="danger"
                            size="sm"
                            onClick={() => void deleteAliasChannel(channel)}
                            disabled={isPending || aliasUnsupported}
                          >
                            {t("common.delete")}
                          </Button>
                        </div>
                      </div>

                      <div className="mt-3 space-y-2">
                        {rows.map((row, idx) => (
                          <div key={row.id} className="grid gap-2 lg:grid-cols-12">
                            <div className="lg:col-span-3">
                              <TextInput
                                value={row.name}
                                onChange={(e) => {
                                  const value = e.currentTarget.value;
                                  setAliasEditing((prev) => ({
                                    ...prev,
                                    [channel]: (prev[channel] ?? []).map((it, i) =>
                                      i === idx ? { ...it, name: value } : it,
                                    ),
                                  }));
                                }}
                                placeholder={t("auth_files.name_placeholder", "name")}
                              />
                            </div>
                            <div className="lg:col-span-3">
                              <TextInput
                                value={row.alias}
                                onChange={(e) => {
                                  const value = e.currentTarget.value;
                                  setAliasEditing((prev) => ({
                                    ...prev,
                                    [channel]: (prev[channel] ?? []).map((it, i) =>
                                      i === idx ? { ...it, alias: value } : it,
                                    ),
                                  }));
                                }}
                                placeholder={t("auth_files.alias_placeholder", "alias")}
                              />
                            </div>
                            <div className="lg:col-span-3">
                              <TextInput
                                value={row.displayName ?? ""}
                                onChange={(e) => {
                                  const value = e.currentTarget.value;
                                  setAliasEditing((prev) => ({
                                    ...prev,
                                    [channel]: (prev[channel] ?? []).map((it, i) =>
                                      i === idx ? { ...it, displayName: value } : it,
                                    ),
                                  }));
                                }}
                                placeholder={t("auth_files.display_name_placeholder")}
                              />
                            </div>
                            <div className="flex flex-wrap items-center gap-3 rounded-2xl border border-slate-200 bg-white px-3 py-2 shadow-sm transition-colors duration-200 ease-out lg:col-span-2 dark:border-neutral-800 dark:bg-neutral-950/60">
                              <label className="flex items-center gap-2 text-xs text-slate-600 dark:text-white/65">
                                <Checkbox
                                  checked={Boolean(row.fork)}
                                  onCheckedChange={(checked) => {
                                    setAliasEditing((prev) => ({
                                      ...prev,
                                      [channel]: (prev[channel] ?? []).map((it, i) =>
                                        i === idx ? { ...it, fork: checked } : it,
                                      ),
                                    }));
                                  }}
                                />
                                {t("auth_files.fork")}
                              </label>
                              <label className="flex items-center gap-2 text-xs text-slate-600 dark:text-white/65">
                                <Checkbox
                                  checked={Boolean(row.forceMapping)}
                                  onCheckedChange={(checked) => {
                                    setAliasEditing((prev) => ({
                                      ...prev,
                                      [channel]: (prev[channel] ?? []).map((it, i) =>
                                        i === idx ? { ...it, forceMapping: checked } : it,
                                      ),
                                    }));
                                  }}
                                />
                                {t("auth_files.force_mapping")}
                              </label>
                            </div>
                            <div className="lg:col-span-1 flex items-center justify-end">
                              <Button
                                variant="danger"
                                size="sm"
                                onClick={() => {
                                  setAliasEditing((prev) => ({
                                    ...prev,
                                    [channel]: (prev[channel] ?? []).filter((_, i) => i !== idx),
                                  }));
                                }}
                                aria-label={t("common.delete_row", "Delete Row")}
                                title={t("common.delete")}
                              >
                                <X size={14} />
                              </Button>
                            </div>
                          </div>
                        ))}

                        <div className="flex items-center gap-2">
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={() => {
                              setAliasEditing((prev) => ({
                                ...prev,
                                [channel]: [
                                  ...(prev[channel] ?? []),
                                  { id: `row-${Date.now()}`, name: "", alias: "" },
                                ],
                              }));
                            }}
                          >
                            <Plus size={14} />
                            {t("auth_files.add_row")}
                          </Button>
                        </div>
                      </div>
                    </div>
                  );
                })
            )}
          </div>
        </div>
      )}
    </div>
  );
}
