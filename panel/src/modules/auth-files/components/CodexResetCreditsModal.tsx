import { CheckCircle2, Loader2, RotateCcw, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/modules/ui/Button";
import { Modal } from "@/modules/ui/Modal";
import type { CodexResetCreditsState } from "@/modules/auth-files/hooks/useCodexResetCredits";

const formatTimestamp = (value: string | null): string => {
  if (!value) return "--";
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) return value;
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(parsed);
};

export function CodexResetCreditsModal({
  state,
  onClose,
  onSelectCredit,
  onRedeem,
}: {
  state: CodexResetCreditsState;
  onClose: () => void;
  onSelectCredit: (creditId: string) => void;
  onRedeem: () => void;
}) {
  const { t } = useTranslation();
  const selectedCredit = state.credits.find((credit) => credit.id === state.selectedCreditId);
  const errorKey = state.errorCode ? `auth_files.codex_reset_error_${state.errorCode}` : null;

  return (
    <Modal
      open={state.open}
      title={t("auth_files.codex_reset_title")}
      description={state.file?.name}
      onClose={onClose}
      maxWidth="max-w-2xl"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={state.consuming}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={onRedeem}
            disabled={state.loading || state.consuming || !selectedCredit}
          >
            {state.consuming ? (
              <Loader2 size={15} className="animate-spin" />
            ) : (
              <RotateCcw size={15} />
            )}
            {state.consuming
              ? t("auth_files.codex_reset_consuming")
              : t("auth_files.codex_reset_confirm")}
          </Button>
        </>
      }
    >
      <div className="space-y-4" data-testid="codex-reset-credits-modal">
        <div className="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm leading-relaxed text-amber-900 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-100">
          {t("auth_files.codex_reset_warning")}
        </div>

        {state.loading ? (
          <div className="flex items-center justify-center gap-2 py-8 text-sm text-slate-500 dark:text-white/55">
            <Loader2 size={18} className="animate-spin" />
            {t("auth_files.codex_reset_loading")}
          </div>
        ) : errorKey ? (
          <div className="flex items-start gap-2 rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-800 dark:border-rose-500/25 dark:bg-rose-500/10 dark:text-rose-100">
            <TriangleAlert size={17} className="mt-0.5 shrink-0" />
            <span>{t(errorKey, { detail: state.errorDetail ?? "" })}</span>
          </div>
        ) : state.credits.length === 0 ? (
          <div className="rounded-xl border border-slate-200 bg-slate-50 px-4 py-6 text-center text-sm text-slate-600 dark:border-neutral-800 dark:bg-neutral-900 dark:text-white/65">
            {t("auth_files.codex_reset_no_credits")}
          </div>
        ) : (
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3 text-sm">
              <span className="font-semibold text-slate-900 dark:text-white">
                {t("auth_files.codex_reset_available", { count: state.availableCount })}
              </span>
              <span className="text-xs text-slate-500 dark:text-white/50">
                {t("auth_files.codex_reset_choose")}
              </span>
            </div>
            {state.credits.map((credit) => {
              const selected = credit.id === state.selectedCreditId;
              return (
                <label
                  key={credit.id}
                  className={[
                    "flex cursor-pointer items-start gap-3 rounded-xl border px-4 py-3 transition-colors",
                    selected
                      ? "border-blue-400 bg-blue-50/80 dark:border-blue-400/60 dark:bg-blue-500/10"
                      : "border-slate-200 bg-white hover:border-slate-300 dark:border-neutral-800 dark:bg-neutral-950 dark:hover:border-neutral-700",
                  ].join(" ")}
                >
                  <input
                    type="radio"
                    name="codex-reset-credit"
                    value={credit.id}
                    checked={selected}
                    onChange={() => onSelectCredit(credit.id)}
                    disabled={state.consuming}
                    className="mt-1"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm font-semibold text-slate-900 dark:text-white">
                      {credit.title ?? t("auth_files.codex_reset_default_credit_title")}
                    </span>
                    {credit.description ? (
                      <span className="mt-1 block text-xs leading-relaxed text-slate-600 dark:text-white/60">
                        {credit.description}
                      </span>
                    ) : null}
                    <span className="mt-2 block text-xs text-slate-500 dark:text-white/45">
                      {t("auth_files.codex_reset_expires", {
                        time: formatTimestamp(credit.expiresAt),
                      })}
                    </span>
                  </span>
                </label>
              );
            })}
          </div>
        )}

        {state.outcome ? (
          <div
            className={[
              "flex items-start gap-2 rounded-xl border px-4 py-3 text-sm",
              state.verificationWarning
                ? "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-100"
                : "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-500/25 dark:bg-emerald-500/10 dark:text-emerald-100",
            ].join(" ")}
          >
            {state.verificationWarning ? (
              <TriangleAlert size={17} className="mt-0.5 shrink-0" />
            ) : (
              <CheckCircle2 size={17} className="mt-0.5 shrink-0" />
            )}
            <span>
              {t(`auth_files.codex_reset_outcome_${state.outcome}`, {
                count: state.windowsReset,
              })}
              {state.verificationWarning ? ` ${t("auth_files.codex_reset_verify_failed")}` : ""}
            </span>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
