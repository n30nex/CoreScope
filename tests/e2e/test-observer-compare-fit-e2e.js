/* Long observer option labels must not push the comparison controls off a
 * phone. Uses one local fixture observer plus synthetic comparison options. */
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
    const response = await page.request.get(BASE + '/api/observers');
    const observer = (await response.json()).observers[0];
    assert.ok(observer && observer.id, 'local fixture needs an observer');
    await page.route('**/style.css?*', route => route.fulfill({ path: path.resolve(__dirname, '../../public/style.css'), contentType: 'text/css' }));
    await page.route('**/api/observers', route => route.fulfill({ json: { observers: [observer, ...Array.from({ length: 238 }, (_, i) => ({
      id: 'sample-observer-' + i, name: 'Sample Observer ' + i + ' - Long Northern Receiving Station', iata: 'ZZZ',
    }))] } }));
    await page.goto(BASE + '/#/observers/' + encodeURIComponent(observer.id), { waitUntil: 'domcontentloaded' });
    await page.waitForFunction(() => document.querySelectorAll('#obsCompareWithPicker option').length === 239);
    await page.locator('#obsCompareWithPicker').selectOption('sample-observer-237');
    assert.equal(await page.locator('[data-action="compare-with-go"]').isEnabled(), true, 'comparison stays usable');
    await page.evaluate(() => document.fonts.ready);
    for (const width of [320, 375, 768, 1280]) {
      await page.setViewportSize({ width, height: 812 });
      const geometry = await page.locator('.compare-with-group').evaluate(group => {
        const r = group.getBoundingClientRect();
        const button = group.querySelector('button').getBoundingClientRect();
        const select = group.querySelector('select').getBoundingClientRect();
        return { pageWidth: document.documentElement.scrollWidth, left: r.left, right: r.right,
          buttonRight: button.right, selectWidth: select.width, buttonWidth: button.width };
      });
      try {
        assert.ok(geometry.pageWidth <= width, 'page widens to ' + geometry.pageWidth + 'px');
        assert.ok(geometry.left >= 0 && geometry.right <= width, 'comparison group fits');
        assert.ok(geometry.buttonRight <= geometry.right && geometry.buttonWidth >= 44 && geometry.selectWidth >= 44, 'both controls remain visible');
        console.log('PASS ' + width + 'px: populated observer picker and Compare button fit');
      } catch (err) {
        failures++;
        console.error('FAIL ' + width + 'px: ' + err.message);
      }
    }
  } finally {
    await browser.close();
  }
  process.exitCode = failures ? 1 : 0;
})().catch(err => { console.error(err); process.exitCode = 1; });
