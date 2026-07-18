import { beforeEach, describe, expect, test, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getAgnesProviders: vi.fn(),
}));

vi.mock("@/lib/http/apis", () => ({
  authFilesApi: {
    list: vi.fn(async () => ({ files: [] })),
    getModelDefinitions: vi.fn(async () => []),
    getModelsForAuthFile: vi.fn(async () => []),
  },
  providersApi: {
    getGeminiKeys: vi.fn(async () => []),
    getClaudeConfigs: vi.fn(async () => []),
    getCodexConfigs: vi.fn(async () => []),
    getOpenCodeGoConfigs: vi.fn(async () => []),
    getVertexConfigs: vi.fn(async () => []),
    getOpenAIProviders: vi.fn(async () => []),
    getBigModelCodingProviders: vi.fn(async () => []),
    getAstronCodeProviders: vi.fn(async () => []),
    getAgnesProviders: mocks.getAgnesProviders,
  },
}));

vi.mock("@/lib/http/client", () => ({
  apiClient: {
    get: vi.fn(async () => null),
  },
}));

describe("configured model availability for Agnes", () => {
  beforeEach(() => {
    mocks.getAgnesProviders.mockReset();
    mocks.getAgnesProviders.mockResolvedValue([
      {
        name: "agnes",
        apiKeyEntries: [{ apiKey: "sk-agnes" }],
        models: [
          { name: "agnes-2.0-flash" },
          { name: "agnes-image-2.1-flash", image: true },
          { name: "agnes-video-v2.0", video: true },
        ],
      },
    ]);
  });

  test("includes chat, image, and video models from the dedicated provider", async () => {
    const { loadConfiguredModelAvailability } = await import(
      "@/modules/models/modelAvailability"
    );

    const availability = await loadConfiguredModelAvailability();

    expect(availability.scoped).toBe(true);
    expect(Array.from(availability.idSet)).toEqual(
      expect.arrayContaining([
        "agnes-2.0-flash",
        "agnes-image-2.1-flash",
        "agnes-video-v2.0",
      ]),
    );
    expect(mocks.getAgnesProviders).toHaveBeenCalledTimes(1);
  });
});
