import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { videoGenerationApi } from "@/lib/http/apis";
import { Button } from "@/modules/ui/Button";
import { Card } from "@/modules/ui/Card";
import { Modal } from "@/modules/ui/Modal";
import { Select } from "@/modules/ui/Select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/modules/ui/Tabs";
import { VirtualTable, type VirtualTableColumn } from "@/modules/ui/VirtualTable";

const DEFAULT_VIDEO_MODEL = "agnes-video-v2.0";
const VIDEO_GENERATION_TASK_POLL_INTERVAL_MS = 1200;
const SIZE_OPTIONS = ["720x1280", "1280x720", "1024x1792", "1792x1024"] as const;
const SECONDS_OPTIONS = ["4", "8", "12", "15"] as const;

type SpecRow = {
  name: string;
  type: string;
  required: boolean;
  descriptionKey: string;
};

type VideoGenerationChannel = {
  provider: string;
  model: string;
  type?: string;
};

const REQUEST_ROWS: SpecRow[] = [
  {
    name: "model",
    type: "string",
    required: true,
    descriptionKey: "video_generation.param_model_desc",
  },
  {
    name: "prompt",
    type: "string",
    required: true,
    descriptionKey: "video_generation.param_prompt_desc",
  },
  {
    name: "seconds",
    type: "string",
    required: false,
    descriptionKey: "video_generation.param_seconds_desc",
  },
  {
    name: "size",
    type: "string",
    required: false,
    descriptionKey: "video_generation.param_size_desc",
  },
];

const RESPONSE_ROWS: SpecRow[] = [
  {
    name: "id",
    type: "string",
    required: true,
    descriptionKey: "video_generation.response_id_desc",
  },
  {
    name: "status",
    type: "string",
    required: true,
    descriptionKey: "video_generation.response_status_desc",
  },
  {
    name: "video_url",
    type: "string",
    required: false,
    descriptionKey: "video_generation.response_video_url_desc",
  },
  {
    name: "progress",
    type: "number",
    required: false,
    descriptionKey: "video_generation.response_progress_desc",
  },
];

function uniqueModels(channels: VideoGenerationChannel[]): string[] {
  const seen = new Set<string>();
  const models: string[] = [];
  for (const channel of channels) {
    const model = channel.model.trim();
    if (!model || seen.has(model)) continue;
    seen.add(model);
    models.push(model);
  }
  return models;
}

function buildVideoCurl(model: string): string {
  return [
    "curl http://127.0.0.1:8317/v1/videos \\",
    '  -H "Authorization: Bearer $API_KEY" \\',
    '  -H "Content-Type: application/json" \\',
    "  -d '{",
    `    "model": ${JSON.stringify(model)},`,
    '    "prompt": "A cinematic tracking shot through a rainy neon street",',
    '    "seconds": 4,',
    '    "size": "720x1280"',
    "  }'",
  ].join("\n");
}

export function VideoGenerationPage() {
  const { t } = useTranslation();
  const [channels, setChannels] = useState<VideoGenerationChannel[]>([]);
  const [channelsLoading, setChannelsLoading] = useState(true);
  const [activeModel, setActiveModel] = useState(DEFAULT_VIDEO_MODEL);
  const [testOpen, setTestOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;

    const loadAvailability = async () => {
      setChannelsLoading(true);
      try {
        const response = await videoGenerationApi.getChannels();
        if (cancelled) return;
        setChannels(response.items ?? []);
      } catch {
        if (!cancelled) {
          setChannels([]);
        }
      } finally {
        if (!cancelled) {
          setChannelsLoading(false);
        }
      }
    };

    void loadAvailability();
    return () => {
      cancelled = true;
    };
  }, []);

  const models = useMemo(() => uniqueModels(channels), [channels]);
  const visibleModels = models.length > 0 ? models : [DEFAULT_VIDEO_MODEL];
  const disabled = !channelsLoading && models.length === 0;

  useEffect(() => {
    if (!models.includes(activeModel)) {
      setActiveModel(models[0] ?? DEFAULT_VIDEO_MODEL);
    }
  }, [activeModel, models]);

  const openTest = useCallback(() => {
    if (disabled || channelsLoading) return;
    setTestOpen(true);
  }, [channelsLoading, disabled]);

  return (
    <div className="min-w-0 space-y-6 overflow-x-hidden">
      <section className="space-y-4">
        <h2 className="text-xl font-semibold tracking-tight text-slate-900 dark:text-white">
          {t("video_generation.title")}
        </h2>

        <Tabs value={activeModel} onValueChange={setActiveModel}>
          <TabsList>
            {visibleModels.map((model) => (
              <TabsTrigger key={model} value={model}>
                {model}
              </TabsTrigger>
            ))}
          </TabsList>

          <TabsContent value={activeModel} className="mt-4 space-y-4">
            {disabled ? (
              <div className="rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm font-medium text-amber-900 dark:border-amber-400/20 dark:bg-amber-400/10 dark:text-amber-100">
                {t("video_generation.channels_empty")}
              </div>
            ) : null}

            <div
              data-testid={disabled ? "video-generation-disabled-state" : undefined}
              className={disabled ? "space-y-4 opacity-60" : "space-y-4"}
              aria-disabled={disabled}
            >
              <Card
                title={t("video_generation.call_title")}
                description={t("video_generation.call_description")}
                actions={
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={openTest}
                    disabled={channelsLoading || disabled}
                    aria-busy={channelsLoading}
                    data-testid="video-generation-open-test"
                  >
                    {t("video_generation.open_test_button")}
                  </Button>
                }
              >
                <EndpointCallDoc model={activeModel} />
              </Card>

              <div className="grid gap-4 xl:grid-cols-2">
                <SpecTable title={t("video_generation.request_params_title")} rows={REQUEST_ROWS} />
                <SpecTable
                  title={t("video_generation.response_schema_title")}
                  rows={RESPONSE_ROWS}
                />
              </div>
            </div>
          </TabsContent>
        </Tabs>
      </section>

      <VideoGenerationTestModal
        open={testOpen}
        model={activeModel}
        models={visibleModels}
        onClose={() => setTestOpen(false)}
      />
    </div>
  );
}

function EndpointCallDoc({ model }: { model: string }) {
  const { t } = useTranslation();

  return (
    <div className="space-y-4">
      <div className="rounded-[22px] border border-slate-200 bg-slate-50 p-4 dark:border-neutral-800 dark:bg-neutral-900">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-sm font-semibold text-slate-900 dark:text-white">
              {t("video_generation.text_to_video_title")}
            </p>
            <p className="mt-1 text-xs leading-5 text-slate-600 dark:text-white/55">
              {t("video_generation.text_to_video_desc")}
            </p>
          </div>
          <div className="flex max-w-full items-center gap-2 rounded-full border border-slate-200 bg-white px-3 py-1.5 font-mono text-xs dark:border-neutral-800 dark:bg-neutral-950">
            <span className="rounded-full bg-slate-900 px-2 py-0.5 font-semibold text-white dark:bg-white dark:text-neutral-950">
              POST
            </span>
            <span className="truncate text-slate-700 dark:text-white/75">/v1/videos</span>
          </div>
        </div>
        <div className="mt-3 flex flex-wrap gap-2 text-xs text-slate-600 dark:text-white/55">
          <span className="rounded-full bg-white px-2.5 py-1 dark:bg-neutral-950">
            Authorization: Bearer YOUR_API_KEY
          </span>
          <span className="rounded-full bg-white px-2.5 py-1 dark:bg-neutral-950">
            application/json
          </span>
        </div>
      </div>

      <div className="overflow-hidden rounded-2xl bg-slate-950 shadow-[0_14px_42px_rgb(15_23_42_/_0.16)] dark:bg-black/45">
        <div className="border-b border-white/10 px-4 py-2 text-xs font-medium text-slate-300">
          curl
        </div>
        <pre className="overflow-x-auto px-4 py-3 text-[13px] leading-6 text-slate-100">
          <code>{buildVideoCurl(model)}</code>
        </pre>
      </div>
    </div>
  );
}

function SpecTable({ title, rows }: { title: string; rows: SpecRow[] }) {
  const { t } = useTranslation();
  const columns = useMemo<VirtualTableColumn<SpecRow>[]>(
    () => [
      {
        key: "name",
        label: t("video_generation.table_param"),
        width: "w-40",
        cellClassName: "font-mono text-xs break-all leading-5 text-slate-900 dark:text-white",
        render: (row) => row.name,
      },
      {
        key: "type",
        label: t("video_generation.table_type"),
        width: "w-28",
        cellClassName: "font-mono text-xs text-slate-600 dark:text-white/55",
        render: (row) => row.type,
      },
      {
        key: "required",
        label: t("video_generation.table_required"),
        width: "w-20",
        cellClassName: "text-xs text-slate-600 dark:text-white/55",
        render: (row) => (row.required ? t("common.yes") : t("common.no")),
      },
      {
        key: "description",
        label: t("video_generation.table_description"),
        cellClassName: "text-xs leading-5 text-slate-600 dark:text-white/60",
        render: (row) => t(row.descriptionKey),
      },
    ],
    [t],
  );

  return (
    <div
      data-testid="video-generation-spec-card"
      className="overflow-hidden rounded-2xl bg-white p-4 dark:bg-neutral-950/80"
    >
      <h4 className="text-sm font-semibold text-slate-900 dark:text-white">{title}</h4>
      <div className="mt-4">
        <VirtualTable<SpecRow>
          rows={rows}
          columns={columns}
          rowKey={(row) => row.name}
          virtualize={false}
          height="h-auto"
          minHeight="min-h-0"
          minWidth="min-w-[560px]"
          caption={`${title} table`}
          rowHeight={48}
          showAllLoadedMessage={false}
        />
      </div>
    </div>
  );
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

function extractTaskError(error: unknown): string | null {
  if (!error || typeof error !== "object") return null;
  const body =
    "body" in error && error.body && typeof error.body === "object"
      ? (error.body as Record<string, unknown>)
      : null;
  const nested =
    body?.error && typeof body.error === "object" && !Array.isArray(body.error)
      ? (body.error as Record<string, unknown>)
      : null;
  const message = nested?.message;
  return typeof message === "string" && message.trim() ? message.trim() : null;
}

function extractVideoUrl(result: Record<string, unknown> | null): string | null {
  if (!result) return null;
  for (const key of ["video_url", "url"] as const) {
    const value = result[key];
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  const video = result.video;
  if (video && typeof video === "object" && !Array.isArray(video)) {
    const nestedUrl = (video as { url?: unknown }).url;
    if (typeof nestedUrl === "string" && nestedUrl.trim()) return nestedUrl.trim();
  }
  return null;
}

function extractResultError(result: Record<string, unknown> | null): string | null {
  if (!result) return null;
  const status = typeof result.status === "string" ? result.status.toLowerCase() : "";
  const error = result.error;
  const message =
    error && typeof error === "object" && !Array.isArray(error)
      ? (error as { message?: unknown }).message
      : typeof error === "string"
        ? error
        : null;
  if (status !== "failed" && status !== "error") return null;
  return typeof message === "string" && message.trim() ? message.trim() : null;
}

function VideoGenerationTestModal({
  open,
  model,
  models,
  onClose,
}: {
  open: boolean;
  model: string;
  models: string[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [selectedModel, setSelectedModel] = useState(model);
  const [prompt, setPrompt] = useState("");
  const [size, setSize] = useState<(typeof SIZE_OPTIONS)[number]>("720x1280");
  const [seconds, setSeconds] = useState<(typeof SECONDS_OPTIONS)[number]>("4");
  const [submitting, setSubmitting] = useState(false);
  const [videoUrl, setVideoUrl] = useState("");
  const [resultJson, setResultJson] = useState("");
  const [errorMessage, setErrorMessage] = useState("");
  const generationSessionRef = useRef(0);

  useEffect(() => {
    if (!open) return;
    generationSessionRef.current += 1;
    setSelectedModel(model);
    setPrompt("");
    setSize("720x1280");
    setSeconds("4");
    setSubmitting(false);
    setVideoUrl("");
    setResultJson("");
    setErrorMessage("");
  }, [model, open]);

  const canSend = Boolean(prompt.trim()) && !submitting;

  const handleGenerate = async () => {
    const trimmedPrompt = prompt.trim();
    if (!trimmedPrompt || submitting) return;

    const sessionId = generationSessionRef.current + 1;
    generationSessionRef.current = sessionId;
    setSubmitting(true);
    setVideoUrl("");
    setResultJson("");
    setErrorMessage("");

    try {
      const startTask = await videoGenerationApi.startTestTask({
        model: selectedModel,
        prompt: trimmedPrompt,
        seconds,
        size,
      });
      if (generationSessionRef.current !== sessionId) return;
      if (!startTask.task_id) {
        throw new Error(t("video_generation.test_failed_generic"));
      }

      let task = await videoGenerationApi.getTestTask(startTask.task_id);
      while (generationSessionRef.current === sessionId) {
        if (task.status === "succeeded") {
          const result =
            task.result && typeof task.result === "object"
              ? (task.result as Record<string, unknown>)
              : null;
          const resultError = extractResultError(result);
          if (resultError) {
            throw new Error(resultError);
          }
          const nextUrl = extractVideoUrl(result);
          setVideoUrl(nextUrl ?? "");
          setResultJson(result ? JSON.stringify(result, null, 2) : "");
          if (!nextUrl && !result) {
            throw new Error(t("video_generation.test_empty_result"));
          }
          return;
        }

        if (task.status === "failed") {
          throw new Error(
            extractTaskError(task.error) ?? t("video_generation.test_failed_generic"),
          );
        }

        await wait(VIDEO_GENERATION_TASK_POLL_INTERVAL_MS);
        if (generationSessionRef.current !== sessionId) return;
        task = await videoGenerationApi.getTestTask(startTask.task_id);
      }
    } catch (error) {
      if (generationSessionRef.current !== sessionId) return;
      setErrorMessage(
        error instanceof Error ? error.message : t("video_generation.test_failed_generic"),
      );
    } finally {
      if (generationSessionRef.current === sessionId) {
        setSubmitting(false);
      }
    }
  };

  return (
    <Modal
      open={open}
      title={t("video_generation.test_title")}
      description={t("video_generation.test_description")}
      onClose={onClose}
      maxWidth="max-w-3xl"
      bodyTestId="video-generation-modal"
    >
      <div className="space-y-4">
        <div
          className={[
            "relative overflow-hidden rounded-2xl border",
            "h-[clamp(220px,36vh,360px)]",
            errorMessage
              ? "border-rose-200 bg-rose-50 dark:border-rose-400/20 dark:bg-rose-400/10"
              : "border-slate-200 bg-slate-50 dark:border-neutral-800 dark:bg-neutral-900",
          ].join(" ")}
        >
          {videoUrl ? (
            <video
              data-testid="video-generation-player"
              src={videoUrl}
              controls
              className="h-full w-full bg-black object-contain"
            />
          ) : resultJson ? (
            <pre
              data-testid="video-generation-json"
              className="h-full overflow-auto px-4 py-3 text-xs leading-5 text-slate-700 dark:text-white/75"
            >
              {resultJson}
            </pre>
          ) : (
            <div className="flex h-full items-center justify-center px-6 text-center text-sm text-slate-500 dark:text-white/45">
              {submitting
                ? t("video_generation.generating_subtitle")
                : errorMessage || t("video_generation.idle_hint")}
            </div>
          )}
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <label className="space-y-1.5 text-xs font-medium text-slate-600 dark:text-white/55">
            {t("video_generation.model_label")}
            <Select
              value={selectedModel}
              onChange={setSelectedModel}
              options={models.map((item) => ({ value: item, label: item }))}
              aria-label={t("video_generation.model_label")}
            />
          </label>
          <label className="space-y-1.5 text-xs font-medium text-slate-600 dark:text-white/55">
            {t("video_generation.size_label")}
            <Select
              value={size}
              onChange={(value) => setSize(value as (typeof SIZE_OPTIONS)[number])}
              options={SIZE_OPTIONS.map((item) => ({ value: item, label: item }))}
              aria-label={t("video_generation.size_label")}
            />
          </label>
          <label className="space-y-1.5 text-xs font-medium text-slate-600 dark:text-white/55">
            {t("video_generation.seconds_label")}
            <Select
              value={seconds}
              onChange={(value) => setSeconds(value as (typeof SECONDS_OPTIONS)[number])}
              options={SECONDS_OPTIONS.map((item) => ({
                value: item,
                label: t("video_generation.seconds_option", { count: item }),
              }))}
              aria-label={t("video_generation.seconds_label")}
            />
          </label>
        </div>

        <label className="block space-y-1.5 text-xs font-medium text-slate-600 dark:text-white/55">
          {t("video_generation.prompt_label")}
          <textarea
            data-testid="video-generation-prompt"
            value={prompt}
            onChange={(event) => setPrompt(event.target.value)}
            placeholder={t("video_generation.prompt_placeholder")}
            rows={4}
            className="min-h-[112px] w-full resize-none rounded-2xl border border-slate-200 bg-white px-3 py-2 text-sm leading-6 text-slate-900 outline-none placeholder:text-slate-400 dark:border-neutral-800 dark:bg-neutral-950 dark:text-white dark:placeholder:text-white/30"
          />
        </label>

        <div className="flex justify-end">
          <Button
            variant="primary"
            onClick={() => void handleGenerate()}
            disabled={!canSend}
            aria-busy={submitting}
            data-testid="video-generation-send-button"
          >
            {submitting
              ? t("video_generation.generating_button")
              : t("video_generation.generate_button")}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
