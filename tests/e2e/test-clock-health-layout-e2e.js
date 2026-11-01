// Clock Health must retain its scroll container on late renders, filters and resize.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const BASE = process.env.BASE_URL || 'http://localhost:3000';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  let release;
  try {
    const page = await browser.newPage({ viewport: { width: 375, height: 900 }, reducedMotion: 'reduce' });
    if (process.env.AUDIT_PUBLIC_DIR) await page.route('**/analytics.js*', route => route.fulfill({ path: path.join(process.env.AUDIT_PUBLIC_DIR, 'analytics.js') }));
    await page.addInitScript(() => {
      window.clockLayoutOverflows = [];
      new MutationObserver(() => {
        if (!document.querySelector('#clock-health-table tbody tr')) return;
        const width = document.documentElement.scrollWidth;
        if (width > innerWidth + 2 && window.clockLayoutOverflows.length < 10) window.clockLayoutOverflows.push({ width, viewport: innerWidth });
      }).observe(document, { childList: true, subtree: true });
    });
    const held = new Promise(resolve => { release = resolve; });
    const requested = page.waitForRequest('**/api/nodes/clock-skew*');
    await page.route('**/api/nodes/clock-skew*', async route => {
      const response = await route.fetch();
      await held;
      await route.fulfill({ response });
    });
    await page.goto(BASE + '/#/analytics?tab=clock-health', { waitUntil: 'domcontentloaded' });
    await requested;
    assert(await page.getByText('Loading clock health data…').isVisible(), 'Delayed data keeps the loading state visible');
    release();
    await page.waitForSelector('#clock-health-table tbody tr');
    await page.evaluate(() => document.fonts.ready);
    const settle = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    async function fits(label) {
      await settle();
      const state = await page.evaluate(() => {
        const table = document.getElementById('clock-health-table');
        const wrap = table.parentElement;
        wrap.scrollLeft = wrap.scrollWidth;
        const last = table.querySelector('thead th:last-child').getBoundingClientRect();
        return { width: document.documentElement.scrollWidth, viewport: innerWidth, wrapped: wrap.classList.contains('analytics-table-scroll'), lastRight: last.right, wrapRight: wrap.getBoundingClientRect().right };
      });
      assert(state.wrapped, label + ': scroll container must survive rendering');
      assert(state.width <= state.viewport + 2, label + ': document must fit the viewport');
      assert(state.lastRight <= state.wrapRight + 2, label + ': last column must remain reachable by scrolling');
      console.log('PASS ' + label);
    }
    await fits('delayed initial render');
    await page.locator('.clock-filter-btn[data-filter="no_clock"]').click();
    await fits('severity filter render');
    await page.locator('#clock-health-table th[data-sort-col="name"]').click();
    await fits('header sort render');
    await page.setViewportSize({ width: 1440, height: 900 });
    await fits('desktop resize');
    await page.setViewportSize({ width: 375, height: 900 });
    await fits('mobile resize');
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('theme-refresh')));
    await page.waitForSelector('#clock-health-table tbody tr');
    await fits('late theme render');
    assert.deepEqual(await page.evaluate(() => window.clockLayoutOverflows), [], 'Table insertion must not briefly widen the page before the next frame');
  } finally { if (release) release(); await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
