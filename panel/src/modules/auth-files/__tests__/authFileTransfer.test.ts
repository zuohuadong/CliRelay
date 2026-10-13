import { describe, expect, test } from "vitest";
import { withoutAuthProxy } from "../helpers/authFileTransfer";

describe("auth file proxy omission", () => {
  test("removes canonical and legacy proxy fields without modifying credentials or source", () => {
    const record = {
      type: "codex",
      access_token: "access",
      refresh_token: "refresh",
      account_id: "account",
      disabled: true,
      proxy_url: "socks5://private:1080",
      "proxy-url": "http://legacy:8080",
    };
    expect(withoutAuthProxy(record)).toEqual({
      type: "codex",
      access_token: "access",
      refresh_token: "refresh",
      account_id: "account",
      disabled: true,
    });
    expect(record.proxy_url).toBe("socks5://private:1080");
    expect(record["proxy-url"]).toBe("http://legacy:8080");
  });

  test("preserves unrelated routing and nested provider data", () => {
    const record = { type: "codex", prefix: "team", custom: { proxy_url: "unrelated" } };
    expect(withoutAuthProxy(record)).toEqual(record);
  });
});
