// A delayed standalone detail response must not replace the next page.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const BASE = process.env.BASE_URL || 'http://127.0.0.1:13943';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    for (const fail of [false, true]) {
      const context = await browser.newContext();
      const page = await context.newPage();
      if (process.env.AUDIT_PUBLIC_DIR) await page.route('**/packets.js*', route => route.fulfill({ path: require('node:path').join(process.env.AUDIT_PUBLIC_DIR, 'packets.js') }));
      let release;
      const held = new Promise(resolve => { release = resolve; });
      const requested = page.waitForRequest('**/api/packets/fixture-delayed', { timeout: 10000 });
      requested.catch(() => {});
      await page.route('**/api/packets/fixture-delayed', async route => {
        await held;
        await route.fulfill({ status: fail ? 500 : 200, json: fail ? { error: 'Fixture failure' } : { packet: null } });
      });
      try {
        await page.goto(BASE + '/#/packet/fixture-delayed', { waitUntil: 'domcontentloaded' });
        await requested;
        await page.goto(BASE + '/#/nodes', { waitUntil: 'domcontentloaded' });
        await page.waitForSelector('#nodesLeft[data-loaded="true"]');
        const response = page.waitForResponse('**/api/packets/fixture-delayed');
        release();
        await (await response).finished();
        await page.waitForTimeout(150);
        assert(await page.locator('#nodesLeft[data-loaded="true"]').isVisible(), `Delayed ${fail ? 'failed' : 'successful'} packet detail replaced Nodes`);
        console.log(`PASS delayed ${fail ? 'failed' : 'successful'} packet detail navigation`);
      } finally { release(); await context.close(); }
    }
    for (const outcome of ['not-found', 'http-error', 'thrown-error']) {
      const context = await browser.newContext();
      const page = await context.newPage();
      const payload = '<img src=x onerror="window.packetDetailXSS=true">';
      let requestedPath;
      if (process.env.AUDIT_PUBLIC_DIR) await page.route('**/packets.js*', route => route.fulfill({ path: require('node:path').join(process.env.AUDIT_PUBLIC_DIR, 'packets.js') }));
      await page.route('**/api/packets/**', route => {
        requestedPath = new URL(route.request().url()).pathname;
        return route.fulfill({ status: outcome === 'http-error' ? 500 : 200, json: { packet: null } });
      });
      try {
        if (outcome === 'thrown-error') {
          await page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
          await page.evaluate(message => {
            const original = window.api;
            window.api = (path, options) => path.startsWith('/packets/') ? Promise.reject(new Error(message)) : original(path, options);
          }, payload);
        }
        await page.goto(BASE + '/#/packet/' + encodeURIComponent(payload), { waitUntil: 'domcontentloaded' });
        await page.waitForSelector('#app h2');
        assert.equal(await page.locator('#app img').count(), 0, outcome + ' must display untrusted text, not HTML');
        assert.equal(await page.evaluate(() => window.packetDetailXSS === true), false, outcome + ' must not execute markup');
        if (outcome !== 'http-error') assert((await page.locator('#app').innerText()).includes(payload), 'Untrusted text remains readable');
        if (outcome !== 'thrown-error') assert.equal(requestedPath, '/api/packets/' + encodeURIComponent(payload), 'Packet parameter must be one encoded path segment');
        console.log('PASS packet detail escapes ' + outcome);
      } finally { await context.close(); }
    }
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
