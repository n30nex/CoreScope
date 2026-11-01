// Exercise the shared field breakdown in standalone, desktop-pane and mobile-sheet details.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const BASE = process.env.BASE_URL || 'http://localhost:3000';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture');
const encryptedData = 'ab'.repeat(64);
// Same decoded shape as a direct REQ from the local fixture, with synthetic bytes.
const packet = {
  id: 90001, hash: 'a1b2c3d4e5f60708', route_type: 2, payload_type: 0,
  raw_hex: '0200aabb0000' + encryptedData, path_json: '[]',
  decoded_json: JSON.stringify({ type: 'REQ', destHash: 'aa', srcHash: 'bb', mac: '0000', encryptedData }),
  timestamp: new Date().toISOString(), observer_id: 'fixture-observer', snr: 6, rssi: -80,
};

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    for (const width of [320, 375, 1440]) {
      for (const routeName of ['packet', 'packets']) {
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
        const page = await context.newPage();
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        if (process.env.AUDIT_PUBLIC_DIR) await page.route('**/packets.js*', route => route.fulfill({ path: path.join(process.env.AUDIT_PUBLIC_DIR, 'packets.js') }));
        await page.route('**/api/**', route => {
          const pathname = new URL(route.request().url()).pathname;
          if (pathname === '/api/packets') return route.fulfill({ json: { packets: [packet], total: 1, limit: 1000, offset: 0 } });
          if (pathname === '/api/packets/' + packet.hash) return route.fulfill({ json: { packet, observations: [] } });
          return route.continue();
        });
        try {
          await page.goto(BASE + '/#/' + routeName + '/' + packet.hash, { waitUntil: 'domcontentloaded' });
          const technical = page.locator('.detail-technical:visible');
          await technical.waitFor();
          if (await technical.getAttribute('open') === null) await technical.locator('summary').click();
          const table = page.locator('.field-table:visible');
          await table.waitFor();
          await page.evaluate(() => document.fonts.ready);
          await page.evaluate(() => Promise.all(document.getAnimations().filter(a => a.playState === 'running' && Number.isFinite(a.effect?.getTiming().iterations)).map(a => a.finished.catch(() => {}))));
          assert.match(await table.innerText(), /Encrypted Data/);
          const state = await table.evaluate(table => {
            const wrap = table.parentElement;
            const panel = table.closest('.mobile-sheet-content, #pktRight, .slide-over-panel') || document.getElementById('app');
            wrap.scrollLeft = wrap.scrollWidth;
            const last = table.querySelector('thead th:last-child').getBoundingClientRect();
            const bounds = wrap.getBoundingClientRect();
            return {
              documentWidth: document.documentElement.scrollWidth, viewport: innerWidth,
              wrapped: wrap.classList.contains('table-scroll-wrap'), overflow: getComputedStyle(wrap).overflowX,
              panelWidth: panel.clientWidth, panelScrollWidth: panel.scrollWidth,
              wrapWidth: wrap.clientWidth, wrapScrollWidth: wrap.scrollWidth,
              lastRight: last.right, wrapRight: bounds.right,
              values: [...table.querySelectorAll('th')].map(cell => cell.textContent),
            };
          });
          assert.deepEqual(errors, [], 'No page errors');
          assert(state.documentWidth <= width + 2, `${routeName} at ${width}px overflowed document to ${state.documentWidth}px`);
          assert(state.panelScrollWidth <= state.panelWidth + 2, `${routeName} detail container must fit its width`);
          assert(state.wrapped && state.overflow === 'auto', 'Field table needs its own scroll container');
          assert.deepEqual(state.values, ['Offset', 'Field', 'Value', 'Description'], 'Keep all field columns');
          assert(state.lastRight <= state.wrapRight + 2, 'Description column must be reachable by scrolling');
          if (process.env.AUDIT_DIR) {
            require('node:fs').mkdirSync(process.env.AUDIT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.AUDIT_DIR, `${width}-${routeName}-field-table.png`), fullPage: true });
          }
          console.log(`PASS ${routeName} ${width}px: document ${state.documentWidth}px; field table ${state.wrapWidth}/${state.wrapScrollWidth}px`);
        } finally { await context.close(); }
      }
    }
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
