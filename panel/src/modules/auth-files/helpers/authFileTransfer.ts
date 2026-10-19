export function withoutAuthProxy(record: Record<string, unknown>): Record<string, unknown> {
  const result = { ...record };
  delete result.proxy_url;
  delete result["proxy-url"];
  return result;
}
