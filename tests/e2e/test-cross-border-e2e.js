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
  'region-filter.js', 'area-filter.js', 'app.js', 'url-state.js', 'bottom-nav.js', 'nav-drawer.js', 'map-tile-providers.js', 'cross-border-flow.js', 'cross-border-dashboard.js', 'cross-border.js']);
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
      if (url.href.startsWith('https://unpkg.com/chart.js@4.5.1/') || url.href.startsWith('https://unpkg.com/leaflet@1.9.4/')) return route.continue();
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
          data.dashboard.flow.status = mode === 'empty' ? 'empty' : 'unavailable';data.dashboard.flow.paths=[];data.dashboard.flow.directions.forEach(d=>Object.assign(d,{contents:0,scoped:0,unscoped:0,mixed:0,transportWildcard:0,unknownScopeName:0,repeaters:0,furthestKm:null,timeline:[]}));
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
        if (mode === 'mapper-denied') data.meshMapper={status:'access_denied',fetchedAt:null,partial:true,regions:[],repeaters:[],scopes:[]};
        if (mode === 'mapper-stale') data.meshMapper.status='stale';
        if (mode === 'mapper-rate-limited') data.meshMapper.status='rate_limited';
        if (mode === 'mapper-loading') data.meshMapper={status:'loading',fetchedAt:null,partial:true,regions:[],repeaters:[],scopes:[]};
        if (mode === 'scope-shift') ['local', 'peer'].forEach(side => { data.dashboard[side].scopes.forEach((scope, index) => { scope.records = 50 - index * 7; }); });
        if (mode === 'region-pairs-cap') { data.status = 'partial'; data.dashboard.regionPairsTruncated = true; }
        if (mode === 'radio-cap-local' || mode === 'radio-cap-peer') { data.status = 'partial'; data.dashboard[mode.endsWith('local') ? 'local' : 'peer'].radio.truncated = true; }
        if (mode === 'detail-only-cap') { data.status = 'partial'; data.completeness.complete = false; data.completeness.packetsTruncated = true; }
        if (mode === 'scope-match-cap' || mode === 'scope-catalog-cap' || mode === 'unexplained-partial') {
          data.status='partial';data.completeness.complete=false;
          if(mode==='scope-match-cap')data.scopeResolution={limited:true};
          if(mode==='scope-catalog-cap')data.scopeCatalogTruncated=true;
        }
        if (mode === 'identity-unverified') {
          data.packets[0].overlap = 'identity_unverified';
          data.summary.identityUnverifiedPackets = 1;
          data.summary.independentSharedPackets = 0; data.dashboard.identityComplete = false;
        }
        if(mode==='flow-identity-incomplete') {
          data.status='partial';data.completeness.complete=false;data.dashboard.flow.status='partial';data.dashboard.flow.identityComplete=false;
          data.dashboard.flow.paths=[];data.dashboard.flow.repeaters=[];
          data.dashboard.flow.directions.forEach(direction=>{Object.assign(direction,{contents:0,scoped:0,unknownScopeName:0,unscoped:0,transportWildcard:0,mixed:0,repeaters:0,furthestKm:null});direction.timeline.forEach(point=>{point.contents=0;point.complete=true;});});
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
          (tag, src) => SHELL_SCRIPTS.has(src.split('?')[0]) || (src.startsWith('https://unpkg.com/chart.js@4.5.1/') || src.startsWith('https://unpkg.com/leaflet@1.9.4/')) ? tag : '');
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
      if (values.scope != null) await page.selectOption('#cb-scope', values.scope);
      if ((values.peer != null || values.local != null) && !(await page.locator('#cb-region-advanced').evaluate(node=>node.open))) await page.locator('#cb-region-advanced > summary').click();
      if (values.peer != null) await page.fill('#cb-peer', values.peer);
      if (values.local != null) await page.fill('#cb-local', values.local);
      if (values.window != null) await page.locator('#cb-window-pills button[data-window="' + values.window + '"]').click();
      else await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
    }
    async function tab(name) { await page.getByRole('tab', { name: name, exact: true }).click(); }
    async function drawnPaths() {
      await page.waitForFunction(() => Array.from(document.querySelectorAll('#cb-flow-map .leaflet-overlay-pane canvas')).some(canvas => {
        if (!canvas.width || !canvas.height) return false;
        const pixels=canvas.getContext('2d').getImageData(0,0,canvas.width,canvas.height).data;
        for(let i=3;i<pixels.length;i+=4)if(pixels[i]>0)return true;
        return false;
      }));
    }
    async function releasePending() {
      const items = pending; pending = [];
      for (const item of items) { await item.route.fulfill({ json: item.data }); item.resolve(); }
    }
    async function saveCoverage() {
      if (!SERVED) return;
      const coverage = await page.evaluate(() => {
        const entries = Object.entries(window.__coverage__ || {}).filter(([key, value]) => /(^|[\\/])cross-border(?:-dashboard|-flow)?\.js$/.test(value.path || key));
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
      assert.strictEqual(await page.locator('.cb-page img[src="x"]').count(), 0);
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
    await check('crossings lead with direction, scoped shares and an actual Leaflet map', async () => {
      assert.strictEqual(await page.getByRole('tab', {name:'Crossings', exact:true}).getAttribute('aria-selected'), 'true');
      assert.strictEqual(await page.locator('#cb-flow-ca_us-count').innerText(), '2');
      assert.strictEqual(await page.locator('#cb-flow-us_ca-count').innerText(), '2');
      assert.strictEqual(await page.locator('#cb-flow-ca_us-scoped').innerText(), '50%');
      assert.strictEqual(await page.locator('#cb-flow-us_ca-scoped').innerText(), '50%');
      assert.match(await page.locator('#cb-flow-status').innerText(), /1 with no crossing in resolved paths.*2 with crossing unresolved/);
      await page.waitForFunction(() => window.L && document.querySelector('#cb-flow-map.leaflet-container') && Chart.getChart('cb-flow-timeline').width > 100);
      assert.strictEqual(await page.evaluate(() => L.version), '1.9.4');
      assert(await page.locator('#cb-flow-map canvas').count() > 0);
      await drawnPaths();
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-flow-scopes').data.datasets.map(ds=>ds.data)), [[50,0],[0,50],[50,50],[0,0],[0,0]]);
      assert(await page.evaluate(() => Chart.getChart('cb-flow-timeline').data.datasets.every(ds=>ds.data[0]===null && ds.spanGaps===false)));
      assert(await page.locator('.cb-overview-metrics').isHidden(), 'feed totals do not masquerade as national crossings');
      assert(await page.locator('.cb-flow-map-section').evaluate(node=>node.getBoundingClientRect().top+window.scrollY<900),'the map section should follow compact cards without a repeated heading');
      assert(!(await page.locator('#cb-region-advanced').evaluate(node=>node.open)),'CSV entry is not the default region picker');
    });
    await check('path and direction controls reuse the snapshot and preserve unresolved hops', async () => {
      const before=calls.length;
      await page.locator('[data-direction="us_ca"]').click();
      assert.match(page.url(), /direction=us_ca/);
      assert.strictEqual(await page.locator('#cb-flow-path-list button').count(), 2);
      await page.locator('#cb-flow-path-list [data-path="crossing-us-ca"]').focus();await page.keyboard.press('Enter');
      assert.match(page.url(), /path=crossing-us-ca/);
      assert.match(await page.locator('#cb-flow-detail-summary').innerText(), /name unresolved|Packet/);
      assert.match(await page.locator('#cb-flow-hop-list').innerText(), /Repeater could not be identified.*pathway stops here/);
      assert.match(await page.locator('#cb-flow-detail-summary').innerText(), /Airtime: Unavailable/);
      assert.strictEqual(await page.locator('#cb-flow-hop-list li').count(), 4);
      await page.locator('[data-direction="both"]').click();
      await page.locator('#cb-flow-path-list [data-path="crossing-ca-us"]').click();
      assert.match(await page.locator('#cb-flow-detail-summary').innerText(), /230.1 ms estimated.*one saved frame/);
      assert.match(await page.locator('#cb-flow-detail-title').innerText(),/onny.*matched against known names/);
      await page.locator('#cb-flow-hop-list details').first().locator('summary').click();
      const evidence=await page.locator('#cb-flow-hop-list details').first().innerText();
      assert.match(evidence,/queried/);assert.match(evidence,/uploaded/);assert.match(evidence,/forwarding inferred/);
      assert.strictEqual(calls.length,before,'local map/detail controls must not fetch per packet or repeater');
      await page.click('#cb-flow-clear');
      assert(!page.url().includes('path='));
    });
    await check('known scope choices remain searchable with zero traffic and keep ca separate from can', async () => {
      const before=calls.length;
      await page.fill('#cb-scope-search','Waterloo');
      assert.match(page.url(),/scopeSearch=Waterloo/);
      assert(await page.locator('#cb-scope option[value="ykf"]').count());
      assert.strictEqual(calls.length,before);
      await page.fill('#cb-scope-search','');
      assert(await page.locator('#cb-scope option[value="ca"]').count());
      assert(await page.locator('#cb-scope option[value="can"]').count());
      await tab('Scopes & regions');
      assert.match(await page.locator('#cb-catalog-list [data-scope="onqc"]').innerText(),/proposed/);
      mode='empty';await page.click('#cb-refresh');await ready();
      assert(await page.locator('#cb-scope option[value="onqc"]').count());
      await tab('Crossings');
      assert.strictEqual(await page.locator('#cb-flow-ca_us-scoped').innerText(),'—');
      assert.match(await page.locator('#cb-flow-path-count').innerText(),/does not prove that no packets crossed/);
      mode='normal';await page.click('#cb-refresh');await ready();
    });
    await check('denied MeshMapper metadata does not hide local crossing analysis', async () => {
      mode='mapper-denied';await page.click('#cb-refresh');await ready();
      assert.strictEqual(await page.locator('#cb-flow-ca_us-count').innerText(),'2');
      await page.locator('#cb-flow-path-list [data-path="crossing-ca-us"]').click();
      await page.locator('#cb-flow-hop-list details').first().locator('summary').click();
      assert.match(await page.locator('#cb-flow-hop-list').innerText(),/MeshMapper scope report unavailable/);
      await tab('Scopes & regions');assert.match(await page.locator('#cb-catalog-status').innerText(),/access not permitted/);
      mode='normal';await page.click('#cb-refresh');await ready();await tab('Crossings');
    });
    await check('MeshMapper provider ready, stale, loading and rate-limited states never become an unknown status', async () => {
      await tab('Scopes & regions');
      for(const [nextMode,label] of [['normal','available'],['mapper-stale','stale cached data'],['mapper-loading','loading'],['mapper-rate-limited','rate limited; cached data may be shown']]) {
        mode=nextMode;await page.click('#cb-refresh');await ready();
        const status=await page.locator('#cb-catalog-status').innerText();
        assert(status.includes('MeshMapper: '+label));
        assert.doesNotMatch(status,/status unknown/);
        assert.strictEqual(await page.locator('#cb-flow-ca_us-count').textContent(),'2','metadata state must not erase local crossing evidence');
      }
      mode='normal';await page.click('#cb-refresh');await ready();await tab('Crossings');
    });
    await check('region chips add and remove named regions, apply together, and restore through history', async () => {
      const before=calls.length;
      await page.selectOption('#cb-local-add','AAD');await page.selectOption('#cb-peer-add','CCC');
      assert.strictEqual(await page.locator('#cb-local-chips button').count(),2);
      assert.strictEqual(await page.locator('#cb-peer-chips button').count(),2);
      assert.match(await page.locator('#cb-local-chips').innerText(),/Additional Canadian area/);
      assert.strictEqual(calls.length,before,'editing region chips is a draft until Apply filters');
      await page.getByRole('button',{name:'Apply filters',exact:true}).click();await ready();
      assert.strictEqual(calls.at(-1).get('localIata'),'AAA,AAD');assert.strictEqual(calls.at(-1).get('peerIata'),'BBB,CCC');
      assert.match(page.url(),/localIata=AAA%2CAAD/);
      await page.locator('#cb-local-chips [data-region-code="AAD"]').click();await page.locator('#cb-peer-chips [data-region-code="CCC"]').click();
      await page.getByRole('button',{name:'Apply filters',exact:true}).click();await ready();
      assert.strictEqual(calls.at(-1).get('localIata'),'AAA');assert.strictEqual(calls.at(-1).get('peerIata'),'BBB');
      await page.goBack();await ready();assert.strictEqual(await page.locator('#cb-local-chips button').count(),2);
      await page.goForward();await ready();assert.strictEqual(await page.locator('#cb-local-chips button').count(),1);
      assert(!(await page.locator('#cb-region-advanced').evaluate(node=>node.open)));
    });
    await check('analytics category tabs reuse data, support keyboard history and resize charts', async () => {
      assert.strictEqual(await page.getByRole('tablist', {name:'Cross-border analytics'}).count(), 1);
      assert.strictEqual(await page.getByRole('tab').count(), 6);
      assert.strictEqual(await page.locator('.cb-overview-metrics .stat-card').count(), 4);
      const before = calls.length;
      await page.getByRole('tab', {name:'Hash widths', exact:true}).click();
      assert.match(page.url(), /tab=hash-widths/);
      assert.strictEqual(await page.locator('#cb-analytics-tabs .active').count(), 1);
      assert(await page.locator('#cb-adoption-title').isVisible());
      await page.waitForFunction(() => Chart.getChart('cb-advert-chart').width > 100);
      await page.keyboard.press('ArrowRight');
      assert.strictEqual(await page.getByRole('tab', {name:'Scopes & regions', exact:true}).getAttribute('aria-selected'), 'true');
      assert(await page.locator('#cb-region-grid').isVisible());
      await page.goBack();
      await page.waitForFunction(() => document.querySelector('#cb-tab-hash-widths').getAttribute('aria-selected') === 'true');
      assert(await page.locator('#cb-adoption-title').isVisible());
      await page.goForward();
      await page.waitForFunction(() => document.querySelector('#cb-tab-scopes-regions').getAttribute('aria-selected') === 'true');
      assert.strictEqual(calls.length, before, 'display categories and history must reuse the loaded response');
      await page.getByRole('tab', {name:'Listener activity', exact:true}).click();
      assert.match(await page.locator('#cb-intro').innerText(), /where packets cross.*scopes/i);
      await page.locator('#cb-activity-chart').waitFor({state:'visible'});
      assert(await page.locator('#cb-activity-chart').isVisible());
    });
    await check('plain-language overview keeps technical details in a keyboard-accessible shareable disclosure', async () => {
      assert.match(await page.locator('#cb-intro').innerText(), /where packets cross.*scopes/i);
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
      assert.match(await page.locator('.cb-filter-help').last().innerText(), /Regions select listener sources.*Radio load includes all scopes/);
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
      const beforeBack = calls.length;
      await page.goBack(); await ready();
      assert.strictEqual(calls.length, beforeBack + 1, 'changed data filters must fetch their own snapshot');
      assert.strictEqual(await page.inputValue('#cb-peer'), 'BBB');
      assert.strictEqual(await page.inputValue('#cb-window'), '60');
      await page.goForward(); await ready();
      assert.strictEqual(calls.length, beforeBack + 2, 'restored data filters must not be treated as display-only history');
      assert.strictEqual(await page.inputValue('#cb-peer'), 'CCC');
      assert.strictEqual(await page.inputValue('#cb-window'), '180');
    });
    await check('invalid IATA is rejected locally and HTTP failures stay recoverable', async () => {
      const before = calls.length;
      await apply({ peer: 'not-a-code' });
      assert.strictEqual(calls.length, before);
      assert.match(await page.locator('#cb-status').innerText(), /three-character IATA/);
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
      assert.match(await page.locator('#cb-dashboard-coverage').textContent(), /capped/);
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
    await check('scope-name matching and catalog limits cannot present a complete comparison or zero absence', async () => {
      await tab('Crossings');
      for(const [nextMode,warning] of [['scope-match-cap',/Scope-name matching is limited.*not proof of no traffic/],['scope-catalog-cap',/scope catalog is limited.*names may be omitted/]]) {
        mode=nextMode;await page.click('#cb-refresh');await ready();
        assert.match(await page.locator('#cb-status').innerText(),/Partial comparison/);
        assert(await page.locator('#cb-quality-warning').isVisible());
        assert.match(await page.locator('#cb-quality-warning').innerText(),warning);
        assert.match(await page.locator('#cb-flow-status').innerText(),/Partial path evidence.*Scope-name evidence is incomplete/);
      }
      await tab('Scopes & regions');assert.match(await page.locator('#cb-catalog-status').innerText(),/Catalog limited; not all names are listed/);
      mode='unexplained-partial';await page.click('#cb-refresh');await ready();
      assert.match(await page.locator('#cb-status').innerText(),/Partial comparison/);
      assert.match(await page.locator('#cb-quality-warning').innerText(),/comparison evidence is incomplete/);
      mode='normal';await page.click('#cb-refresh');await ready();await tab('Crossings');
    });
    await check('incomplete repeater identity coverage shows unavailable attribution rather than false zero crossings', async () => {
      await tab('Crossings');mode='flow-identity-incomplete';await page.click('#cb-refresh');await ready();
      assert.strictEqual(await page.locator('#cb-flow-ca_us-count').innerText(),'—');
      assert.strictEqual(await page.locator('#cb-flow-us_ca-count').innerText(),'—');
      assert.strictEqual(await page.locator('#cb-flow-ca_us-scoped').innerText(),'—');
      assert.match(await page.locator('#cb-flow-status').innerText(),/identity coverage is incomplete/);
      assert.match(await page.locator('#cb-flow-map-status').innerText(),/identity coverage incomplete/);
      assert.match(await page.locator('#cb-quality-warning').innerText(),/Repeater identity coverage is incomplete/);
      assert(await page.evaluate(()=>Chart.getChart('cb-flow-timeline').data.datasets.every(dataset=>dataset.data.every(value=>value===null))));
      assert.strictEqual(await page.locator('#cb-flow-path-list button').count(),0);
      mode='empty';await page.click('#cb-refresh');await ready();
      assert.strictEqual(await page.locator('#cb-flow-ca_us-count').innerText(),'0','complete empty evidence remains a real zero');
      mode='normal';await page.click('#cb-refresh');await ready();
    });
    await check('after-snapshot observations show a neutral quality warning and unknown freshness', async () => {
      mode = 'after-snapshot'; await page.click('#cb-refresh'); await ready();
      assert.match(await page.locator('#cb-quality-warning').innerText(), /after the snapshot cutoff/);
      await page.locator('#cb-source-details > summary').click();
      await page.waitForFunction(() => location.hash.includes('sources=1'));
      assert.match(await page.locator('#cb-peer-source').innerText(), /Only after-snapshot observations.*\n.*\nLatest observation: Unknown · Age unknown/);
      assert.doesNotMatch(await page.locator('#cb-quality-warning').innerText(), /clock (fault|skew|error)/i);
      await page.locator('#cb-source-details > summary').click();
      mode = 'normal';
    });
    await check('charts show separate adoption denominators, gaps and distinct scope categories', async () => {
      mode = 'normal'; await page.click('#cb-refresh'); await ready();
      await tab('Hash widths');
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-advert-chart').data.datasets.map(ds => ds.data)), [[20,70],[70,20],[10,10]]);
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-width-chart').data.datasets.map(ds => ds.data)), [[20,70],[65,20],[15,10]]);
      assert(await page.evaluate(() => Chart.getChart('cb-local-trend').data.datasets.every(ds => ds.data[0] === null && ds.spanGaps === false)));
      await tab('Scopes & regions');
      assert.match(await page.locator('#cb-scope-values').innerText(), /Transport wildcard/);
      await tab('Radio load');
      assert.match(await page.locator('#cb-radio-values').innerText(), /4 reporting radios/);
      assert.match(await page.locator('#cb-radio-values').innerText(), /Latest available report: (?!Unknown)/);
      assert.deepStrictEqual(await page.evaluate(() => {
        const footer = Chart.getChart('cb-local-trend').options.plugins.tooltip.callbacks.footer;
        return [footer([{dataIndex:1}]), footer([{dataIndex:0}])];
      }), ['Qualifying flood records: 100', 'Qualifying flood records: Unknown']);
      await tab('Hash widths');
      const trendMethod = page.getByText('Time-series values and method', { exact: true });
      await trendMethod.click();
      assert(await page.locator('#cb-trend-values').isVisible());
      const trendValues = await page.locator('#cb-trend-values tr').nth(1).locator('td').allTextContents();
      assert.strictEqual(trendValues[0], 'Canadian listeners');
      assert.deepStrictEqual(trendValues.slice(2), ['100', '47%', '33%', '20%']);
      assert.strictEqual(await page.locator('#cb-trend-values tr').first().locator('td').nth(2).innerText(), 'Unknown');
      await trendMethod.click();
      // Leaflet owns a separate vector canvas; count the actual Chart.js
      // instances rather than mistaking the map renderer for another chart.
      assert.strictEqual(await page.evaluate(() => Array.from(document.querySelectorAll('#cb-dashboard canvas')).filter(canvas => Chart.getChart(canvas)).length), 13);
      for (const [view, ids] of [['Listener activity',['cb-activity-chart','cb-local-donut','cb-peer-donut']], ['Hash widths',['cb-advert-chart','cb-width-chart','cb-local-trend','cb-peer-trend']], ['Scopes & regions',['cb-scope-chart']], ['Radio load',['cb-radio-chart']]]) {
        await tab(view);
        await page.waitForFunction(chartIds => chartIds.every(id => { const chart = Chart.getChart(id); return chart.width > 100 && chart.height > 100; }), ids);
      }
      await tab('Hash widths');
      await page.evaluate(() => { window.__keptBorderChart = Chart.getChart('cb-advert-chart'); });
      await page.click('#cb-refresh'); await ready();
      assert(await page.evaluate(() => window.__keptBorderChart === Chart.getChart('cb-advert-chart')), 'refresh must update charts in place');
      const scopeColors = await page.evaluate(() => Chart.getChart('cb-scope-chart').data.datasets.map(ds => [ds.label, ds.backgroundColor]));
      mode = 'scope-shift'; await page.click('#cb-refresh'); await ready();
      assert.deepStrictEqual(await page.evaluate(() => Chart.getChart('cb-scope-chart').data.datasets.map(ds => [ds.label, ds.backgroundColor])), scopeColors, 'scope colors must not change when counts move');
      mode = 'normal';
    });
    await check('repeater matrix searches and drill-downs without another request', async () => {
      await tab('Repeaters');
      const before = calls.length;
      assert.strictEqual(await page.locator('#cb-border-repeaters tr').count(),2);
      assert.match(await page.locator('#cb-border-repeaters').innerText(),/Canadian fixture relay/);
      await page.locator('#cb-border-repeaters button').first().click();
      assert.strictEqual(await page.getByRole('tab',{name:'Crossings',exact:true}).getAttribute('aria-selected'),'true');
      assert.match(page.url(),/path=crossing/);
      assert(await page.locator('#cb-flow-hop-list li').count()>0);
      assert.strictEqual(calls.length,before);
      await tab('Repeaters');
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
      await tab('Scopes & regions');
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
      await page.evaluate(() => { window.__borderMapElement = document.querySelector('#cb-flow-map'); });
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
      assert(await page.evaluate(() => !window.__borderMapElement._leaflet_id), 'route exit removes the owned Leaflet map');
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
      await page.locator('#cb-local-chips button').first().focus(); await page.keyboard.press('Tab');
      assert.strictEqual(await page.evaluate(() => document.activeElement.id), 'cb-local-add');
      await page.locator('#cb-window-pills button[data-window="180"]').focus(); await page.keyboard.press('Enter'); await ready();
      assert.match(page.url(), /windowMinutes=180/);
      if (process.env.CROSS_BORDER_ARTIFACT_DIR) {
        fs.mkdirSync(process.env.CROSS_BORDER_ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-mobile.png'), fullPage: true });
        await tab('Hash widths');
        await page.locator('#cb-adoption-title').scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-mobile-adoption.png') });
        await tab('Repeaters');
        await page.locator('#cb-carry-title').scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-mobile-repeaters.png') });
        await page.setViewportSize({ width: 1440, height: 1050 });
        await tab('Crossings');
        await page.reload(); await ready();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-desktop.png'), fullPage: true });
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-desktop-top.png') });
        await page.evaluate(() => { document.documentElement.setAttribute('data-theme', 'dark'); window.dispatchEvent(new Event('theme-changed')); });
        await drawnPaths();
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-dark.png'), fullPage: true });
        await tab('Hash widths');
        assert.strictEqual(await page.locator('#cb-analytics-tabs .active').count(), 1);
        assert.strictEqual(await page.locator('#cb-analytics-tabs [aria-selected="true"]').count(), 1);
        assert(await page.locator('#cb-analytics-tabs').evaluate(node => {
          const selected = node.querySelector('[aria-selected="true"]');
          const selectedColor = getComputedStyle(selected).backgroundColor;
          const cardColor = getComputedStyle(document.querySelector('.analytics-card')).backgroundColor;
          return Array.from(node.querySelectorAll('[aria-selected="false"]')).every(button => getComputedStyle(button).backgroundColor === cardColor && getComputedStyle(button).backgroundColor !== selectedColor);
        }), 'inactive dark tabs must use the card surface, not an active-looking fill');
        await page.screenshot({ path: path.join(process.env.CROSS_BORDER_ARTIFACT_DIR, 'border-links-dark-hashes.png'), fullPage: true });
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
