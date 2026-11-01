const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const BASE = process.env.BASE_URL || 'http://127.0.0.1:13943';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture');

(async () => {
  const { observers } = await fetch(BASE + '/api/observers').then(r => r.json());
  assert(observers.length > 1, 'Two fixture observers are required');
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    for (const fail of [false, true]) {
    const page = await browser.newPage();
    if (process.env.AUDIT_PUBLIC_DIR) await page.route('**/observer-detail.js*', route => route.fulfill({ path: require('node:path').join(process.env.AUDIT_PUBLIC_DIR, 'observer-detail.js') }));
    const oldPath = '/api/observers/' + observers[0].id;
    let release;
    const held = new Promise(resolve => { release = resolve; });
    const requested = page.waitForRequest(req => new URL(req.url()).pathname === oldPath, { timeout: 10000 });
    requested.catch(() => {});
    await page.route('**' + oldPath, async route => {
      const response = await route.fetch();
      await held;
      await route.fulfill(fail ? { status: 500, json: { error: 'Fixture failure' } } : { response });
    });
    try {
      await page.goto(BASE + '/#/observers/' + observers[0].id, { waitUntil: 'domcontentloaded' });
      await requested;
      await page.goto(BASE + '/#/observers/' + observers[1].id, { waitUntil: 'domcontentloaded' });
      await page.waitForFunction(name => document.getElementById('obsTitle')?.textContent === name, observers[1].name);
      const response = page.waitForResponse(r => new URL(r.url()).pathname === oldPath);
      release();
      await (await response).finished();
      await page.waitForTimeout(200);
      assert.equal(await page.locator('#obsTitle').textContent(), observers[1].name, 'Delayed observer response must not replace the current observer');
      assert(!await page.locator('#obsDetailContent').innerText().then(t => t.includes('Error:')), 'Delayed failure must not replace the current detail');
      console.log('PASS delayed ' + (fail ? 'failed' : 'successful') + ' observer detail cannot replace another observer');
    } finally { release(); await page.close(); }
    }
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
