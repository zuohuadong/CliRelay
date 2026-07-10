import { beforeEach, describe, expect, test, vi } from "vitest";

const getMock = vi.fn();
const postMock = vi.fn();

vi.mock("@/lib/http/client", () => ({
  apiClient: {
    get: getMock,
    post: postMock,
  },
}));

describe("OAuth egress serialization", () => {
  beforeEach(() => {
    getMock.mockReset();
    postMock.mockReset();
  });

  test("requires and passes egress_id for Codex authorization", async () => {
    const { oauthApi } = await import("@/lib/http/apis/oauth");
    getMock.mockResolvedValue({ url: "https://auth.example", state: "state-1" });

    expect(() => oauthApi.startAuth("codex")).toThrow("egress endpoint");
    await oauthApi.startAuth("codex", { egressId: "hk" });

    expect(getMock).toHaveBeenCalledWith("/codex-auth-url", {
      params: { is_webui: true, egress_id: "hk" },
    });
  });

  test("passes egress_id for Codex callback only", async () => {
    const { oauthApi } = await import("@/lib/http/apis/oauth");
    postMock.mockResolvedValue({ status: "ok" });

    await oauthApi.submitCallback("codex", "https://callback.example", { egressId: "hk" });
    await oauthApi.submitCallback("xai", "https://callback.example", { egressId: "ignored" });

    expect(postMock).toHaveBeenNthCalledWith(1, "/oauth-callback", {
      provider: "codex",
      redirect_url: "https://callback.example",
      egress_id: "hk",
    });
    expect(postMock).toHaveBeenNthCalledWith(2, "/oauth-callback", {
      provider: "xai",
      redirect_url: "https://callback.example",
    });
  });

  test("does not force egress on other OAuth providers", async () => {
    const { oauthApi } = await import("@/lib/http/apis/oauth");
    getMock.mockResolvedValue({ url: "https://auth.example", state: "state-xai" });
    postMock.mockResolvedValue({ url: "https://auth.example", state: "state-iflow" });

    await oauthApi.startAuth("xai");
    await oauthApi.startAuth("qwen");
    await oauthApi.startAuth("iflow");
    await oauthApi.iflowCookieAuth("cookie=value");

    expect(getMock).toHaveBeenNthCalledWith(1, "/xai-auth-url", {
      params: { is_webui: true },
    });
    expect(getMock).toHaveBeenNthCalledWith(2, "/qwen-auth-url", { params: {} });
    expect(postMock).toHaveBeenNthCalledWith(1, "/iflow-auth-url", undefined, {
      params: { is_webui: true },
    });
    expect(postMock).toHaveBeenNthCalledWith(2, "/iflow-auth-url", { cookie: "cookie=value" });
  });
});
