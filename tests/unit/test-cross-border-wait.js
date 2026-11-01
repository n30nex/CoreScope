'use strict';
const assert = require('assert');
const waitForPending = require('../e2e/cross-border-wait');

(async () => {
  let cleanedUp = false;
  const start = performance.now();
  await assert.rejects(async () => {
    try { await waitForPending(() => false, 20); }
    finally { cleanedUp = true; }
  }, /Timed out waiting for held cross-border request/);
  assert(cleanedUp, 'missing requests must reach finally cleanup');
  assert(performance.now() - start < 1000, 'negative condition must terminate promptly');
  await waitForPending(() => true, 20);
  console.log('PASS pending request wait is bounded, descriptive, and reaches cleanup');
})().catch(error => { console.error(error); process.exitCode = 1; });
