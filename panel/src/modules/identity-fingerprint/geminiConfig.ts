type ConfigRecord = Record<string, unknown>;

function isRecord(value: unknown): value is ConfigRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function groupsFrom(root: ConfigRecord): ConfigRecord[] | undefined {
  const providers = root["api-keys"];
  if (!isRecord(providers) || !Object.prototype.hasOwnProperty.call(providers, "gemini")) {
    return undefined;
  }
  const groups = providers.gemini;
  if (!Array.isArray(groups) || groups.some((group) => !isRecord(group) || !Array.isArray(group.keys) || group.keys.some((key: unknown) => !isRecord(key)))) {
    throw new Error("Invalid api-keys.gemini configuration");
  }
  return groups as ConfigRecord[];
}

// 显式空 headers 覆盖分组；null 或缺省值沿用分组，与后端继承规则一致。
export function geminiFingerprintEntries(root: ConfigRecord): unknown[] {
  const groups = groupsFrom(root);
  if (groups === undefined) {
    return Array.isArray(root["gemini-api-key"]) ? root["gemini-api-key"] : [];
  }
  return groups.flatMap((group) => (group.keys as ConfigRecord[]).map((key) => ({
    ...key,
    headers: key.headers ?? group.headers,
  })));
}

// 只更新 Gemini 分组的 headers，不改密钥、模型、路由或其他 provider。
export function updateGroupedGeminiHeaders(root: ConfigRecord, headers: Record<string, string>): number | undefined {
  const groups = groupsFrom(root);
  if (groups === undefined) return undefined;
  const count = groups.reduce((total, group) => total + (group.keys as unknown[]).length, 0);
  if (count === 0) throw new Error("No Gemini API key entries found in config.yaml");
  const normalized = Object.fromEntries(Object.entries(headers)
    .map(([key, value]) => [key.trim(), value.trim()])
    .filter(([key, value]) => key !== "" && value !== ""));
  const updated = groups.map((group) => ({
    ...group,
    headers: { ...normalized },
    keys: (group.keys as ConfigRecord[]).map((key) => {
      const next = { ...key };
      // 批量应用后让各凭据继承新值，避免旧的逐密钥覆盖继续生效。
      delete next.headers;
      return next;
    }),
  }));
  root["api-keys"] = { ...(root["api-keys"] as ConfigRecord), gemini: updated };
  return count;
}
