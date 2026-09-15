import { beforeEach, describe, expect, test, vi } from "vitest";

const postMock = vi.fn();
const getMock = vi.fn();

vi.mock("@/lib/http/client", () => ({
  apiClient: {
    get: getMock,
    post: postMock,
  },
}));

describe("videoGenerationApi", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  test("loads video generation channels from the management endpoint", async () => {
    const { videoGenerationApi } = await import("@/lib/http/apis/video-generation");

    getMock.mockResolvedValue({ items: [{ provider: "agnes", model: "agnes-video-v2.0" }] });

    await videoGenerationApi.getChannels();

    expect(getMock).toHaveBeenCalledWith("/video-generation/channels");
  });

  test("creates a background task for video generation tests", async () => {
    const { videoGenerationApi } = await import("@/lib/http/apis/video-generation");

    postMock.mockResolvedValue({ task_id: "vid_task_1", status: "queued" });

    await videoGenerationApi.startTestTask({
      model: "agnes-video-v2.0",
      prompt: "rainy neon street",
      seconds: "4",
      size: "720x1280",
    });

    expect(postMock).toHaveBeenCalledWith("/video-generation/test", {
      model: "agnes-video-v2.0",
      prompt: "rainy neon street",
      seconds: "4",
      size: "720x1280",
    });
  });

  test("polls video generation test task status with a short request timeout", async () => {
    const { videoGenerationApi } = await import("@/lib/http/apis/video-generation");

    getMock.mockResolvedValue({ task_id: "vid_task_1", status: "succeeded" });

    await videoGenerationApi.getTestTask("vid_task_1");

    expect(getMock).toHaveBeenCalledWith(
      "/video-generation/test/vid_task_1",
      expect.objectContaining({
        timeoutMs: 10 * 1000,
      }),
    );
  });
});
