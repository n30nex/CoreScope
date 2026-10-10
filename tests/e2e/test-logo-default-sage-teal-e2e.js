#!/usr/bin/env node
/* Historic filename retained for CI. The fork's default is now Canadaverse;
   explicit operator branding and visitor theme overrides must still win. */
'use strict';
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
if (!['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname)) {
  throw new Error('Browser tests require a local fixture server');
}
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
    await page.addInitScript(() => localStorage.setItem('meshcore-user-level', 'experienced'));
    await page.goto(BASE + '/#/home');
    await page.waitForFunction(() => window._customizerV2?.initDone);
    assert.equal(await page.title(), 'Canadaverse CoreScope');
    assert.equal(await page.locator('.brand-text').textContent(), 'Canadaverse CoreScope');
    assert.equal(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--surface-0').trim()), '#020706');
    console.log('  ✓ default identity and near-black palette');
    assert.equal(await page.locator('.home-hero-logo').getAttribute('src'), 'brand/canadaverse-emblem.svg');
    assert(await page.locator('.home-attribution a[href="https://github.com/Kpa-clawbot/CoreScope"]').isVisible());
    console.log('  ✓ shared emblem and upstream attribution');
    await page.evaluate(() => {
      window._customizerV2.writeOverrides({
        branding: { siteName: 'Operator mesh', logoUrl: '/img/corescope-logo.svg', homeUrl: 'https://example.org/' },
        home: { heroTitle: 'Operator dashboard' },
        themeDark: { accent: '#dc2626', accentHover: '#ef4444' }
      });
      window._customizerV2.runPipeline();
    });
    assert.equal(await page.title(), 'Operator mesh');
    assert.equal(await page.locator('.brand-text').textContent(), 'Operator mesh');
    assert.equal(await page.locator('.nav-brand').getAttribute('aria-label'), 'Operator mesh home');
    assert.equal(await page.locator('.nav-brand').getAttribute('href'), 'https://example.org/');
    assert.equal(await page.locator('.brand-logo').getAttribute('src'), '/img/corescope-logo.svg');
    assert.equal(await page.locator('.brand-mark-only').getAttribute('src'), '/img/corescope-logo.svg');
    assert.equal(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()), '#dc2626');
    console.log('  ✓ operator branding and explicit theme override');
    await page.reload();
    await page.waitForFunction(() => window._customizerV2?.initDone);
    assert.equal(await page.title(), 'Operator mesh');
    await page.evaluate(() => {
      window._customizerV2.clearOverride('branding', 'logoUrl');
      window._customizerV2.clearOverride('branding', 'siteName');
      window._customizerV2.clearOverride('branding', 'homeUrl');
    });
    assert.equal(await page.title(), 'Canadaverse CoreScope');
    assert.equal(await page.locator('.brand-text').textContent(), 'Canadaverse CoreScope');
    assert.equal(await page.locator('.nav-brand').getAttribute('href'), '#/');
    assert.equal(await page.locator('.brand-logo image').getAttribute('href'), 'brand/canadaverse-emblem.svg');
    assert.equal(await page.locator('.brand-mark-only image').getAttribute('href'), 'brand/canadaverse-emblem.svg');
    for (const selector of ['.brand-logo', '.brand-mark-only']) {
      assert.equal(await page.locator(selector + ' .logo-node-a').count(), 1, 'packet pulse remains available after reset');
    }
    console.log('  ✓ persistence and reset restore both default marks');

    await page.route('**/api/nodes/' + 'F'.repeat(64) + '/health', route => route.fulfill({
      status: 404, contentType: 'application/json', body: '{"error":"No telemetry in this fixture"}'
    }));
    await page.evaluate(() => localStorage.setItem('meshcore-my-nodes', JSON.stringify([{ pubkey: 'F'.repeat(64), name: 'Fixture' }])));
    await page.reload();
    await page.waitForFunction(() => window._customizerV2?.initDone);
    await page.locator('.mnc-remove').waitFor();
    assert.equal(await page.locator('.home-hero h1').textContent(), 'My Mesh');
    await page.locator('.mnc-remove').click();
    assert.equal(await page.locator('.home-hero h1').textContent(), 'Operator dashboard');
    assert.deepEqual(await page.locator('.home-action').evaluateAll(links => links.map(link => link.getAttribute('href'))), ['#/packets', '#/map', '#/nodes']);
    console.log('  ✓ removing the last node preserves the operator title and existing routes');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
