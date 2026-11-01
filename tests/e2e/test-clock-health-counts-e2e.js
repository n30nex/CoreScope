/* Clock filter totals must count the API's severities without reclassifying
 * unknown rows. Synthetic data keeps the No Clock regression deterministic. */
'use strict';
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture server');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  let releaseTheme;
  try {
    const page = await browser.newPage();
    const rows = ['no_clock', 'no_clock', 'ok', 'warning', 'critical', 'absurd', 'unknown'].map((severity, i) => ({
      pubkey: String(i + 1).padStart(64, '0'), nodeName: 'Clock fixture ' + i,
      severity, recentMedianSkewSec: 0, medianSkewSec: 0, driftPerDaySec: 0,
      sampleCount: 10, lastObservedTS: 1767225600,
    }));
    await page.route('**/analytics.js*', route => route.fulfill({ path: path.resolve(__dirname, '../../public/analytics.js'), contentType: 'application/javascript' }));
    await page.route('**/api/nodes/clock-skew*', route => route.fulfill({ json: rows }));
    // Deliver the initial theme only after a filter has been selected. This
    // deterministically exercises the late theme-refresh seen in normal boot.
    const themeHeld = new Promise(resolve => { releaseTheme = resolve; });
    await page.route('**/api/config/theme*', async route => {
      const response = await route.fetch();
      await themeHeld;
      await route.fulfill({ response });
    });
    await page.addInitScript(() => {
      window.clockThemeRefreshes = 0;
      window.addEventListener('theme-refresh', () => { window.clockThemeRefreshes++; });
    });
    await page.goto(BASE + '/#/analytics?tab=clock-health', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#clock-health-table tbody tr');
    for (const [severity, count] of Object.entries({ all: 7, ok: 1, warning: 1, critical: 1, absurd: 1, no_clock: 2 })) {
      assert.match(await page.locator('.clock-filter-btn[data-filter="' + severity + '"]').innerText(),
        new RegExp('\\(' + count + '\\)$'), severity + ' counter must match API rows');
    }
    await page.locator('.clock-filter-btn[data-filter="no_clock"]').click();
    const snapshot = async stage => {
      const state = await page.evaluate(() => ({
        active: document.querySelector('.clock-filter-btn.active')?.dataset.filter,
        rows: document.querySelectorAll('#clock-health-table tbody tr').length,
      }));
      console.log(stage + ': ' + JSON.stringify(state));
      return state;
    };
    assert.deepEqual(await snapshot('after selection'), { active: 'no_clock', rows: 2 });
    releaseTheme();
    await page.waitForFunction(() => window.clockThemeRefreshes > 0);
    await page.waitForSelector('#clock-health-table tbody tr');
    assert.deepEqual(await snapshot('after late initial theme'), { active: 'no_clock', rows: 2 },
      'late theme delivery must preserve the selected filter');
    assert.deepEqual(await page.locator('#clock-health-table tbody tr').evaluateAll(elements => elements.map(el => el.dataset.pubkey).sort()),
      rows.slice(0, 2).map(row => row.pubkey), 'No Clock filter selects only explicitly classified rows');
    await page.locator('.clock-filter-btn[data-filter="all"]').click();
    assert.equal(await page.locator('#clock-health-table tbody tr').count(), 7, 'unknown rows remain in All');
    console.log('PASS clock totals and No Clock filter match the API classifications');
  } finally {
    if (releaseTheme) releaseTheme();
    await browser.close();
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
