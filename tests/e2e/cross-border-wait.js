'use strict';

module.exports = async function waitForPending(hasPending, timeoutMs = 10000) {
  const deadline = performance.now() + timeoutMs;
  while (!hasPending()) {
    if (performance.now() >= deadline) throw new Error('Timed out waiting for held cross-border request');
    await new Promise(resolve => setTimeout(resolve, 10));
  }
};
