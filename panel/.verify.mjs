import { chromium } from "playwright";

const BASE = "http://localhost:5173/manage/";

async function main() {
  const browser = await chromium.launch({ headless: true });

  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.goto(BASE, { waitUntil: "networkidle", timeout: 30000 });

    await page.screenshot({ path: "/tmp/verify-01-load.png", fullPage: true });
    console.log("✅ Page loaded");

    const bodyText = await page.locator("body").innerText();
    console.log(`Contains "Request Logs": ${bodyText.includes("Request Logs")}`);

    await page.screenshot({ path: "/tmp/verify-02-desktop.png" });
    console.log("✅ Desktop screenshot");

    await page.setViewportSize({ width: 390, height: 844 });
    await page.waitForTimeout(500);
    await page.screenshot({ path: "/tmp/verify-03-mobile.png", fullPage: true });
    console.log("✅ Mobile screenshot");

    await browser.close();
    console.log("\n✅ Done");
  } catch (err) {
    console.error("FAILED:", err.message);
    await browser.close();
    process.exit(1);
  }
}

main();
