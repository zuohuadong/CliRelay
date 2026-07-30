import { describe, expect, test } from "vitest";
import { normalizeOauthModelAlias, serializeOauthModelAlias } from "@/lib/http/apis/helpers";

describe("OAuth model alias helpers", () => {
  test("round-trips display name and force mapping fields", () => {
    const normalized = normalizeOauthModelAlias({
      "oauth-model-alias": {
        codex: [
          {
            name: "gpt-5.3-codex-spark",
            alias: "gpt-5.5",
            "display-name": "GPT 5.5",
            "force-mapping": true,
            fork: true,
          },
        ],
      },
    });

    expect(normalized).toEqual({
      codex: [
        {
          name: "gpt-5.3-codex-spark",
          alias: "gpt-5.5",
          displayName: "GPT 5.5",
          forceMapping: true,
          fork: true,
        },
      ],
    });
    expect(serializeOauthModelAlias(normalized.codex)).toEqual([
      {
        name: "gpt-5.3-codex-spark",
        alias: "gpt-5.5",
        "display-name": "GPT 5.5",
        "force-mapping": true,
        fork: true,
      },
    ]);
  });
});
