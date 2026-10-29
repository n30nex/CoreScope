/* Browser acceptance with synthetic public cross-border telemetry.
 * Default: fixture shell/assets with the existing pinned Chart.js script. BASE_URL: real loopback server, only the
 * cross-border endpoint mocked, with page-only Istanbul coverage snapshots.
 * CROSS_BORDER_ARTIFACT_DIR optionally saves desktop/mobile screenshots.
 */
'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { randomUUID } = require('crypto');
const waitForPending = require('./cross-border-wait');
const ROOT = path.resolve(__dirname, '../..');
const PUBLIC = process.env.CROSS_BORDER_PUBLIC_DIR || path.join(ROOT, 'public');
const SAMPLE = JSON.parse(fs.readFileSync(path.join(ROOT, 'test-fixtures/cross-border.json'), 'utf8'));
const SERVED = Boolean(process.env.BASE_URL);
function loopbackOrigin(value) {
  const message = 'BASE_URL must be an HTTP(S) loopback origin (localhost, 127.0.0.1 or [::1]).';
  let url;
  try { url = new URL(value); } catch (_) { throw new Error(message); }
  if (!['http:', 'https:'].includes(url.protocol) || !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname) ||
      url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error(message);
  return url.origin;
}
const BASE = SERVED ? loopbackOrigin(process.env.BASE_URL) : 'http://127.0.0.1:13589';
// Validate the target before loading or launching any browser dependency.
const { chromium } = require('playwright');
const SHELL_SCRIPTS = new Set(['payload-labels.js', 'roles.js', 'cb-presets.js', 'customize-v2.js',
  'region-filter.js', 'area-filter.js', 'app.js', 'url-state.js', 'bottom-nav.js', 'nav-drawer.js', 'cross-border-dashboard.js', 'cross-border.js']);
const clone = value => JSON.parse(JSON.stringify(value));

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  const context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, serviceWorkers: 'block' });
  let calls = [], pending = [], heldWindow = null, mode = 'normal';
  let passed = 0, failed = 0, coverageFiles = 0;
  const errors = [];
  try {
    await context.addInitScript(offline => {
      class OfflineSocket extends EventTarget {
        constructor() { super(); this.readyState = 1; }
        close() { this.readyState = 3; }
        send() {}
      }
      Object.assign(OfflineSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
      if (offline) window.WebSocket = OfflineSocket;
      const originalTimer = window.setTimeout.bind(window);
      window.__borderMinuteCallbacks = [];
      window.setTimeout = function (fn, delay, ...args) { if (delay === 60000) window.__borderMinuteCallbacks.push(fn); return originalTimer(fn, delay, ...args); };
      window.__borderHidden = false;
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => window.__borderHidden });
      const originalFetch = window.fetch.bind(window);
      window.__borderSignals = [];
      window.fetch = function (url, opts) {
        if (String(url).startsWith('/api/cross-border')) {
          window.__borderSignals.push(opts.signal);
          if (window.__ignoreBorderAbort) opts = Object.assign({}, opts, { signal: undefined });
        }
        return originalFetch(url, opts);
      };
    }, !SERVED);
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (SERVED && (url.origin !== BASE || url.pathname !== '/api/cross-border')) return route.continue();
      if (url.href.startsWith('https://unpkg.com/chart.js@4.5.1/')) return route.continue();
      if (url.origin !== BASE) return route.fulfill({ body: '', contentType: 'text/plain' });
      if (url.pathname === '/api/cross-border') {
        calls.push(url.searchParams);
        if (mode === 'error') return route.fulfill({ status: 503, json: { error: 'Fixture unavailable' } });
        if (mode === 'invalid') return route.fulfill({ status: 400, json: { error: 'Fixture filter rejected' } });
        const data = clone(SAMPLE);
        data.filters = { localIata: (url.searchParams.get('localIata') || 'AAA').split(','),
          peerIata: (url.searchParams.get('peerIata') || 'BBB').split(','), scope: url.searchParams.get('scope') || 'all',
          view: url.searchParams.get('view') || '', windowMinutes: Number(url.searchParams.get('windowMinutes') || (mode === 'operator-default' ? 180 : 60)) };
        if (mode === 'operator-default') data.options.windowMinutes = [15, 60, 180];
        data.sources.local.selectionIata = data.filters.localIata;
        data.sources.peer.selectionIata = data.filters.peerIata;
        ['local', 'peer'].forEach(side => {
          const selected = new Set(data.filters[side + 'Iata']);
          const source = data.dashboard[side];
          source.regions = source.regions.filter(region => selected.has(region.iata));
          source.records = source.regions.reduce((sum, region) => sum + region.records, 0);
          source.contents = source.regions.reduce((sum, region) => sum + region.contents, 0);
        });
        data.dashboard.regionPairs = data.dashboard.regionPairs.filter(pair => data.filters.localIata.includes(pair.localIata) && data.filters.peerIata.includes(pair.peerIata));
        data.options.peerRegions.push({ iata: 'CCC', label: 'CCC', observers: 1 });
        data.peerObservers[0].name = 'Peer observer with a very long public label '.repeat(5) + '<img src=x onerror=alert(1)>';
        data.peerRepeaters[0].name = 'Peer repeater with a long label '.repeat(5);
        data.packets[0].localObservations[0].scope = { kind: 'unknown', name: null };
        if (data.filters.scope !== 'all') data.packets = data.packets.filter(packet => packet.scope.kind === data.filters.scope || packet.scope.name === data.filters.scope);
        data.summary.peerPackets = data.packets.length;
        if (mode === 'empty' || mode === 'disabled' || mode === 'unavailable') {
          data.packets = []; data.peerObservers = []; data.peerRepeaters = [];
          data.summary = { peerPackets: 0, localReferencePackets: 0, independentSharedPackets: 0, sharedObserverOnlyPackets: 0, sharedFeedIdentityPackets: 0 };
          data.sources.peer.status = data.sources.local.status = mode === 'empty' ? 'empty' : mode;
          data.sources.peer.latestObservationAt = data.sources.local.latestObservationAt = null;
          data.status = mode === 'empty' ? 'ready' : mode;
          if (mode === 'disabled') data.enabled = false;
          ['local', 'peer'].forEach(side => { Object.assign(data.dashboard[side], {status: mode === 'empty' ? 'empty' : 'unavailable', records:0, contents:0, floodRecords:0, widths:[], timeline:[], scopes:[], regions:[], repeaters:[], radio:{status:'empty',txAirtimePct:null,rxAirtimePct:null,reportingObservers:0,observers:[]} }); });
        }
        if (mode === 'missing') {
          data.status = 'partial'; data.sources.local.status = 'unavailable'; data.summary.localReferencePackets = null;
          data.peerRepeaters[0].declaredScopes = null;
          data.peerObservers[0].latestReportedRadioHardware = null;
          data.peerObservers[0].reportedCanRelay = false;
          data.packets[0].overlap = 'local_unavailable';
          data.packets[0].observationsTruncated = null;
          data.peerObservers[0].hopWidths = null;
          data.sources.peer.referenceProfile.url = 'javascript:alert(1)';
          data.dashboard.local.status = 'unavailable'; data.dashboard.local.radio.status = 'unavailable'; data.dashboard.local.radio.txAirtimePct = null; data.dashboard.local.radio.rxAirtimePct = null; data.dashboard.local.radio.observers = [];
        }
        if (mode === 'stale') data.sources.peer.status = 'stale';
        if (mode === 'scope-shift') ['local', 'peer'].forEach(side => { data.dashboard[side].scopes.forEach((scope, index) => { scope.records = 50 - index * 7; }); });
        if (mode === 'region-pairs-cap') { data.status = 'partial'; data.dashboard.regionPairsTruncated = true; }
        if (mode === 'radio-cap-local' || mode === 'radio-cap-peer') { data.status = 'partial'; data.dashboard[mode.endsWith('local') ? 'local' : 'peer'].radio.truncated = true; }
        if (mode === 'detail-only-cap') { data.status = 'partial'; data.completeness.complete = false; data.completeness.packetsTruncated = true; }
        if (mode === 'identity-unverified') {
          data.packets[0].overlap = 'identity_unverified';
          data.summary.identityUnverifiedPackets = 1;
          data.summary.independentSharedPackets = 0; data.dashboard.identityComplete = false;
        }
        if (mode === 'shared-feed-identity') {
          data.packets[0].overlap = 'shared_feed_identity';
          data.summary.independentSharedPackets = 0;
          data.summary.sharedObserverOnlyPackets = 0;
          data.summary.sharedFeedIdentityPackets = 1;
        }
        if (mode === 'after-snapshot') {
          data.status = 'partial'; data.completeness.complete = false;
          data.sources.peer.status = 'after_snapshot';
          data.sources.peer.latestObservationAt = null; data.sources.peer.ageSeconds = null;
          data.sources.peer.latestAfterSnapshotAt = '2026-10-28T20:01:00Z';
        }
        if (mode === 'caps') {
          data.completeness = { complete: false, packetsTruncated: true, peerRepeatersTruncated: true };
          data.peerObservers = Array.from({ length: 205 }, (_, i) => Object.assign({}, data.peerObservers[0], { id: 'peer-' + i }));
          data.peerRepeaters = Array.from({ length: 205 }, (_, i) => Object.assign({}, data.peerRepeaters[0], { publicKey: 'key-' + i }));
          data.packets = Array.from({ length: 105 }, (_, i) => Object.assign({}, data.packets[0], { hash: 'packet-' + i }));
          data.summary.peerPackets = 105;
          data.dashboard.local.truncated = true; data.dashboard.local.coveredFromAt = '2026-10-28T19:30:00Z';
          data.dashboard.local.repeatersTruncated = true;
          data.dashboard.local.timeline.slice(0,6).forEach(point => { point.complete=false; });
        }
        if (heldWindow === data.filters.windowMinutes) {
          await new Promise(resolve => pending.push({ route, data, resolve }));
          return;
        }
        return route.fulfill({ json: data });
      }
      if (url.pathname.startsWith('/api/')) {
        const data = url.pathname === '/api/stats' ? { totalPackets: 3, totalNodes: 1, totalObservers: 2 } : {};
        return route.fulfill({ json: data });
      }
      if (url.pathname === '/') {
        let html = fs.readFileSync(path.join(PUBLIC, 'index.html'), 'utf8');
        html = html.replace(/<script\b[^>]*\bsrc="([^"]+)"[^>]*>[\s\S]*?<\/script>/g,
          (tag, src) => SHELL_SCRIPTS.has(src.split('?')[0]) || src.startsWith('https://unpkg.com/chart.js@4.5.1/') ? tag : '');
        return route.fulfill({ body: html, contentType: 'text/html' });
      }
      const file = path.resolve(PUBLIC, '.' + url.pathname);
      if (!file.startsWith(PUBLIC + path.sep) || !fs.existsSync(file)) return route.fulfill({ status: 404, body: 'Not found' });
      return route.fulfill({ path: file });
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', error => errors.push(error.message));
    const ready = () => page.waitForFunction(() => { const el = document.querySelector('#cb-results'); return el && !el.hasAttribute('aria-busy'); });
    async function apply(values) {
      if (values.window != null) await page.selectOption('#cb-window', String(values.window));
      if (values.scope != null) await page.selectOption('#cb-scope', values.scope);
      if (values.peer != null) await page.fill('#cb-peer', values.peer);
      if (values.local != null) await page.fill('#cb-local', values.local);
      await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
    }
    async function releasePending() {
      const items = pending; pending = [];
      for (const item of items) { await item.route.fulfill({ json: item.data }); item.resolve(); }
    }
    async function saveCoverage() {
      if (!SERVED) return;
      const coverage = await page.evaluate(() => {
        const entries = Object.entries(window.__coverage__ || {}).filter(([key, value]) => /(^|[\\/])cross-border(?:-dashboard)?\.js$/.test(value.path || key));
        return entries.length ? Object.fromEntries(entries) : null;
      });
      if (!coverage) return;
      const outDir = path.join(ROOT, '.nyc_output');
      fs.mkdirSync(outDir, { recursive: true });
      fs.writeFileSync(path.join(outDir, 'cross-border-' + randomUUID() + '.json'), JSON.stringify(coverage));
      coverageFiles++;
    }
    async function check(name, fn) {
      try { await fn(); await saveCoverage(); passed++; console.log('PASS ' + name); }
      catch (error) { failed++; throw error; }
    }

    await check('Tools keyboard entry, one bulk request, peer-only evidence and reported radio metadata labels', async () => {
      await page.goto(BASE + '/#/tools');
      const link = page.getByRole('link', { name: /Border links/ });
      await link.focus(); await page.keyboard.press('Enter'); await ready();
      assert.strictEqual(calls.length, 1);
      assert.strictEqual(calls[0].get('view'), 'dashboard');
      await page.waitForFunction(() => window.Chart && Chart.getChart('cb-advert-chart'));
      assert.strictEqual(await page.evaluate(() => Chart.version), '4.5.1');
      assert(await page.locator('#cb-dashboard').isVisible());
      assert(!(await page.locator('#cb-evidence').evaluate(node => node.open)));
      assert.strictEqual(await page.locator('#cb-dash-local').innerText(), '150');
      assert.strictEqual(await page.locator('#cb-dash-peer').innerText(), '70');
      const evidence = page.locator('#cb-evidence > summary');
      await evidence.focus(); await page.keyboard.press('Enter');
      await page.waitForFunction(() => document.querySelector('#cb-evidence').open);
      assert.strictEqual(await page.locator('#cb-packets tr').count(), 3);
      assert.match(page.url(), /localIata=AAA/);
      assert.match(page.url(), /peerIata=BBB/);
      assert.strictEqual(await page.locator('.cb-page a[href^="#/packets"], .cb-page a[href^="#/nodes"]').count(), 0);
      assert.match(await page.locator('#cb-observers').innerText(), /910\.525,62\.5,7,8/);
      assert.match(await page.locator('#cb-observers').innerText(), /910\.525024,62\.5,7,5/);
      assert.strictEqual(await page.getByRole('columnheader', { name: 'Reported radio metadata', exact: true }).count(), 1);
      assert.match(await page.locator('section[aria-labelledby="cb-observers-title"]').innerText(), /latest broker string[\s\S]*hardware or a profile[\s\S]*does not verify per-packet settings/);
      assert.match(await page.locator('#cb-findings').textContent(), /Reported radio metadata[\s\S]*hardware or a profile[\s\S]*does not verify per-packet settings/);
      assert.match(await page.locator('#cb-observers').innerText(), /Reported: Unknown/);
      assert.strictEqual(await page.locator('.cb-page img').count(), 0);
      assert.match(await page.locator('#cb-packets').innerText(), /Match · distinct observers/);
      assert.match(await page.locator('#cb-packets').innerText(), /Match · same observer present/);
      assert.strictEqual(await page.locator('#cb-local-count').innerText(), '2');
      const localMatchCard = await page.locator('#cb-local-count').locator('..').innerText();
      assert.match(localMatchCard, /All Canadian matches/);
      assert.match(localMatchCard, /Among peer packets; includes shared-identity matches/);
      assert.doesNotMatch(localMatchCard, /Reference feed total|Canadian reference packets/);
      assert.strictEqual(await page.locator('#cb-peer-profile a').getAttribute('href'), 'https://example.org/radio-profile');
      assert.match(await page.locator('#cb-peer-profile').textContent(), /not device measurements/);
    });
    await check('plain-language overview keeps technical details in a keyboard-accessible shareable disclosure', async () => {
      assert.match(await page.locator('#cb-intro').innerText(), /path widths, scopes and radio airtime/);
      assert.match(await page.locator('.cb-summary').innerText(), /Matching Canadian packets[\s\S]*Distinct observers · among peer packets/);
      assert(await page.locator('#cb-completeness').isHidden());
      assert(await page.locator('#cb-peer-quality').isHidden());
      assert(await page.locator('#cb-findings').isHidden());
      assert(await page.locator('#cb-quality-warning').isHidden());
      const before = calls.length;
      const disclosure = page.locator('#cb-quality summary');
      await disclosure.focus(); await page.keyboard.press('Enter');
      await page.waitForFunction(() => location.hash.includes('quality=1'));
      assert(await page.locator('#cb-completeness').isVisible());
      assert.strictEqual(calls.length, before, 'opening data quality must not fetch data');
      await page.keyboard.press('Space');
      await page.waitForFunction(() => !location.hash.includes('quality=1'));
      await page.goBack(); await ready();
      assert(await page.locator('#cb-quality').evaluate(node => node.open));
      await page.goForward(); await ready();
      assert(!(await page.locator('#cb-quality').evaluate(node => node.open)));
    });
    await check('packet evidence has its own URL state and per-source scope; close restores focus', async () => {
      await page.locator('#cb-packets button').first().focus(); await page.keyboard.press('Enter');
      assert.match(page.url(), /packet=1111111111111111/);
      assert.match(await page.locator('#cb-peer-evidence').innerText(), /Scope: example/);
      assert.match(await page.locator('#cb-local-evidence').innerText(), /Scope: Unknown scope/);
      await page.click('#cb-close-detail');
      assert(!page.url().includes('packet='));
      assert.strictEqual(await page.evaluate(() => document.activeElement.dataset.packet), '1111111111111111');
    });
    await check('refresh preserves row identity and keyboard focus', async () => {
      await page.evaluate(() => { window.__firstBorderRow = document.querySelector('#cb-packets tr'); });
      const before = calls.length;
      await page.click('#cb-refresh'); await ready();
      assert.strictEqual(calls.length, before + 1);
      assert(await page.evaluate(() => window.__firstBorderRow === document.querySelector('#cb-packets tr')));
      assert.strictEqual(await page.evaluate(() => document.activeElement.id), 'cb-refresh');
    });
    await check('overview scope control preserves separately labelled packet-reference evidence', async () => {
      assert.strictEqual(await page.locator('#cb-scope').evaluate(node => node.labels[0].firstChild.textContent), 'Scope');
      assert.match(await page.locator('.cb-filter-help').innerText(), /Scope filters both overview samples; radio reports include all scopes/);
      await apply({ scope: 'example' }); await ready();
      assert.strictEqual(calls.at(-1).get('scope'), 'example');
      assert.strictEqual(await page.locator('#cb-packets tr').count(), 1);
      assert.match(await page.locator('#cb-packets').innerText(), /Match · distinct observers/);
      assert.strictEqual(await page.locator('#cb-independent-count').innerText(), '1');
      await page.locator('#cb-packets button').first().click();
      assert.match(await page.locator('#cb-peer-evidence').innerText(), /Scope: example/);
      assert.match(await page.locator('#cb-local-evidence').innerText(), /Scope: Unknown scope/);
      await page.click('#cb-close-detail');
      await apply({ scope: 'all' }); await ready();
    });
    await check('filters are applied together and browser Back/Forward restores them', async () => {
      await apply({ window: 180, scope: 'unscoped', peer: 'CCC' }); await ready();
      assert.strictEqual(await page.locator('#cb-packets tr').count(), 1);
      assert.strictEqual(calls.at(-1).get('peerIata'), 'CCC');
      assert.strictEqual(calls.at(-1).get('windowMinutes'), '180');
      await page.goBack(); await ready();
      assert.strictEqual(await page.inputValue('#cb-peer'), 'BBB');
      assert.strictEqual(await page.inputValue('#cb-window'), '60');
      await page.goForward(); await ready();
      assert.strictEqual(await page.inputValue('#cb-peer'), 'CCC');
      assert.strictEqual(await page.inputValue('#cb-window'), '180');
    });
    await check('invalid IATA is rejected locally and HTTP failures stay recoverable', async () => {
      const before = calls.length;
      await apply({ peer: 'not-a-code' });
      assert.strictEqual(calls.length, before);
      assert.match(await page.locator('#cb-status').innerText(), /three-letter IATA/);
      mode = 'invalid'; await apply({ peer: 'ZZZ' }); await ready();
      assert.match(await page.locator('#cb-status').innerText(), /filter selection is not supported/);
      mode = 'error'; await apply({ peer: 'BBB' }); await ready();
      assert(await page.locator('#cb-results').isHidden());
      assert.match(await page.locator('#cb-status').innerText(), /Refresh to retry/);
      mode = 'normal'; await page.click('#cb-refresh'); await ready();
      assert(await page.locator('#cb-results').isVisible());
    });
    await check('aborted old response cannot replace a newer selection even if transport ignores cancellation', async () => {
      await page.evaluate(() => { window.__ignoreBorderAbort = true; });
      heldWindow = 15;
      await apply({ window: 15, scope: 'all' });
      await page.waitForFunction(() => document.querySelector('#cb-results').hasAttribute('aria-busy'));
      await waitForPending(() => pending.length > 0);
      const pendingCalls = calls.length;
      await page.evaluate(() => window.__borderMinuteCallbacks.at(-1)());
      assert.strictEqual(calls.length, pendingCalls, 'automatic refresh must not overlap a pending request');
      assert(!(await page.evaluate(() => window.__borderSignals.at(-1).aborted)), 'automatic refresh must not abort the active request');
      await apply({ window: 360 }); await ready();
      assert(await page.evaluate(() => window.__borderSignals.at(-2).aborted));
      await releasePending(); heldWindow = null;
      await page.waitForFunction(() => document.querySelector('#cb-window').value === '360');
      assert.match(page.url(), /windowMinutes=360/);
    });
    await check('empty, missing metadata and unavailable references never imply loss or zero unknown values', async () => {
      mode = 'empty'; await page.click('#cb-refresh'); await ready();
      assert(await page.locator('#cb-packets-empty').isVisible());
      assert.strictEqual(await page.locator('#cb-packets tr').count(), 0);
      mode = 'missing'; await page.click('#cb-refresh'); await ready();
      assert.strictEqual(await page.locator('#cb-local-count').innerText(), 'Unavailable');
      assert.doesNotMatch(await page.locator('#cb-packets').innerText(), /Match · distinct observers/);
      assert(await page.locator('#cb-quality-warning').isVisible());
      assert.match(await page.locator('#cb-quality-warning').innerText(), /Partial comparison.*unavailable/);
      assert.match(await page.locator('#cb-repeaters').innerText(), /Not reported/);
      assert.match(await page.locator('#cb-observers').innerText(), /Can relay: No/);
      assert.match(await page.locator('#cb-observers').innerText(), /Unknown/);
      assert.strictEqual(await page.locator('#cb-peer-profile a').getAttribute('href'), null);
      await page.locator('#cb-packets button').first().click();
      assert.match(await page.locator('#cb-detail-meta').innerText(), /completeness unknown/);
      await page.click('#cb-close-detail');
      mode = 'unavailable'; await page.click('#cb-refresh'); await ready();
      assert.strictEqual(await page.locator('#cb-peer-count').innerText(), 'Unavailable');
      assert.match(await page.locator('#cb-packets-empty').innerText(), /unavailable/);
      mode = 'stale'; await page.click('#cb-refresh'); await ready();
      assert(await page.locator('#cb-quality-warning').isVisible());
      assert.match(await page.locator('#cb-quality-warning').innerText(), /stale evidence/);
      assert(await page.locator('#cb-completeness').isHidden());
    });
    await check('incomplete observer identities are warned about and never displayed as distinct-observer matches', async () => {
      mode = 'identity-unverified'; await page.click('#cb-refresh'); await ready();
      const row = page.locator('#cb-packets tr').first();
      assert.match(await row.innerText(), /Observer identity check incomplete/);
      assert.doesNotMatch(await row.innerText(), /Match · distinct observers|Match · same observer present/);
      assert.strictEqual(await page.locator('#cb-independent-count').innerText(), '0');
      assert(await page.locator('#cb-quality-warning').isVisible());
      assert.match(await page.locator('#cb-quality-warning').innerText(), /Observer identity check incomplete/);
      assert(!(await page.locator('#cb-quality').evaluate(node => node.open)), 'identity warning must remain outside the closed disclosure');
      await row.locator('button').click();
      assert.match(await page.locator('#cb-detail-meta').innerText(), /Observer identity check incomplete/);
      await page.click('#cb-close-detail');
    });
    await check('large samples are bounded and caps remain visible', async () => {
      mode = 'caps'; await page.click('#cb-refresh'); await ready();
      assert.strictEqual(await page.locator('#cb-packets tr').count(), 100);
      assert.strictEqual(await page.locator('#cb-observers tr').count(), 200);
      assert.strictEqual(await page.locator('#cb-repeaters tr').count(), 200);
      assert.match(await page.locator('#cb-quality-warning').innerText(), /Partial comparison.*sample limits/);
      assert(await page.locator('#cb-quality-warning').isVisible());
      assert.match(await page.locator('#cb-observer-count').innerText(), /200 of 205/);
      assert.match(await page.locator('#cb-dashboard-coverage').innerText(), /capped/);
      assert(await page.evaluate(() => Chart.getChart('cb-local-trend').data.datasets.every(ds => ds.data.slice(0,6).every(value => value === null))));
    });
    await check('shared-feed identities do not claim the same observer reported the packet', async () => {
      mode = 'shared-feed-identity'; await page.click('#cb-refresh'); await ready();
      const row = page.locator('#cb-packets tr').first();
      assert.match(await row.innerText(), /Match · shared-feed identity/);
      assert.doesNotMatch(await row.innerText(), /Match · same observer present|Match · distinct observers/);
      assert.strictEqual(await page.locator('#cb-shared-count').innerText(), '1');
      assert.match(await page.locator('.cb-summary').innerText(), /Shared-identity matches/);
    });
    await check('overview status preserves region and radio caps while exempting detail-only limits', async () => {
      for (const [nextMode, warning] of [['region-pairs-cap', /Region comparisons are limited/], ['radio-cap-local', /Airtime reports are limited/], ['radio-cap-peer', /Airtime reports are limited/]]) {
        mode = nextMode; await page.click('#cb-refresh'); await ready();
        assert.match(await page.locator('#cb-status').innerText(), /Partial comparison/);
        assert(await page.locator('#cb-quality-warning').isVisible());
        assert.match(await page.locator('#cb-quality-warning').innerText(), warning);
      }
      mode = 'detail-only-cap'; await page.click('#cb-refresh'); await ready();
      assert.match(await page.locator('#cb-status').innerText(), /Comparison ready/);
      assert(await page.locator('#cb-quality-warning').isHidden());
      assert.match(await page.locator('#cb-detail-quality-warning').innerText(), /sample limits/);
      mode = 'normal';
    });
    await check('after-snapshot observations show a neutral quality warning and unknown freshness', async () => {
      mode = 'after-snapshot'; await page.click('#cb-refresh'); await ready();
      assert.match(await page.locator('#cb-quality-warning').innerText(), /after the snapshot cutoff/);
      assert.match(await page.locator('#cb-peer-source').innerText(), /Only after-snapshot observations.*\n.*\nLatest observation: Unknown · Age unknown/);
      assert.doesNotMatch(await page.locator('#cb-quality-warning').innerText(), /clock (fault|skew|error)/i);
      mode = 'normal';
    });
    await check('charts show separate adoption denominators, gaps and distinct scope categories', async () => {
      mode = 'normal'; await page.click('#cb-refresh'); await ready();
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-advert-chart').data.datasets.map(ds => ds.data)), [[20,70],[70,20],[10,10]]);
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-width-chart').data.datasets.map(ds => ds.data)), [[20,70],[65,20],[15,10]]);
      assert(await page.evaluate(() => Chart.getChart('cb-local-trend').data.datasets.every(ds => ds.data[0] === null && ds.spanGaps === false)));
      assert.match(await page.locator('#cb-scope-values').innerText(), /Transport wildcard/);
      assert.match(await page.locator('#cb-radio-values').innerText(), /4 reporting radios/);
      assert.match(await page.locator('#cb-radio-values').innerText(), /Latest available report: (?!Unknown)/);
      assert.deepStrictEqual(await page.evaluate(() => {
        const footer = Chart.getChart('cb-local-trend').options.plugins.tooltip.callbacks.footer;
        return [footer([{dataIndex:1}]), footer([{dataIndex:0}])];
      }), ['Qualifying flood records: 100', 'Qualifying flood records: Unknown']);
      const trendMethod = page.getByText('Time-series values and method', { exact: true });
      await trendMethod.click();
      assert(await page.locator('#cb-trend-values').isVisible());
      const trendValues = await page.locator('#cb-trend-values tr').nth(1).locator('td').allTextContents();
      assert.strictEqual(trendValues[0], 'Canada');
      assert.deepStrictEqual(trendValues.slice(2), ['100', '47%', '33%', '20%']);
      assert.strictEqual(await page.locator('#cb-trend-values tr').first().locator('td').nth(2).innerText(), 'Unknown');
      await trendMethod.click();
      assert.strictEqual(await page.locator('#cb-dashboard canvas').count(), 6);
      assert(await page.locator('#cb-dashboard canvas').evaluateAll(nodes => nodes.every(canvas => canvas.width > 100 && canvas.height > 100)));
      await page.evaluate(() => { window.__keptBorderChart = Chart.getChart('cb-advert-chart'); });
      await page.click('#cb-refresh'); await ready();
      assert(await page.evaluate(() => window.__keptBorderChart === Chart.getChart('cb-advert-chart')), 'refresh must update charts in place');
      const scopeColors = await page.evaluate(() => Chart.getChart('cb-scope-chart').data.datasets.map(ds => [ds.label, ds.backgroundColor]));
      mode = 'scope-shift'; await page.click('#cb-refresh'); await ready();
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-scope-chart').data.datasets.map(ds => [ds.label, ds.backgroundColor])), scopeColors, 'scope colors must not change when counts move');
      mode = 'normal';
    });
    await check('repeater matrix searches and drill-downs without another request', async () => {
      const before = calls.length;
      await page.fill('#cb-repeater-search', 'Canadian test repeater 1');
      assert.strictEqual(await page.locator('#cb-carry-rows tr').count(), 1);
      assert.match(page.url(), /repeater=Canadian/);
      const row = page.locator('#cb-carry-rows tr');
      assert.match(await row.innerText(), /2-byte[\s\S]*Ambiguous[\s\S]*Observed[\s\S]*Observed/);
      await row.locator('button').click();
      assert(await page.locator('#cb-repeater-detail').isVisible());
      assert.match(page.url(), /node=local/);
      await page.click('#cb-repeater-close');
      assert.strictEqual(calls.length, before);
      await page.fill('#cb-repeater-search', '');
      assert.strictEqual(await page.locator('#cb-carry-rows tr').count(), 8);
    });
    await check('region matrix selects a comparison and preserves unknown identity states', async () => {
      await apply({local:'AAA,AAD', peer:'BBB,CCC'}); await ready();
      await page.evaluate(() => { window.__keptRegionButton = document.querySelector('#cb-region-grid button'); });
      await page.click('#cb-refresh'); await ready();
      assert(await page.evaluate(() => window.__keptRegionButton === document.querySelector('#cb-region-grid button')), 'refresh must retain region button identity');
      await page.locator('#cb-region-grid button[data-local="AAA"][data-peer="CCC"]').click(); await ready();
      assert.strictEqual(calls.at(-1).get('localIata'), 'AAA');
      assert.strictEqual(calls.at(-1).get('peerIata'), 'CCC');
      mode = 'identity-unverified'; await page.click('#cb-refresh'); await ready();
      assert.strictEqual(await page.locator('#cb-dash-shared').innerText(), 'Unknown');
      assert(await page.locator('#cb-region-grid button').evaluateAll(nodes => nodes.every(node => node.textContent === '?')));
      mode = 'normal'; await apply({peer:'BBB'}); await ready();
    });
    await check('live updates pause while hidden, never overlap, and retain URL preference', async () => {
      const before = calls.length;
      await page.evaluate(() => { window.__borderHidden = true; document.dispatchEvent(new Event('visibilitychange')); window.__borderMinuteCallbacks.at(-1)(); });
      assert.strictEqual(calls.length, before);
      await page.evaluate(() => { window.__borderHidden = false; document.dispatchEvent(new Event('visibilitychange')); }); await ready();
      assert.strictEqual(calls.length, before + 1);
      await page.uncheck('#cb-live');
      assert.match(page.url(), /live=0/);
      await page.evaluate(() => window.__borderMinuteCallbacks.at(-1)());
      assert.strictEqual(calls.length, before + 1);
      await page.check('#cb-live');
      assert.match(page.url(), /live=1/);
      await page.evaluate(() => window.__borderMinuteCallbacks.at(-1)()); await ready();
      assert.strictEqual(calls.length, before + 2);
    });
    await check('disabled feature remains navigable and late responses do not overwrite route exit', async () => {
      mode = 'disabled'; await page.click('#cb-refresh'); await ready();
      assert(await page.locator('#cb-results').isHidden());
      assert.match(await page.locator('#cb-status').innerText(), /disabled/);
      mode = 'normal'; heldWindow = 15;
      await apply({ window: 15 });
      await waitForPending(() => pending.length > 0);
      await page.locator('.cb-page > header a').click();
      await page.waitForSelector('.tools-landing');
      assert(await page.evaluate(() => window.__borderSignals.at(-1).aborted));
      await releasePending(); heldWindow = null;
      assert.strictEqual(await page.locator('#cb-results').count(), 0);
      assert.strictEqual(await page.locator('.tools-landing').count(), 1);
      assert.strictEqual(await page.evaluate(() => Object.keys(Chart.instances).length), 0, 'route exit destroys all owned charts');
      const exitedCalls = calls.length;
      await page.evaluate(() => window.__borderMinuteCallbacks.at(-1)());
      assert.strictEqual(calls.length, exitedCalls, 'late live timer cannot fetch after exit');
    });
    await check('operator default window and supported window options control the initial selection', async () => {
      mode = 'operator-default';
      await page.goto(BASE + '/#/cross-border'); await ready();
      assert.strictEqual(calls.at(-1).has('windowMinutes'), false, 'initial request must let the server choose its window');
      assert.strictEqual(await page.inputValue('#cb-window'), '180');
      assert.match(page.url(), /windowMinutes=180/);
      assert.deepStrictEqual(await page.locator('#cb-window option').evaluateAll(nodes => nodes.map(node => node.value)), ['15', '60', '180']);
      await page.goto(BASE + '/#/cross-border?windowMinutes=60'); await ready();
      assert.strictEqual(calls.at(-1).get('windowMinutes'), '60', 'an explicit supported URL window must survive');
      assert.strictEqual(await page.inputValue('#cb-window'), '60');
      await page.goto(BASE + '/#/cross-border?windowMinutes=9999'); await ready();
      assert.strictEqual(calls.at(-1).has('windowMinutes'), false, 'invalid window must not silently become 60');
      assert.strictEqual(await page.inputValue('#cb-window'), '180');
      assert.match(page.url(), /windowMinutes=180/);
      mode = 'normal';
    });
    await check('mobile form and long labels stay inside the viewport; keyboard can reach controls', async () => {
      await page.setViewportSize({ width: 375, height: 900 });
      await page.goto(BASE + '/#/cross-border'); await ready();
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
      assert(await page.locator('[data-bottom-nav-tab="more"]').evaluate(node => node.classList.contains('active')),
        'Border links belongs under Tools, so the mobile More tab must remain active');
      await page.focus('#cb-peer'); await page.keyboard.press('Tab');
      assert.strictEqual(await page.evaluate(() => document.activeElement.id), 'cb-local');
      await page.selectOption('#cb-window', '180');
      await page.getByRole('button', { name: 'Apply filters', exact: true }).focus(); await page.keyboard.press('Enter'); await ready();
      assert.match(page.url(), /windowMinutes=180/);
      if (process.env.CROSS_BORDER_ARTIFACT_DIR) {
        fs.mkdirSync(process.env.CROSS_BORDER_ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-mobile.png'), fullPage: true });
        await page.locator('#cb-adoption-title').scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-mobile-adoption.png') });
        await page.locator('#cb-carry-title').scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-mobile-repeaters.png') });
        await page.setViewportSize({ width: 1440, height: 1050 });
        await page.reload(); await ready();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-desktop.png'), fullPage: true });
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-desktop-top.png') });
        await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'dark'); window.dispatchEvent(new Event('theme-changed')); });
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-dark.png'), fullPage: true });
      }
    });
    assert.deepStrictEqual(errors, [], 'Browser must have no uncaught JavaScript errors');
    console.log('Border links ' + (SERVED ? 'served' : 'fixture') + ' browser acceptance complete; ' + coverageFiles + ' page-only coverage files. No server processes started.');
  } catch (error) {
    if (!failed) failed++;
    throw error;
  } finally {
    for (const item of pending) { await item.route.abort().catch(() => {}); item.resolve(); }
    await browser.close();
    console.log(passed + ' passed, ' + failed + ' failed');
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
