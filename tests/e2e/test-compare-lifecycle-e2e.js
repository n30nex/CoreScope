const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const path = require('node:path');
const BASE = process.env.BASE_URL || 'http://localhost:3000';
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Use a local fixture');
const observers = { observers: [{ id: 'A', name: 'Alpha' }, { id: 'B', name: 'Beta' }, { id: 'C', name: 'Gamma' }] };
const packets = { packets: [{ hash: 'aabbccdd', payload_type: 4, route_type: 1, timestamp: new Date().toISOString() }], limit: 10000, truncated: false };
async function until(fn) {
  const end = Date.now() + 5000;
  while (!fn()) { assert(Date.now() < end, 'Expected request state was not reached'); await new Promise(r => setTimeout(r, 20)); }
}
async function harness(browser, width, holdObservers = false) {
  const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
  const page = await context.newPage(); page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(30000);
  const state = { context, page, held: [], failed: [], errors: [], mode: 'hold', observerRoute: null, limited: new Set() };
  page.on('pageerror', e => state.errors.push(e.message));
  page.on('requestfailed', r => { if (/\/api\/(packets\/summary|observers)(\?|$)/.test(r.url())) state.failed.push(r.url()); });
  if (process.env.AUDIT_PUBLIC_DIR) await page.route('**/compare.js*', route => route.fulfill({ path: path.join(process.env.AUDIT_PUBLIC_DIR, 'compare.js') }));
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/observers') {
      if (holdObservers) { state.observerRoute = route; return; }
      return route.fulfill({ json: observers });
    }
    if (url.pathname === '/api/packets' && url.searchParams.has('observer')) {
      state.errors.push('Comparison must not fetch full packets');
      return route.fulfill({ status: 500, json: { error: 'Unexpected full packet request' } });
    }
    if (url.pathname === '/api/packets/summary' && url.searchParams.has('observer')) {
      if (state.mode === 'hold') { state.held.push(route); return; }
      return route.fulfill({ json: { ...packets, truncated: state.limited.has(url.searchParams.get('observer')) } });
    }
    return route.continue();
  });
  return state;
}
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const timed = await harness(browser, 375);
    try {
      await timed.page.clock.install();
      await timed.page.goto(BASE + '/#/compare?a=A&b=B', { waitUntil: 'domcontentloaded' });
      await until(() => timed.held.length === 2);
      await timed.page.clock.fastForward(30001);
      await timed.page.getByRole('alert').waitFor();
      assert.match(await timed.page.getByRole('alert').innerText(), /timed out after 30 seconds/);
      await until(() => timed.failed.length === 2);
      await timed.page.clock.fastForward(60000);
      assert.equal(timed.held.length, 2, 'No automatic expensive retries');
      assert.equal(await timed.page.locator('.compare-results').count(), 0, 'Timeout is not zero activity');
      const button = timed.page.getByRole('button', { name: 'Retry', exact: true });
      const bounds = await button.boundingBox();
      assert(bounds.x >= 0 && bounds.x + bounds.width <= 375, 'Retry is reachable on mobile');
      assert(bounds.height >= 48 && bounds.width >= 48, 'Retry uses the existing mobile touch target size');
      if (process.env.AUDIT_DIR) {
        require('node:fs').mkdirSync(process.env.AUDIT_DIR, { recursive: true });
        await timed.page.screenshot({ path: path.join(process.env.AUDIT_DIR, 'compare-timeout-375.png'), fullPage: true });
      }
      timed.mode = 'success'; await button.click();
      await timed.page.locator('.compare-results').waitFor();
      assert.match(await timed.page.locator('.compare-results').innerText(), /Alpha/);
      if (process.env.AUDIT_DIR) await timed.page.screenshot({ path: path.join(process.env.AUDIT_DIR, 'compare-retry-375.png'), fullPage: true });
      assert.deepEqual(timed.errors, []);
      console.log('PASS 30-second deadline cancels both requests; mobile Retry loads valid data');
    } finally { await timed.context.close(); }

    const changed = await harness(browser, 1440);
    try {
      await changed.page.goto(BASE + '/#/compare?a=A&b=B', { waitUntil: 'domcontentloaded' });
      await until(() => changed.held.length === 2);
      await changed.page.locator('#compareObsB').selectOption('C');
      await until(() => changed.held.length === 4 && changed.failed.length === 2);
      await Promise.all(changed.held.slice(2).map(route => route.fulfill({ json: packets })));
      await changed.page.locator('.compare-results').waitFor();
      assert.match(await changed.page.locator('.compare-results').innerText(), /Gamma/);
      await Promise.all(changed.held.slice(0, 2).map(route => route.fulfill({ json: { packets: [], limit: 10000, truncated: false } })));
      assert.match(await changed.page.locator('.compare-results').innerText(), /Gamma/);
      await changed.page.locator('#compareObsB').selectOption('B');
      await until(() => changed.held.length === 6);
      await changed.page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
      await until(() => changed.failed.length === 4);
      await Promise.all(changed.held.slice(4).map(route => route.fulfill({ json: packets })));
      assert.equal(await changed.page.locator('#compareContent').count(), 0);
      assert.deepEqual(changed.errors, []);
      console.log('PASS selection changes and route exit cancel obsolete comparisons');
    } finally { await changed.context.close(); }

    const discovery = await harness(browser, 375, true);
    try {
      await discovery.page.goto(BASE + '/#/compare?a=A&b=B', { waitUntil: 'domcontentloaded' });
      await until(() => discovery.observerRoute !== null);
      await discovery.page.goto(BASE + '/#/home', { waitUntil: 'domcontentloaded' });
      await until(() => discovery.failed.length === 1);
      await discovery.observerRoute.fulfill({ json: observers });
      assert.equal(await discovery.page.locator('#compareControls').count(), 0);
      assert.deepEqual(discovery.errors, []);
      console.log('PASS route exit cancels pending observer discovery');
    } finally { await discovery.context.close(); }

    const limited = await harness(browser, 375);
    try {
      limited.mode = 'success'; limited.limited.add('A');
      await limited.page.goto(BASE + '/#/compare?a=A&b=B', { waitUntil: 'domcontentloaded' });
      const note = limited.page.getByRole('note');
      await note.waitFor();
      assert.match(await note.innerText(), /Alpha: limited to 10,000 packets/);
      assert(!(await note.innerText()).includes('Beta: limited'));
      assert.match(await note.innerText(), /Full-period totals are unknown/);
      assert(!(await limited.page.locator('#compareDetail').innerText()).includes('Perfect overlap'));
      limited.limited.clear(); limited.limited.add('B');
      await limited.page.locator('#compareObsA').selectOption('C');
      await limited.page.waitForFunction(() => document.querySelector('[role="note"]')?.textContent.includes('Beta: limited'));
      assert(!(await note.innerText()).includes('Alpha'));
      assert.deepEqual(limited.errors, []);
      if (process.env.AUDIT_DIR) await limited.page.screenshot({ path: path.join(process.env.AUDIT_DIR, 'compare-limited-sample-375.png'), fullPage: true });
      console.log('PASS limited A/B samples are labelled without full-period totals or overlap claims');
    } finally { await limited.context.close(); }
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
