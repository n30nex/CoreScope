// Every registered page, including deep links and optional account/admin views.
// Local fixture only: all account/admin responses and writes stay in the browser.
const { chromium } = require('playwright');
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const ROOT = path.resolve(__dirname, '../..');
const BASE = process.env.BASE_URL || 'http://127.0.0.1:13943';
const OUT = process.env.AUDIT_DIR;
assert(['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname), 'Audit must use a local fixture');
if (OUT) fs.mkdirSync(OUT, { recursive: true });
const results = [];
const now = new Date().toISOString();
const user = { id: 1, email: 'admin@example.invalid', displayName: 'Fixture admin', role: 'admin', status: 'active', csrfToken: 'fixture', createdAt: now };
const member = { ...user, id: 2, email: 'member@example.invalid', displayName: 'Fixture member', role: 'user' };
const tabs = ['overview', 'rf', 'topology', 'channels', 'hashsizes', 'collisions', 'subpaths', 'nodes', 'my-repeaters', 'repeater-metrics', 'distance', 'neighbor-graph', 'rf-health', 'clock-health', 'roles', 'scopes', 'prefix-tool'];

async function installMocks(page, signedIn) {
  await page.route('**/api/**', async route => {
    const req = route.request();
    const url = new URL(req.url());
    const p = url.pathname;
    if (p === '/api/config/client') {
      const response = await route.fetch();
      const config = await response.json();
      config.userManagement = { enabled: true, channelProposals: true, notifications: true };
      return route.fulfill({ json: config });
    }
    if (p === '/api/auth/me') return route.fulfill({ status: signedIn ? 200 : 401, json: signedIn ? user : { error: 'Not logged in' } });
    if (req.method() !== 'GET') return route.fulfill({ status: 200, json: { message: 'Fixture request accepted' } });
    if (p === '/api/admin/stats') return route.fulfill({ json: { total: 2, active: 2, pending: 0, disabled: 0, admins: 1, new7d: 2, new30d: 2, active7d: 2, active30d: 2, logins24h: 2, failedLogins24h: 0, newPerDay: [], mail7d: {} } });
    if (p === '/api/admin/users') return route.fulfill({ json: [user, member] });
    if (/^\/api\/admin\/users\/\d+$/.test(p)) return route.fulfill({ json: { user: member, sessions: [], mail: [], audit: [] } });
    if (p === '/api/admin/audit') return route.fulfill({ json: { entries: [{ id: 1, at: now, action: 'login', actorUserId: 1 }], next: null } });
    if (p === '/api/admin/proposals' || p === '/api/account/proposals') return route.fulfill({ json: [] });
    if (p === '/api/account/sessions') return route.fulfill({ json: [{ id: 1, current: true, createdAt: now, lastSeenAt: now, userAgent: 'Fixture browser' }] });
    if (p === '/api/account/settings') return route.fulfill({ json: { revision: 0, generation: 1, doc: { v: 1, keys: {} } } });
    if (p === '/api/account/notifications') return route.fulfill({ json: { enabled: false, watches: [], events: [], availableEvents: ['silent', 'active'], limits: { maxWatches: 10 } } });
    if (p.startsWith('/api/account/') || p.startsWith('/api/admin/')) return route.fulfill({ json: {} });
    return route.continue();
  });
  if (process.env.AUDIT_PUBLIC_DIR) {
    await page.route(/\/(?:[^/?]+\.(?:js|css))(?:\?.*)?$/, async route => {
      const file = path.join(process.env.AUDIT_PUBLIC_DIR, path.basename(new URL(route.request().url()).pathname));
      if (fs.existsSync(file)) return route.fulfill({ path: file });
      return route.continue();
    });
  }
}

function measure() {
  const app = document.getElementById('app');
  const visible = el => {
    const r = el.getBoundingClientRect();
    if (!r.width || !r.height) return false;
    // Closed floating panels retain geometry during initialization but do not
    // paint their children. Inspect ancestor visibility as well as the control.
    for (let node = el; node && node !== app.parentElement; node = node.parentElement) {
      const style = getComputedStyle(node);
      if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0) return false;
    }
    return true;
  };
  const controls = Array.from(app.querySelectorAll('button,input,select,textarea,a[href],summary')).filter(visible);
  const clipped = controls.filter(el => {
    if (el.closest('.leaflet-container, .data-table, table, .analytics-tabs, .admin-tabs')) return false;
    const r = el.getBoundingClientRect();
    if (r.bottom < 0 || r.top > innerHeight) return false;
    let p = el.parentElement;
    while (p && p !== app) { if (['auto', 'scroll'].includes(getComputedStyle(p).overflowX)) return false; p = p.parentElement; }
    return r.left < -2 || r.right > innerWidth + 2;
  }).map(el => ({ tag: el.tagName, id: el.id, text: (el.textContent || el.getAttribute('aria-label') || '').trim().slice(0, 80), rect: { left: Math.round(el.getBoundingClientRect().left), right: Math.round(el.getBoundingClientRect().right) } }));
  return { text: app.innerText.slice(0, 700), documentWidth: document.documentElement.scrollWidth, viewport: innerWidth, controls: controls.length, clipped, heading: app.querySelector('h1,h2,h3')?.textContent || '', currentPage: typeof currentPage === 'undefined' ? null : currentPage };
}

async function exercise(page, name, key, nodeName) {
  const actions = [];
  if (name.startsWith('analytics-')) {
    assert.equal(await page.locator('.tab-btn.active').first().getAttribute('data-tab'), name.slice(10), 'Requested analytics tab must be active');
    actions.push('requested analytics tab active');
  } else if (name === 'packets') {
    await page.waitForSelector('tr[data-hash]', { timeout: 10000 });
    if (!await page.locator('#fHash').isVisible()) {
      const mobileToggle = page.locator('.filter-toggle-btn-mirror');
      if (await mobileToggle.isVisible()) await mobileToggle.click();
      else await page.locator('#filterToggleBtn').click();
    }
    await page.locator('#fHash').fill('00000000000000000000000000000000');
    await page.waitForTimeout(400);
    assert.equal(await page.locator('tr[data-hash]:visible').count(), 0, 'Hash filter must hide nonmatching packets');
    await page.locator('#fHash').fill('');
    await page.waitForSelector('tr[data-hash]');
    actions.push('populated packet table; hash filter and clear');
    if (await page.locator('[data-action="pkt-byop"]').isVisible()) {
      await page.locator('[data-action="pkt-byop"]').click();
      const modal = page.locator('.byop-modal');
      await modal.waitFor({ state: 'visible' });
      const rect = await modal.boundingBox();
      assert(rect.x >= 0 && rect.x + rect.width <= page.viewportSize().width + 2, 'Packet decoder dialog must fit');
      await page.keyboard.press('Escape');
      await modal.waitFor({ state: 'hidden' });
      actions.push('packet decoder opens, fits, and closes with Escape');
    }
  } else if (name === 'nodes') {
    await page.locator('#nodeSearch').fill(key);
    await page.waitForTimeout(250);
    assert((await page.locator('#app').innerText()).includes(nodeName), 'Node search must retain the matching node');
    await page.locator('#nodeSearch').fill('');
    actions.push('node search and clear');
  } else if (name === 'observer-detail') {
    await page.locator('#obsDaysSelect').selectOption('1');
    assert.equal(await page.locator('#obsDaysSelect').inputValue(), '1');
    actions.push('observer time range selection');
  } else if (name === 'node-analytics') {
    await page.locator('#timeRangeBtns button[data-days="1"]').click();
    await page.waitForSelector('#timeRangeBtns button[data-days="1"].active');
    actions.push('node analytics 24 hour range renders');
  } else if (name === 'path-inspector') {
    await page.locator('#path-inspector-input').fill('invalid');
    await page.locator('#path-inspector-submit').click();
    assert.match(await page.locator('#path-inspector-error').innerText(), /Invalid hex/);
    actions.push('invalid prefix feedback');
  } else if (name === 'account-forgot') {
    await page.locator('#forgotEmail').fill('fixture@example.invalid');
    await page.locator('#forgotForm button[type="submit"]').click();
    await page.waitForSelector('#mailSentHeading');
    assert(page.url().endsWith('#/account/check-mail'));
    actions.push('mocked password reset request reaches check-mail; no email sent');
  } else if (name === 'account-reset') {
    await page.locator('#resetPassword').fill('fixture-password-a');
    await page.locator('#resetPassword2').fill('fixture-password-b');
    await page.locator('#resetForm button[type="submit"]').click();
    assert.match(await page.locator('#accountMsg').innerText(), /do not match/);
    actions.push('password mismatch feedback');
  } else if (name === 'admin-user-detail') {
    await page.locator('.um-detail-close').click();
    await page.waitForFunction(() => !new URLSearchParams(location.hash.split('?')[1]).has('id'));
    actions.push('user detail close restores list URL');
  } else if (name === 'account-notifications') {
    assert(await page.locator('#notifyEnabled').count(), 'Notification settings must render');
    actions.push('notification controls rendered');
  } else if (name === 'admin-denied') {
    assert.match(await page.locator('#app').innerText(), /Admins only/);
    actions.push('unauthenticated admin access denied');
  }
  return actions;
}

async function main() {
  const [nd, ob, pk] = await Promise.all(['/api/nodes', '/api/observers', '/api/packets?limit=1&timeWindow=180'].map(p => fetch(BASE + p).then(r => r.json())));
  const key = nd.nodes[0].public_key;
  const obs = ob.observers[0].id;
  const packet = pk.packets[0];
  if (!process.env.AUDIT_MATCH || new RegExp(process.env.AUDIT_MATCH).test('packet-detail')) {
    assert(packet, 'The local fixture must contain a packet within the supported mobile 180 minute window');
  }
  const routes = [
    ['home', '#/home'], ['packets', '#/packets?timeWindow=180'], ['nodes', '#/nodes'], ['map', '#/map'], ['live', '#/live'], ['channels', '#/channels'],
    ['observers', '#/observers'], ['observer-detail', '#/observers/' + obs], ['nodes-detail', '#/nodes/' + key],
    ['node-analytics', '#/nodes/' + key + '/analytics'], ['node-reach', '#/nodes/' + key + '/reach'],
    ['tools-landing', '#/tools'], ['path-inspector', '#/tools/path-inspector'], ['traces', '#/traces'],
    ['traces-detail', '#/tools/trace/' + (packet?.hash || 'missing-fixture')],
    ['packets-detail', '#/packets/' + (packet?.hash || 'missing-fixture')],
    ['compare-selected', '#/compare?a=' + encodeURIComponent(obs) + '&b=' + encodeURIComponent(ob.observers[1].id)],
    ['compare', '#/compare'], ['cross-border', '#/cross-border'], ['rx-coverage', '#/rx-coverage'], ['audio-lab', '#/audio-lab'], ['scope-audit', '#/scope-audit'], ['perf', '#/perf'],
    ['packet-detail', '#/packet/' + (packet?.hash || 'missing-fixture')],
    ...tabs.map(t => ['analytics-' + t, '#/analytics?tab=' + t]),
    ...['login', 'register', 'activate', 'forgot', 'reset', 'confirm-email', 'check-mail', 'unsubscribe'].map(v => ['account-' + v, '#/account/' + v, false]),
    ['account-profile', '#/account', true], ['account-notifications', '#/account?section=notifications', true],
    ...['overview', 'users', 'audit', 'proposals'].map(t => ['admin-' + t, '#/admin?tab=' + t, true]),
    ['admin-user-detail', '#/admin?tab=users&id=2', true], ['admin-denied', '#/admin', false],
  ];
  const registered = fs.readdirSync(path.join(ROOT, 'public')).filter(f => f.endsWith('.js')).flatMap(f => [...fs.readFileSync(path.join(ROOT, 'public', f), 'utf8').matchAll(/registerPage\(['"]([^'"]+)/g)].map(m => m[1]));
  const modules = { 'nodes-detail': 'nodes', 'traces-detail': 'traces', 'packets-detail': 'packets', 'compare-selected': 'compare' };
  assert.deepEqual([...new Set(registered)].sort(), [...new Set(routes.map(r => r[0].startsWith('analytics-') ? 'analytics' : r[0].startsWith('account-') ? 'account' : r[0].startsWith('admin-') ? 'admin' : modules[r[0]] || r[0]))].sort(), 'Every registered page needs a route');
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    for (const width of (process.env.AUDIT_WIDTHS || '1440,375').split(',').map(Number)) {
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: 'reduce' });
      await context.addInitScript(() => localStorage.setItem('meshcore-time-window', '180'));
      for (const [name, hash, signedIn = false] of routes) {
        if (process.env.AUDIT_MATCH && !new RegExp(process.env.AUDIT_MATCH).test(name)) continue;
        const page = await context.newPage();
        page.setDefaultTimeout(10000);
        const errors = [];
        const badResponses = [];
        page.on('pageerror', e => errors.push(e.message));
        page.on('response', r => { if (r.status() >= 400 && r.url().includes('/api/') && !r.url().endsWith('/api/auth/me')) badResponses.push({ status: r.status(), path: new URL(r.url()).pathname }); });
        await installMocks(page, signedIn);
        const result = { width, name, hash, source: signedIn || name.startsWith('account') || name.startsWith('admin') ? 'synthetic account APIs; real local UI' : 'local database fixture', errors, badResponses };
        try {
          await page.goto(BASE + '/' + hash, { waitUntil: 'domcontentloaded' });
          await page.waitForFunction(() => {
            const text = document.getElementById('app')?.innerText.trim() || '';
            return text.length > 0 && !/\b(?:Loading|Calculating|Computing|Fetching)[^\n]{0,60}(?:…|\.{3})/.test(text);
          }, null, { timeout: 10000 });
          await page.evaluate(() => document.fonts.ready);
          await page.evaluate(() => Promise.all(document.getAnimations()
            .filter(animation => animation.playState === 'running' && Number.isFinite(animation.effect?.getTiming().iterations))
            .map(animation => animation.finished.catch(() => {}))));
          Object.assign(result, await page.evaluate(measure));
          result.pass = !errors.length && !badResponses.length && result.documentWidth <= width + 2 && !result.clipped.length && !/Page not yet implemented|^Loading(?:\s|…)|^Error\b/.test(result.text.trim());
          if (OUT) await page.screenshot({ path: path.join(OUT, `${width}-${name}.png`), fullPage: true });
          result.actions = await exercise(page, name, key, nd.nodes[0].name || key);
          if (result.actions.length) {
            result.after = await page.evaluate(measure);
            result.pass = result.pass && !errors.length && !badResponses.length && result.after.documentWidth <= width + 2 && !result.after.clipped.length;
            if (OUT) await page.screenshot({ path: path.join(OUT, `${width}-${name}-after.png`), fullPage: true });
          }
        } catch (e) { result.pass = false; result.error = e.message; }
        results.push(result);
        console.log(`${result.pass ? 'PASS' : 'FAIL'} ${width} ${name}: ${JSON.stringify({ errors, clipped: result.clipped, doc: result.documentWidth, text: result.text?.slice(0, 110), error: result.error })}`);
        if (OUT) fs.writeFileSync(path.join(OUT, 'page-audit.json'), JSON.stringify({ base: BASE, fixturePackets: pk.total, registered, results }, null, 2));
        await page.close();
      }
      await context.close();
    }
  } finally { await browser.close(); }
  const failures = results.filter(r => !r.pass);
  console.log(`${results.length - failures.length}/${results.length} route/viewports passed; ${pk.total} local packets.`);
  if (failures.length) process.exitCode = 1;
}
main().catch(e => { console.error(e); process.exitCode = 1; });
