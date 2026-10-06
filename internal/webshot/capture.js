// capture.js — the Playwright half of internal/webshot. The Go side (webshot.go)
// embeds this file, writes it to a temp dir, and runs it with `node`, passing all
// parameters through the environment so there is no arg-parsing to keep in sync.
//
// It launches the ALREADY-INSTALLED chromium via executablePath
// (PLAYWRIGHT_EXECUTABLE, resolved on the Go side), so it never triggers a browser
// download — the point of the whole approach on a box whose playwright browser is a
// custom revision and whose arch has no driver on the retired CDN (decision #23).
//
// Env contract:
//   WEBSHOT_URL            (required) page to screenshot
//   WEBSHOT_OUT            (required) PNG output path
//   WEBSHOT_WIDTH/HEIGHT   viewport (default 1280x800)
//   WEBSHOT_FULL           "1" → full-page screenshot
//   WEBSHOT_WAIT_SELECTOR  wait for this selector before shooting
//   WEBSHOT_SETTLE_MS      extra settle after load/selector (default 600)
//   PLAYWRIGHT_EXECUTABLE  chromium binary to launch
const { chromium } = require('playwright');

(async () => {
  const url = process.env.WEBSHOT_URL;
  const out = process.env.WEBSHOT_OUT;
  if (!url || !out) {
    console.error('webshot: WEBSHOT_URL and WEBSHOT_OUT are required');
    process.exit(2);
  }
  const width = parseInt(process.env.WEBSHOT_WIDTH || '1280', 10);
  const height = parseInt(process.env.WEBSHOT_HEIGHT || '800', 10);
  const full = process.env.WEBSHOT_FULL === '1';
  const waitSel = process.env.WEBSHOT_WAIT_SELECTOR || '';
  const settle = parseInt(process.env.WEBSHOT_SETTLE_MS || '600', 10);
  const exe = process.env.PLAYWRIGHT_EXECUTABLE || undefined;

  const browser = await chromium.launch({
    executablePath: exe,
    args: ['--no-sandbox', '--disable-gpu', '--hide-scrollbars'],
  });
  try {
    const page = await browser.newPage({ viewport: { width, height } });
    const resp = await page.goto(url, { waitUntil: 'load', timeout: 30000 });
    if (resp && resp.status() >= 400) {
      console.error('webshot: navigation to', url, 'returned', resp.status());
      process.exit(3);
    }
    if (waitSel) {
      await page.waitForSelector(waitSel, { timeout: 15000 });
    }
    // The board embeds the report in a loading="lazy" iframe; scroll it into view to
    // trigger the load and wait for every frame to finish, so the screenshot shows
    // the report in context rather than a blank frame. All bounded + best-effort — a
    // slow/absent frame must never fail the capture.
    for (const fh of await page.$$('iframe')) {
      try {
        await fh.scrollIntoViewIfNeeded({ timeout: 3000 });
      } catch (_) {}
    }
    for (const fr of page.frames()) {
      try {
        await fr.waitForLoadState('load', { timeout: 8000 });
      } catch (_) {}
    }
    if (settle > 0) {
      await page.waitForTimeout(settle);
    }
    await page.screenshot({ path: out, fullPage: full });
    console.log('webshot: wrote', out);
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error('webshot:', (e && e.message) || e);
  process.exit(1);
});
