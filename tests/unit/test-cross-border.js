'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');
const { normalizeFilters, numberLabel, scopeLabel, ageAt, uniqueRows, qualityNotice } = require('../../public/cross-border');
const dashboard = require('../../public/cross-border-dashboard');
let failures = 0;
function test(name, fn) {
  try { fn(); console.log('PASS ' + name); }
  catch (error) { failures++; console.error('FAIL ' + name + ': ' + error.message); }
}

test('filters preserve explicit regions and named scope with bounded windows', () => {
  assert.deepStrictEqual(normalizeFilters({ localIata: ' aaa, bbb,AAA ', peerIata: ['ccc'], scope: 'example', windowMinutes: '180', packet: 'abc' }),
    { localIata: 'AAA,BBB', peerIata: 'CCC', scope: 'example', windowMinutes: 180, packet: 'abc', quality: '' });
  assert.deepStrictEqual(normalizeFilters({ windowMinutes: '9999' }),
    { localIata: '', peerIata: '', scope: 'all', windowMinutes: '', packet: '', quality: '' });
  assert.strictEqual(normalizeFilters({ quality: '1' }).quality, '1');
});
test('missing or invalid windows defer to the server while explicit valid windows survive', () => {
  [undefined, null, '', 'bad', 9999].forEach(windowMinutes => assert.strictEqual(normalizeFilters({ windowMinutes }).windowMinutes, ''));
  [15, 60, 180, 360].forEach(windowMinutes => assert.strictEqual(normalizeFilters({ windowMinutes: String(windowMinutes) }).windowMinutes, windowMinutes));
});
test('unknown numbers never become zero and legitimate zero is retained', () => {
  [null, undefined, '', false, NaN, Infinity, 'bad'].forEach(value => assert.strictEqual(numberLabel(value), 'Unknown'));
  assert.strictEqual(numberLabel(0), '0');
  assert.strictEqual(numberLabel('2'), '2');
});
test('scope evidence distinguishes unscoped from unknown', () => {
  assert.strictEqual(scopeLabel(null), 'Unknown scope');
  assert.strictEqual(scopeLabel({ kind: 'unknown' }), 'Unknown scope');
  assert.strictEqual(scopeLabel({ kind: 'unscoped' }), 'Unscoped');
  assert.strictEqual(scopeLabel({ kind: 'named', name: 'example' }), 'example');
});
test('ages are relative to the snapshot and do not hide missing or future clocks', () => {
  assert.strictEqual(ageAt('2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z'), '1m ago');
  assert.strictEqual(ageAt(null, '2026-01-01T00:01:00Z'), 'Age unknown');
  assert.strictEqual(ageAt('invalid', '2026-01-01T00:01:00Z'), 'Age unknown');
  assert.strictEqual(ageAt('2026-01-01T00:02:00Z', '2026-01-01T00:01:00Z'), 'After snapshot');
});
test('row bounds deduplicate identities and reject absent identities', () => {
  assert.deepStrictEqual(uniqueRows([{ id: 'a' }, { id: 'a' }, { id: null }, {}, null, { id: 'b' }, { id: 'c' }], 'id', 2), [{ id: 'a' }, { id: 'b' }]);
  assert.deepStrictEqual(uniqueRows(null, 'id', 2), []);
});
test('partial, stale, unavailable and capped evidence stays prominent outside the disclosure', () => {
  assert.match(qualityNotice({ enabled: true, status: 'partial', completeness: { complete: false }, sources: { peer: { status: 'stale' }, local: { status: 'unavailable' } } }), /Partial comparison.*unavailable.*stale/);
  assert.match(qualityNotice({ enabled: true, status: 'ready', completeness: { complete: true }, sources: { peer: { observationsTruncated: true } } }), /sample limits/);
  assert.strictEqual(qualityNotice({ enabled: true, status: 'ready', completeness: { complete: true }, sources: {} }), '');
  assert.strictEqual(qualityNotice({ enabled: false, status: 'disabled' }), '');
});
test('incomplete observer identity checks produce a visible warning', () => {
  assert.match(qualityNotice({ enabled: true, status: 'ready', completeness: { complete: true }, summary: { identityUnverifiedPackets: 1 } }), /Observer identity check incomplete/);
  assert.strictEqual(qualityNotice({ enabled: true, status: 'ready', completeness: { complete: true }, summary: { identityUnverifiedPackets: 0 } }), '');
});
test('after-snapshot evidence is visible without declaring a clock fault', () => {
  const notice = qualityNotice({ enabled: true, status: 'partial', completeness: { complete: false },
    sources: { peer: { status: 'after_snapshot', latestAfterSnapshotAt: '2026-01-01T00:02:00Z' } } });
  assert.match(notice, /after the snapshot cutoff/);
  assert.doesNotMatch(notice, /clock (fault|skew|error)/i);
});
test('complete overview is not marked partial merely because packet detail rows are capped', () => {
  const data = { enabled: true, status: 'partial', completeness: { complete: false, packetsTruncated: true },
    dashboard: { local: { status: 'available', nodeIndexComplete: true }, peer: { status: 'available', nodeIndexComplete: true }, identityComplete: true } };
  assert.strictEqual(qualityNotice(data), '');
  data.dashboard.local.truncated = true;
  assert.match(qualityNotice(data), /sample limits.*gaps remain unknown/);
});
test('capped region pairs remain a visible overview limitation', () => {
  const data = { enabled: true, status: 'partial', dashboard: { identityComplete: true, regionPairsTruncated: true,
    local: { status: 'available' }, peer: { status: 'available' } } };
  assert.match(qualityNotice(data), /Partial comparison.*Region comparisons are limited/);
});
['local', 'peer'].forEach(side => test(side + ' airtime cap remains a visible overview limitation', () => {
  const data = { enabled: true, status: 'partial', dashboard: { identityComplete: true,
    local: { status: 'available', radio: { truncated: false } }, peer: { status: 'available', radio: { truncated: false } } } };
  data.dashboard[side].radio.truncated = true;
  assert.match(qualityNotice(data), /Partial comparison.*Airtime reports are limited/);
}));
test('browser suite refuses a non-loopback server before opening a browser', () => {
  const result = spawnSync(process.execPath, [path.resolve(__dirname, '../e2e/test-cross-border-e2e.js')], {
    encoding: 'utf8', timeout: 15000, env: { ...process.env, BASE_URL: 'https://example.org', CROSS_BORDER_ARTIFACT_DIR: '' }
  });
  assert.notStrictEqual(result.status, 0);
  assert.match(result.stderr, /BASE_URL must be an HTTP\(S\) loopback origin/);
});
test('CI executes the browser suite on the coverage shard', () => {
  const workflow = fs.readFileSync(path.resolve(__dirname, '../../.github/workflows/deploy.yml'), 'utf8');
  assert.strictEqual((workflow.match(/^\s+suite 2 test-cross-border-e2e\.js BASE_URL=\$B\s*$/gm) || []).length, 1);
});
test('advert adoption and carried-width shares use separate denominators', () => {
  const source = { status: 'available', widths: [{ bytes: 1, records: 90, advertisers: 2 }, { bytes: 2, records: 10, advertisers: 8 }, { bytes: 3, records: 0, advertisers: 0 }] };
  assert.deepStrictEqual(dashboard.widthSummary(source, 'advertisers').map(r => r.percent), [20, 80, 0]);
  assert.deepStrictEqual(dashboard.widthSummary(source, 'records').map(r => r.percent), [90, 10, 0]);
  assert(dashboard.widthSummary({ status: 'unavailable' }, 'records').every(r => r.percent === null));
  assert(dashboard.widthSummary({ status: 'empty', widths: [] }, 'records').every(r => r.percent === null));
});
test('timeline gaps and incomplete buckets stay null instead of false zero', () => {
  const source = { timeline: [{ at: '2026-01-01T00:00:00Z', complete: false, oneByte: 50, twoByte: 0, threeByte: 0 }, { at: '2026-01-01T00:05:00Z', complete: true, oneByte: 1, twoByte: 3, threeByte: 0 }, { at: '2026-01-01T00:10:00Z', complete: true, oneByte: 0, twoByte: 0, threeByte: 0 }] };
  const result = dashboard.timelineData(source);
  assert.deepStrictEqual(result.series.map(s => s.values), [[null, 25, null], [null, 75, null], [null, 0, null]]);
  assert.deepStrictEqual(result.counts, [null, 4, 0], 'qualifying-record denominator must preserve missing and measured zero separately');
});
test('radio freshness uses the latest valid report and preserves unavailable metadata', () => {
  assert.strictEqual(typeof dashboard.latestRadioReport, 'function');
  assert.strictEqual(dashboard.latestRadioReport({status:'available', observers:[{latestAt:null}, {latestAt:'bad'}, {latestAt:'2026-01-01T00:05:00Z'}, {latestAt:'2026-01-01T01:10:00+01:00'}]}), '2026-01-01T01:10:00+01:00');
  assert.strictEqual(dashboard.latestRadioReport({status:'available', observers:[]}), null);
  assert.strictEqual(dashboard.latestRadioReport({status:'unavailable', observers:[{latestAt:'2026-01-01T00:05:00Z'}]}), null);
});
test('scope comparison retains transport wildcard and unknown separately', () => {
  const source = { status: 'available', scopes: [{ key: 'unscoped', label: 'Unscoped flood', records: 4 }, { key: 'transport-zero', label: 'Transport wildcard', records: 2 }, { key: 'unknown', label: 'Unresolved scope', records: 1 }] };
  const result = dashboard.scopeComparison(source, { status: 'unavailable' });
  assert.strictEqual(result.labels.length, 3);
  assert(result.labels.includes('Transport wildcard'));
  assert(Array.isArray(result.keys), 'scope keys must remain inspectable for stable series colors');
  assert.deepStrictEqual(result.peer, [null, null, null]);
  assert(Math.abs(result.local.reduce((a, b) => a + b, 0) - 100) < .001);
  const reversed = JSON.parse(JSON.stringify(source));
  reversed.scopes[0].records = 1; reversed.scopes[2].records = 40;
  assert.deepStrictEqual(dashboard.scopeComparison(reversed, {}).keys, result.keys, 'scope identity order must not follow changing counts');
});
test('carried scopes do not mistake transport wildcard for ordinary unscoped flooding', () => {
  assert.strictEqual(typeof dashboard.carriedScopes, 'function');
  assert.deepStrictEqual(dashboard.carriedScopes({observedScopes:[{kind:'unscoped'}], observedUnscoped:false, observedTransportWildcard:true}), ['Transport wildcard']);
  assert.deepStrictEqual(dashboard.carriedScopes({observedScopes:[{kind:'named',name:'example'}], observedUnscoped:true, observedTransportWildcard:true}), ['example', 'Unscoped flood', 'Transport wildcard']);
});
test('radio ranking preserves zero, omits unknown scores and has a hard bound', () => {
  const radio = { observers: [{ id: 'unknown', txAirtimePct: null, rxAirtimePct: null }, { id: 'zero', txAirtimePct: 0, rxAirtimePct: 0 }, { id: 'busy', txAirtimePct: 12, rxAirtimePct: 5 }] };
  const rows = dashboard.radioRanking({ radio }, { radio: { observers: [] } });
  assert.deepStrictEqual(rows.map(r => r.id), ['busy', 'zero']);
  assert(dashboard.radioRanking({ radio: { observers: Array.from({length: 1000}, (_, i) => ({id: String(i), txAirtimePct: i % 100})) } }, {}).length <= 10);
});
test('carrying states do not turn one-byte ambiguity or absence into blocking', () => {
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [2], sampleForwardedPackets: 4 }, 2), 'Observed');
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [2], sampleForwardedPackets: 4 }, 3), 'Not observed');
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [], carryingAmbiguous: true, ambiguousWidths: [2] }, 2), 'Ambiguous');
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [], carryingAmbiguous: true, ambiguousWidths: [1] }, 2), 'No evidence');
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [], ambiguousWidths: [1] }, 1), 'Ambiguous');
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [], ambiguousWidths: [] }, 1), 'No evidence');
  assert.strictEqual(dashboard.carryLabel({ hopWidths: [] }, 3), 'No evidence');
});
test('region matrix is bounded and unknown identity never becomes a zero match', () => {
  const input = { identityComplete: true, local: { status:'available', regions: [{iata:'AAA'}] }, peer: { status:'available', regions: [{iata:'BBB'}, {iata:'CCC'}] }, regionPairs: [{ localIata: 'AAA', peerIata: 'BBB', contents: 7 }] };
  assert.deepStrictEqual(dashboard.regionGrid(input).cells.map(c => c.value), [7, 0]);
  input.regionPairsTruncated = true;
  assert.deepStrictEqual(dashboard.regionGrid(input).cells.map(c => c.value), [7, null]);
  input.peer.status = 'unavailable';
  assert(dashboard.regionGrid(input).cells.every(c => c.value === null));
  input.peer.status = 'available';
  input.identityComplete = false;
  assert(dashboard.regionGrid(input).cells.every(c => c.value === null));
});
if (failures) process.exitCode = 1;
