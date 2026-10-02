import { describe, expect, it } from "vitest";
import { geminiFingerprintEntries, updateGroupedGeminiHeaders } from "../geminiConfig";

describe("Gemini fingerprint configuration layouts", () => {
  it("reads legacy entries without changing their representation", () => {
    const entries = [{ "api-key": "fixture", headers: { Agent: "legacy" } }];
    const root = { "gemini-api-key": entries };
    expect(geminiFingerprintEntries(root)).toEqual(entries);
    expect(updateGroupedGeminiHeaders(root, {})).toBeUndefined();
    expect(root["gemini-api-key"]).toEqual(entries);
  });

  it("honors grouped inheritance, null and explicit empty overrides", () => {
    const root = { "api-keys": { gemini: [{ headers: { Agent: "group" }, keys: [
      { "api-key": "inherited" }, { "api-key": "null", headers: null },
      { "api-key": "empty", headers: {} }, { "api-key": "override", headers: { Agent: "key" } },
    ] }] } };
    expect(geminiFingerprintEntries(root)).toMatchObject([
      { headers: { Agent: "group" } }, { headers: { Agent: "group" } },
      { headers: {} }, { headers: { Agent: "key" } },
    ]);
  });

  it("updates all keys while preserving grouping, credentials and other providers", () => {
    const root = { access: { "api-keys": ["client"] }, "api-keys": {
      gemini: [{ name: "group", "base-url": "https://example.invalid", models: [{ name: "model" }], headers: { Agent: "old" }, keys: [
        { "api-key": "one", weight: 3, headers: { Agent: "override" } }, { "api-key": "two", priority: 7 },
      ] }], codex: [{ keys: [{ "api-key": "other" }] }],
    } };
    const before = structuredClone(root);
    expect(updateGroupedGeminiHeaders(root, { " Agent ": " new ", "": "ignored" })).toBe(2);
    expect(geminiFingerprintEntries(root)).toMatchObject([{ headers: { Agent: "new" } }, { headers: { Agent: "new" } }]);
    expect(root.access).toEqual(before.access);
    expect(root["api-keys"].codex).toEqual(before["api-keys"].codex);
    expect(root["api-keys"].gemini[0]).toMatchObject({ name: "group", "base-url": "https://example.invalid", models: [{ name: "model" }], keys: [{ "api-key": "one", weight: 3 }, { "api-key": "two", priority: 7 }] });
    expect(root).not.toHaveProperty("gemini-api-key");
  });

  it("clears headers without accidentally restoring old group or key values", () => {
    const root = { "api-keys": { gemini: [{ headers: { Agent: "old" }, keys: [{ "api-key": "one", headers: { Agent: "key" } }] }] } };
    updateGroupedGeminiHeaders(root, {});
    expect(geminiFingerprintEntries(root)).toMatchObject([{ headers: {} }]);
  });

  it("does not fall back to shadowed legacy credentials for an explicit empty group list", () => {
    const root = { "gemini-api-key": [{ "api-key": "legacy" }], "api-keys": { gemini: [] } };
    const before = structuredClone(root);
    expect(geminiFingerprintEntries(root)).toEqual([]);
    expect(() => updateGroupedGeminiHeaders(root, {})).toThrow("No Gemini");
    expect(root).toEqual(before);
  });

  it("rejects malformed groups before writing any partial result", () => {
    for (const group of [null, {}, { keys: ["invalid"] }, { keys: null }]) {
      const root = { "api-keys": { gemini: [group] } };
      const before = structuredClone(root);
      expect(() => updateGroupedGeminiHeaders(root, { Agent: "new" })).toThrow("Invalid");
      expect(root).toEqual(before);
    }
  });
});
