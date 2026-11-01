/* Expanded packet filters must stay inside the mobile viewport. Run against
 * a localhost fixture server; serve this checkout's CSS for candidate checks. */
'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname),
  'Use a local fixture server, never production');

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
  });
  let failures = 0;
  try {
    const page = await browser.newPage();
    await page.route('**/style.css?*', route => route.fulfill({
      path: path.resolve(__dirname, '../../public/style.css'),
      contentType: 'text/css',
    }));
    for (const width of [320, 360, 390, 600, 767, 1280]) {
      await page.setViewportSize({ width, height: 800 });
      await page.goto(BASE + '/#/packets', { waitUntil: 'domcontentloaded' });
      await page.locator('#fChannel').waitFor({ state: 'attached' });
      if (width < 768 && !await page.locator('.filter-bar').evaluate(el => el.classList.contains('filters-expanded'))) {
        await page.locator('.filter-toggle-btn-mirror:visible, .filter-toggle-btn:visible').first().click();
        await page.waitForFunction(() => document.querySelector('.filter-bar.filters-expanded'));
      }
      await page.evaluate(() => document.fonts.ready);
      const outside = await page.locator('.filter-bar input, .filter-bar select, .filter-bar button')
        .evaluateAll(controls => controls.flatMap(el => {
          const r = el.getBoundingClientRect();
          if (!r.width || !r.height || (r.left >= -1 && r.right <= innerWidth + 1)) return [];
          return [{ id: el.id, left: r.left, right: r.right }];
        }));
      try {
        assert.deepEqual(outside, [], width + 'px: packet filters extend outside the viewport');
        console.log('PASS ' + width + 'px: all expanded packet controls fit');
      } catch (err) {
        failures++;
        console.error('FAIL ' + err.message);
      }
    }
  } finally {
    await browser.close();
  }
  process.exitCode = failures ? 1 : 0;
})().catch(err => { console.error(err); process.exitCode = 1; });
