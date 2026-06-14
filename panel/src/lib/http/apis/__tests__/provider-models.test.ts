import { describe, expect, test } from "vitest";
import { normalizeModels, serializeOpenAIProvider } from "@/lib/http/apis/helpers";

describe("provider model helpers", () => {
  test("normalizes and serializes model context length", () => {
    expect(
      normalizeModels([
        { name: "glm-5.2", alias: "gpt-5.3-codex", "context-length": 1048576 },
        { name: "astron-code-latest", alias: "gpt-5.3-codex", contextLength: 220000 },
      ]),
    ).toEqual([
      { name: "glm-5.2", alias: "gpt-5.3-codex", contextLength: 1048576 },
      { name: "astron-code-latest", alias: "gpt-5.3-codex", contextLength: 220000 },
    ]);

    expect(
      serializeOpenAIProvider({
        name: "bigmodel-coding",
        models: [{ name: "glm-5.2", alias: "gpt-5.3-codex", contextLength: 1048576 }],
      }),
    ).toEqual({
      name: "bigmodel-coding",
      models: [{ name: "glm-5.2", alias: "gpt-5.3-codex", "context-length": 1048576 }],
    });
  });
});
