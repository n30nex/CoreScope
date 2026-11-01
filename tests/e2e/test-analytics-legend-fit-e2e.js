/* The real Channels renderer must keep every legend entry inside its card,
 * including the overflow count when more than eight channels are active. */
'use strict';
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture server');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  let failures = 0;
  try {
    const page = await browser.newPage();
    await page.route('**/style.css?*', route => route.fulfill({ path: path.resolve(__dirname, '../../public/style.css'), contentType: 'text/css' }));
    await page.route('**/api/analytics/channels*', route => route.fulfill({ json: {
      channels: [], activeChannels: 20, decryptable: 0, topSenders: [], msgLengths: [],
      channelTimeline: Array.from({ length: 20 }, (_, i) => ({ hour: '2026-01-01T12', channel: '#channel-' + i, count: i + 1 })),
    } }));
    await page.goto(BASE + '/#/analytics?tab=channels', { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.querySelectorAll('.timeline-legend > span').length === 9);
    await page.evaluate(() => document.fonts.ready);
    for (const width of [320, 375, 768, 1280]) {
      await page.setViewportSize({ width, height: 800 });
      const outside = await page.locator('.timeline-legend').evaluate(legend => {
        const r = legend.getBoundingClientRect();
        return [...legend.children].filter(entry => {
          const e = entry.getBoundingClientRect();
          return e.left < r.left - 1 || e.right > r.right + 1;
        }).map(entry => entry.textContent);
      });
      try {
        assert.deepEqual(outside, [], width + 'px: channel legend overflows its card');
        console.log('PASS ' + width + 'px: all eight channels and overflow count fit');
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
