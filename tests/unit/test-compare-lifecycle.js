// Real compare.js with held requests and a controllable safety deadline.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../../public/compare.js'), 'utf8');
const settle = () => new Promise(resolve => setImmediate(resolve));
const observerData = { observers: [{ id: 'A', name: 'Alpha' }, { id: 'B', name: 'Beta' }, { id: 'C', name: 'Gamma' }] };
const packetSummary = (packets = [], truncated = false, limit = 10000) => ({ packets, truncated, limit });
function harness(ignoreAbort = false) {
  let page, nextTimer = 0;
  const requests = [], timers = new Map(), elements = {};
  function element(id) {
    return elements[id] ||= { innerHTML: '', value: '', listeners: {}, style: {},
      classList: { toggle() {}, add() {}, remove() {} },
      addEventListener(name, fn) { this.listeners[name] = fn; },
      setAttribute() {}, removeAttribute() {}, querySelectorAll() { return []; },
      querySelector() { return elements.retry; }, focus() {} };
  }
  ['app', 'compareControls', 'compareContent', 'compareObsA', 'compareObsB', 'compareRouteFilter', 'compareDetail', 'retry'].forEach(element);
  function fetch(url, options = {}) {
    return new Promise((resolve, reject) => {
      const request = { url, options, aborted: false,
        reply(data, status = 200) { resolve({ ok: status >= 200 && status < 300, status, json: async () => data }); }, reject };
      requests.push(request);
      options.signal?.addEventListener('abort', () => {
        request.aborted = true;
        if (!ignoreAbort) reject(Object.assign(new Error('cancelled'), { name: 'AbortError' }));
      });
    });
  }
  const sandbox = {
    window: {}, document: { getElementById: element, querySelector: () => null },
    location: { hash: '#/compare?a=A&b=B' }, history: { replaceState() {} },
    registerPage(name, mod) { page = mod; }, fetch,
    api: async p => (await fetch('/api' + p)).json(), CLIENT_TTL: { observers: 0 },
    AbortController, URLSearchParams, Map, Set, Date, Promise, console,
    escapeHtml: v => String(v), timeAgo: () => '', payloadTypeColor: () => '',
    setTimeout(fn, ms) { const id = ++nextTimer; timers.set(id, { fn, ms }); return id; },
    clearTimeout(id) { timers.delete(id); },
  };
  vm.createContext(sandbox); vm.runInContext(source, sandbox);
  return { page, requests, timers, elements, sandbox,
    init() { page.init(elements.app); },
    async ready() { this.init(); requests[0].reply(observerData); await settle(); },
    deadline() { const timer = [...timers.values()].find(t => t.ms === 30000); assert(timer, 'A fixed 30-second safety deadline is required'); timer.fn(); },
  };
}
let count = 0;
async function test(name, fn) { await fn(); count++; console.log('PASS ' + name); }
(async () => {
  await test('24-hour, 10000-packet requests are abortable and retain same-origin credentials', async () => {
    const h = harness(); await h.ready(); assert.equal(h.requests.length, 3);
    for (const request of h.requests.slice(1)) {
      assert(request.options.signal, 'Packet request must be abortable');
      assert.equal(request.options.credentials, 'same-origin');
      const query = new URL(request.url, 'http://localhost').searchParams;
      assert.equal(new URL(request.url, 'http://localhost').pathname, '/api/packets/summary');
      assert.equal(query.get('limit'), '10000');
      assert(Math.abs(Date.now() - Date.parse(query.get('since')) - 86400000) < 1000);
    }
    h.page.destroy(); await settle();
    assert(h.requests.slice(1).every(r => r.aborted)); assert.equal(h.timers.size, 0);
  });
  await test('deadline aborts both requests, shows retry, and never retries automatically', async () => {
    const h = harness(); await h.ready(); h.deadline(); await settle();
    assert(h.requests.slice(1).every(r => r.aborted));
    assert.match(h.elements.compareContent.innerHTML, /timed out/i);
    assert.match(h.elements.compareContent.innerHTML, /Retry/);
    assert(!h.elements.compareContent.innerHTML.includes('compare-results'));
    assert.equal(h.requests.length, 3); assert.equal(h.timers.size, 0);
    h.elements.retry.listeners.click(); await settle(); assert.equal(h.requests.length, 5);
    h.requests[3].reply(packetSummary()); h.requests[4].reply(packetSummary()); await settle();
    assert.match(h.elements.compareContent.innerHTML, /compare-results/); assert.equal(h.timers.size, 0);
  });
  await test('selection changes abort obsolete packets and ignore late results', async () => {
    const h = harness(true); await h.ready();
    h.elements.compareObsB.value = 'C'; h.elements.compareObsB.listeners.change();
    assert(h.requests[1].aborted && h.requests[2].aborted);
    h.requests[3].reply(packetSummary([{ hash: 'new', route_type: 2 }])); h.requests[4].reply(packetSummary()); await settle();
    const result = h.elements.compareContent.innerHTML; assert.match(result, /Gamma/);
    h.requests[1].reply(packetSummary()); h.requests[2].reply(packetSummary()); await settle();
    assert.equal(h.elements.compareContent.innerHTML, result);
  });
  await test('a timed-out response cannot later become a successful comparison', async () => {
    const h = harness(true); await h.ready(); h.deadline();
    h.requests[1].reply(packetSummary()); h.requests[2].reply(packetSummary()); await settle();
    assert.match(h.elements.compareContent.innerHTML, /timed out/i);
    assert(!h.elements.compareContent.innerHTML.includes('compare-results'));
  });
  await test('clearing selection cancels work and clears stale comparison data', async () => {
    const h = harness(true); await h.ready();
    h.elements.compareObsB.value = ''; h.elements.compareObsB.listeners.change();
    assert(h.requests[1].aborted && h.requests[2].aborted);
    const current = h.elements.compareContent.innerHTML;
    assert(!current.includes('Fetching packets')); assert(!current.includes('compare-results'));
    h.requests[1].reply(packetSummary()); h.requests[2].reply(packetSummary()); await settle();
    assert.equal(h.elements.compareContent.innerHTML, current);
  });
  await test('selecting the same observer twice cancels without another packet request', async () => {
    const h = harness(); await h.ready();
    h.elements.compareObsB.value = 'A'; h.elements.compareObsB.listeners.change(); await settle();
    assert(h.requests[1].aborted && h.requests[2].aborted);
    assert.equal(h.requests.length, 3); assert.equal(h.timers.size, 0);
    assert.match(h.elements.compareContent.innerHTML, /Choose two different observers/);
  });
  await test('route exit and remount reject stale observer discovery', async () => {
    const h = harness(true); h.init(); h.page.destroy(); h.init();
    assert(h.requests[0].aborted); h.requests[1].reply(observerData); await settle();
    const requests = h.requests.length, controls = h.elements.compareControls.innerHTML;
    h.requests[0].reply({ observers: [{ id: 'old', name: 'Old observer' }] }); await settle();
    assert.equal(h.requests.length, requests); assert.equal(h.elements.compareControls.innerHTML, controls);
    h.page.destroy();
  });
  await test('route exit cannot be overwritten by a late packet response', async () => {
    const h = harness(true); await h.ready(); h.page.destroy();
    h.elements.compareContent.innerHTML = 'Another page';
    h.requests[1].reply(packetSummary()); h.requests[2].reply(packetSummary()); await settle();
    assert.equal(h.elements.compareContent.innerHTML, 'Another page'); assert.equal(h.timers.size, 0);
  });
  for (const malformed of [false, true]) await test((malformed ? 'malformed' : 'HTTP failure') + ' rejects unavailable data without fabricated zero counts', async () => {
    const h = harness(); await h.ready();
    h.requests[1].reply(malformed ? {} : { error: '/api/private-path' }, malformed ? 200 : 524);
    h.requests[2].reply(packetSummary()); await settle();
    assert.match(h.elements.compareContent.innerHTML, /Retry/);
    assert(!h.elements.compareContent.innerHTML.includes('/api/'));
    assert(!h.elements.compareContent.innerHTML.includes('compare-results'));
  });
  await test('observer discovery timeout has a manual retry and no obsolete callbacks', async () => {
    const h = harness(); h.init(); h.deadline(); await settle();
    assert.match(h.elements.compareControls.innerHTML, /timed out/i);
    h.elements.retry.listeners.click(); assert.equal(h.requests.length, 2); h.page.destroy(); await settle();
  });
  for (const side of ['A', 'B', 'both']) await test('truncated ' + side + ' labels sampled counts and never claims full-period overlap', async () => {
    const h = harness(); await h.ready();
    const same = [{ hash: 'shared', route_type: 1 }];
    h.requests[1].reply(packetSummary(same, side !== 'B', 1));
    h.requests[2].reply(packetSummary(same, side !== 'A', 1)); await settle();
    const html = h.elements.compareContent.innerHTML;
    assert.match(html, /Limited 24-hour sample/);
    if (side !== 'B') assert.match(html, /Alpha: limited to 1 packet/);
    if (side !== 'A') assert.match(html, /Beta: limited to 1 packet/);
    assert.match(html, /Full-period totals are unknown/);
    assert.match(h.elements.compareDetail.innerHTML, /returned 24-hour samples/);
    assert(!h.elements.compareDetail.innerHTML.includes('Perfect overlap'));
    assert(!h.elements.compareDetail.innerHTML.includes('both observers see the same packets'));
  });
  await test('unreported truncation fails closed instead of claiming complete totals', async () => {
    const h = harness(); await h.ready();
    h.requests[1].reply({ packets: [], limit: 10000 }); h.requests[2].reply(packetSummary()); await settle();
    assert.match(h.elements.compareContent.innerHTML, /Retry/);
    assert(!h.elements.compareContent.innerHTML.includes('compare-results'));
  });
  await test('disjoint limited samples do not imply incompatible radios or full-period isolation', async () => {
    const h = harness(); await h.ready();
    h.requests[1].reply(packetSummary([{ hash: 'a' }], true, 1));
    h.requests[2].reply(packetSummary([{ hash: 'b' }], false, 1)); await settle();
    assert.match(h.elements.compareDetail.innerHTML, /returned samples share no packets/);
    assert.match(h.elements.compareDetail.innerHTML, /Full-period overlap is unknown/);
    assert(!h.elements.compareDetail.innerHTML.includes('different frequencies'));
  });
  console.log(`${count} comparison lifecycle checks passed`);
})().catch(error => { console.error(error); process.exitCode = 1; });
