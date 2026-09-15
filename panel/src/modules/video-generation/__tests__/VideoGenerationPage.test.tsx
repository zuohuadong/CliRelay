import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import i18n from "@/i18n";
import { videoGenerationApi } from "@/lib/http/apis";
import { VideoGenerationPage } from "@/modules/video-generation/VideoGenerationPage";
import { ThemeProvider } from "@/modules/ui/ThemeProvider";
import { ToastProvider } from "@/modules/ui/ToastProvider";

const videoGenerationChannelsMock = () =>
  videoGenerationApi.getChannels as unknown as ReturnType<typeof vi.fn>;
const videoGenerationStartTaskMock = () =>
  videoGenerationApi.startTestTask as unknown as ReturnType<typeof vi.fn>;
const videoGenerationGetTaskMock = () =>
  videoGenerationApi.getTestTask as unknown as ReturnType<typeof vi.fn>;

function renderPage() {
  return render(
    <MemoryRouter>
      <ThemeProvider>
        <ToastProvider>
          <VideoGenerationPage />
        </ToastProvider>
      </ThemeProvider>
    </MemoryRouter>,
  );
}

describe("VideoGenerationPage", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("zh-CN");
    vi.spyOn(videoGenerationApi, "getChannels");
    vi.spyOn(videoGenerationApi, "startTestTask");
    vi.spyOn(videoGenerationApi, "getTestTask");
    videoGenerationChannelsMock().mockResolvedValue({
      items: [{ provider: "agnes", model: "agnes-video-v2.0", type: "openai-compatible" }],
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  test("renders video call docs and starts a generation test", async () => {
    videoGenerationStartTaskMock().mockResolvedValue({ task_id: "vid_task_1", status: "queued" });
    videoGenerationGetTaskMock().mockResolvedValue({
      task_id: "vid_task_1",
      status: "succeeded",
      result: {
        id: "vid_1",
        status: "completed",
        video_url: "https://example.com/video.mp4",
      },
    });

    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByRole("heading", { name: "视频模型" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "agnes-video-v2.0" })).toBeInTheDocument();
    expect(screen.getByText("/v1/videos")).toBeInTheDocument();
    expect(screen.getByText(/curl http:\/\/127\.0\.0\.1:8317\/v1\/videos/)).toBeInTheDocument();

    const openButton = screen.getByTestId("video-generation-open-test");
    await waitFor(() => expect(openButton).toBeEnabled());
    await user.click(openButton);
    const dialog = await screen.findByTestId("video-generation-modal");
    await user.type(within(dialog).getByTestId("video-generation-prompt"), "rainy neon street");
    await user.click(within(dialog).getByTestId("video-generation-send-button"));

    expect(await within(dialog).findByTestId("video-generation-player")).toHaveAttribute(
      "src",
      "https://example.com/video.mp4",
    );
    expect(videoGenerationStartTaskMock()).toHaveBeenCalledWith({
      model: "agnes-video-v2.0",
      prompt: "rainy neon street",
      seconds: "4",
      size: "720x1280",
    });
  });

  test("shows the empty hint when no video channel is configured", async () => {
    videoGenerationChannelsMock().mockResolvedValue({ items: [] });

    renderPage();

    expect(await screen.findByText("当前没有可用的视频生成渠道。")).toBeInTheDocument();
    expect(screen.getByTestId("video-generation-open-test")).toBeDisabled();
    expect(screen.getByTestId("video-generation-disabled-state")).toHaveClass("opacity-60");
  });

  test("falls back to JSON when the test result has no playable url", async () => {
    videoGenerationStartTaskMock().mockResolvedValue({ task_id: "vid_task_2", status: "queued" });
    videoGenerationGetTaskMock().mockResolvedValue({
      task_id: "vid_task_2",
      status: "succeeded",
      result: {
        id: "vid_2",
        status: "completed",
        progress: 100,
      },
    });

    const user = userEvent.setup();
    renderPage();

    const openButton = await screen.findByTestId("video-generation-open-test");
    await waitFor(() => expect(openButton).toBeEnabled());
    await user.click(openButton);
    const dialog = await screen.findByTestId("video-generation-modal");
    fireEvent.change(within(dialog).getByTestId("video-generation-prompt"), {
      target: { value: "a quiet harbor at dusk" },
    });
    await user.click(within(dialog).getByTestId("video-generation-send-button"));

    expect(await within(dialog).findByTestId("video-generation-json")).toHaveTextContent(
      '"id": "vid_2"',
    );
  });
});
