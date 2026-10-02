type ConfigRecord = Record<string, unknown>;

// 只转换可视化编辑器拥有的字段；上游凭据分组和扩展配置不参与转换。
const paths = [
  ["host", "server.host"],
  ["port", "server.port"],
  ["tls", "server.tls"],
  ["commercial-mode", "server.commercial-mode"],
  ["remote-management", "management"],
  ["auth-dir", "oauth.auth-dir"],
  ["api-keys", "access.api-keys"],
  ["debug", "observability.logs.debug"],
  ["logging-to-file", "observability.logs.logging-to-file"],
  ["logs-max-total-size-mb", "observability.logs.logs-max-total-size-mb"],
  ["usage-statistics-enabled", "observability.usage.usage-statistics-enabled"],
  ["proxy-url", "requests.proxy-url"],
  ["force-model-prefix", "routing.force-model-prefix"],
  ["request-retry", "routing.retry.request-retry"],
  ["max-retry-interval", "routing.retry.max-retry-interval"],
  ["ws-auth", "oauth.providers.aistudio.ws-auth"],
  ["streaming", "requests.streaming"],
  ["nonstream-keepalive-interval", "requests.nonstream-keepalive-interval"],
  ["payload", "requests.payload"],
] as const;

const structFields = new Set(["tls", "remote-management", "streaming", "payload"]);
const v8Roots = ["server", "management", "access", "oauth", "requests", "observability"];

function isRecord(value: unknown): value is ConfigRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function owns(value: ConfigRecord, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key);
}

function readPath(root: ConfigRecord, path: string): { present: boolean; value: unknown } {
  let value: unknown = root;
  for (const key of path.split(".")) {
    if (!isRecord(value) || !owns(value, key)) return { present: false, value: undefined };
    value = value[key];
  }
  return { present: true, value };
}

function usesV8(root: ConfigRecord): boolean {
  return root["config-version"] === 8 || isRecord(root["api-keys"]) ||
    v8Roots.some((key) => owns(root, key)) ||
    paths.some(([, path]) => readPath(root, path).present);
}

function writePath(root: ConfigRecord, path: string, value: unknown, present: boolean): void {
  const keys = path.split(".");
  let parent = root;
  for (const key of keys.slice(0, -1)) {
    if (!isRecord(parent[key])) {
      if (!present) return;
      parent[key] = {};
    }
    parent = parent[key] as ConfigRecord;
  }
  const key = keys[keys.length - 1];
  if (present) parent[key] = value;
  else delete parent[key];
}

// 用兼容视图复用已有表单；按照字段是否存在，而非 truthy 值，确定 v8 优先级。
export function projectVisualConfig(root: ConfigRecord): ConfigRecord {
  const projected = structuredClone(root);
  if (isRecord(root["api-keys"])) delete projected["api-keys"];
  for (const [legacy, canonical] of paths) {
    const current = readPath(root, canonical);
    if (!current.present) continue;
    if (structFields.has(legacy)) {
      if (isRecord(current.value)) {
        const previous = isRecord(projected[legacy]) ? projected[legacy] : {};
        projected[legacy] = { ...previous, ...structuredClone(current.value) };
      }
      // null/空结构没有叶子字段，不覆盖尚未迁移的旧字段。
      continue;
    }
    projected[legacy] = structuredClone(current.value);
  }
  return projected;
}

// 保存回原有布局，不把客户端密钥写到 v8 上游分组占用的 api-keys 根节点。
export function restoreVisualConfig(original: ConfigRecord, edited: ConfigRecord): ConfigRecord {
  if (!usesV8(original)) return edited;
  const restored = structuredClone(edited);
  for (const [legacy, canonical] of paths) {
    const present = owns(edited, legacy);
    delete restored[legacy];
    writePath(restored, canonical, present ? structuredClone(edited[legacy]) : undefined, present);
  }
  if (isRecord(original["api-keys"])) {
    restored["api-keys"] = structuredClone(original["api-keys"]);
  }
  return restored;
}
