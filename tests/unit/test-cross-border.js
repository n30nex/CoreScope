'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');
const { normalizeFilters, numberLabel, scopeLabel, ageAt, uniqueRows, qualityNotice } = require('../../public/cross-border');
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
if (failures) process.exitCode = 1;
