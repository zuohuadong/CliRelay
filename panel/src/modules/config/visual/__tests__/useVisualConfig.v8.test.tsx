import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { parse, stringify } from "yaml";
import { useVisualConfig } from "../useVisualConfig";

const upstream = {
  codex: [{ name: "upstream", "base-url": "https://example.invalid", keys: [{ "api-key": "fixture-upstream" }] }],
};

const v8 = stringify({
  "config-version": 8,
  server: { host: "127.0.0.1", port: 8317, "commercial-mode": true, tls: { enable: true, cert: "cert.pem", key: "key.pem" } },
  management: { "allow-remote": true, "secret-key": "fixture-admin", "disable-control-panel": false },
  access: { "api-keys": ["fixture-client"] },
  "api-keys": upstream,
  oauth: { "auth-dir": "./auth", providers: { aistudio: { "ws-auth": true }, codex: { "identity-confuse": true } } },
  requests: { "proxy-url": "http://127.0.0.1:8080", streaming: { "keepalive-seconds": 10, "bootstrap-retries": 2 }, "nonstream-keepalive-interval": 5 },
  routing: { strategy: "fill-first", "force-model-prefix": true, retry: { "request-retry": 3, "max-retry-interval": 30, "max-retry-credentials": 7 } },
  observability: { logs: { debug: true, "logging-to-file": true, "logs-max-total-size-mb": 100 }, usage: { "usage-statistics-enabled": true } },
  plugins: { items: { example: { opaque: [1, 2, 3] } } },
  "egress-network": { enabled: true },
});

function load(raw: string) {
  const hook = renderHook(() => useVisualConfig());
  act(() => hook.result.current.loadVisualValuesFromYaml(raw));
  return hook;
}

describe("v8 visual configuration", () => {
  it("reads canonical server, management, access, OAuth and request settings", () => {
    const { result } = load(v8);
    expect(result.current.visualValues).toMatchObject({
      host: "127.0.0.1", port: "8317", commercialMode: true,
      tlsEnable: true, tlsCert: "cert.pem", tlsKey: "key.pem",
      rmAllowRemote: true, rmSecretKey: "fixture-admin",
      authDir: "./auth", apiKeysText: "fixture-client", wsAuth: true,
      proxyUrl: "http://127.0.0.1:8080", forceModelPrefix: true,
      requestRetry: "3", maxRetryInterval: "30", debug: true,
      loggingToFile: true, logsMaxTotalSizeMb: "100", usageStatisticsEnabled: true,
      streaming: { keepaliveSeconds: "10", bootstrapRetries: "2", nonstreamKeepaliveInterval: "5" },
    });
    expect(result.current.visualDirty).toBe(false);
  });

  it("preserves upstream groups and unrelated settings when saving a visual edit", () => {
    const { result } = load(v8);
    act(() => result.current.setVisualValues({ port: "8318", apiKeysText: "new-client", debug: false, requestRetry: "0" }));
    const saved = parse(result.current.applyVisualChangesToYaml(v8));
    expect(saved["api-keys"]).toEqual(upstream);
    expect(saved.access["api-keys"]).toEqual(["new-client"]);
    expect(saved.server.port).toBe(8318);
    expect(saved.observability.logs.debug).toBe(false);
    expect(saved.routing.retry).toEqual({ "request-retry": 0, "max-retry-interval": 30, "max-retry-credentials": 7 });
    expect(saved.plugins).toEqual(parse(v8).plugins);
    expect(saved["egress-network"]).toEqual(parse(v8)["egress-network"]);
    expect(saved.oauth.providers.codex).toEqual(parse(v8).oauth.providers.codex);
    for (const legacy of ["host", "port", "tls", "remote-management", "auth-dir", "debug", "request-retry", "proxy-url", "streaming", "ws-auth"]) {
      expect(saved).not.toHaveProperty(legacy);
    }
  });

  it("keeps upstream groups when no client access keys are configured", () => {
    const raw = stringify({ "api-keys": upstream });
    const { result } = load(raw);
    expect(result.current.visualValues.apiKeysText).toBe("");
    expect(parse(result.current.applyVisualChangesToYaml(raw))["api-keys"]).toEqual(upstream);
  });

  it("prefers explicit v8 false, zero and empty client keys over legacy values", () => {
    const raw = stringify({ "api-keys": ["legacy-client"], "request-retry": 9, debug: true, access: { "api-keys": [] }, routing: { retry: { "request-retry": 0 } }, observability: { logs: { debug: false } } });
    const { result } = load(raw);
    expect(result.current.visualValues).toMatchObject({ apiKeysText: "", requestRetry: "0", debug: false });
    const saved = parse(result.current.applyVisualChangesToYaml(raw));
    expect(saved["api-keys"]).toBeUndefined();
    expect(saved["request-retry"]).toBeUndefined();
    expect(saved.routing.retry["request-retry"]).toBe(0);
  });

  it("merges partially migrated TLS fields without losing legacy siblings", () => {
    const raw = stringify({ tls: { enable: true, cert: "legacy-cert", key: "legacy-key" }, server: { tls: { cert: "v8-cert" } } });
    const { result } = load(raw);
    expect(result.current.visualValues).toMatchObject({ tlsEnable: true, tlsCert: "v8-cert", tlsKey: "legacy-key" });
    const saved = parse(result.current.applyVisualChangesToYaml(raw));
    expect(saved.server.tls).toEqual({ enable: true, cert: "v8-cert", key: "legacy-key" });
  });

  it("continues editing legacy layout without silently migrating it", () => {
    const raw = stringify({ host: "localhost", port: 8317, "api-keys": ["legacy-client"], "request-retry": 2 });
    const { result } = load(raw);
    act(() => result.current.setVisualValues({ port: "8318" }));
    const saved = parse(result.current.applyVisualChangesToYaml(raw));
    expect(saved.port).toBe(8318);
    expect(saved["api-keys"]).toEqual(["legacy-client"]);
    expect(saved["request-retry"]).toBe(2);
    expect(saved.server).toBeUndefined();
  });
});
