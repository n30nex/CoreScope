#!/usr/bin/env node
/* Brand theme/layout contract: Canadaverse emblem, readable text, existing
   theme preference, mobile navigation, and retained packet indicators. */
'use strict';
const assert = require('assert');
const { chromium } = require('playwright');
const BASE = process.env.BASE_URL || 'http://localhost:13581';
if (!['localhost', '127.0.0.1', '[::1]'].includes(new URL(BASE).hostname)) {
  throw new Error('Browser tests require a local fixture server');
}
function luminance(color) {
  const hex = color.trim().replace('#', '');
  const rgb = color.startsWith('#')
    ? [0, 2, 4].map(i => parseInt(hex.slice(i, i + 2), 16))
    : color.match(/[\d.]+/g).slice(0, 3).map(Number);
  return rgb.map(v => { v /= 255; return v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4; })
    .reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0);
}
function contrast(a, b) {
  const x = luminance(a), y = luminance(b);
  return (Math.max(x, y) + .05) / (Math.min(x, y) + .05);
}
(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  let passed = 0;
  async function check(name, fn) { await fn(); passed++; console.log('  ✓ ' + name); }
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await page.addInitScript(() => localStorage.setItem('meshcore-user-level', 'experienced'));
    await page.goto(BASE + '/#/home');
    await page.waitForFunction(() => window._customizerV2?.initDone);
    await page.locator('.home-hero').waitFor();
    for (const theme of ['light', 'dark']) {
      await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), theme);
      await check(theme + ': body, links and muted labels meet AA on all main surfaces', async () => {
        const colors = await page.evaluate(() => {
          const cs = getComputedStyle(document.documentElement);
          const names = ['text', 'text-muted', 'link-color', 'surface-0', 'surface-1', 'surface-2', 'nav-text', 'nav-bg', 'text-on-accent', 'accent-strong'];
          return Object.fromEntries(names.map(n => [n, cs.getPropertyValue('--' + n).trim()]));
        });
        for (const fg of ['text', 'text-muted', 'link-color']) {
          for (const bg of ['surface-0', 'surface-1', 'surface-2']) {
            assert(contrast(colors[fg], colors[bg]) >= 4.5, theme + ' ' + fg + '/' + bg + ' contrast below 4.5');
          }
        }
        assert(contrast(colors['nav-text'], colors['nav-bg']) >= 4.5);
        assert(contrast(colors['text-on-accent'], colors['accent-strong']) >= 4.5);
        const primary = await page.locator('.home-action').first().evaluate(el => {
          const cs = getComputedStyle(el);
          return { text: cs.color, background: cs.backgroundColor };
        });
        assert(contrast(primary.text, primary.background) >= 4.5, 'home primary action must meet AA');
      });
      await check(theme + ': emblem, hero and readable text remain visible', async () => {
        assert(await page.locator('.brand-text').isVisible());
        assert(await page.locator('.home-hero h1').isVisible());
        assert(await page.locator('.home-hero-logo').evaluate(img => img.complete && img.naturalWidth > 0));
        assert.equal(await page.locator('.nav-brand .brand-logo image').getAttribute('href'), 'brand/canadaverse-emblem.svg');
      });
    }
    await check('packet indicator colors still react to theme tokens', async () => {
      const fills = await page.evaluate(() => {
        document.documentElement.style.setProperty('--logo-accent', '#123456');
        document.documentElement.style.setProperty('--logo-accent-hi', '#abcdef');
        const values = ['a', 'b'].map(id => getComputedStyle(document.querySelector('.brand-logo .logo-node-' + id)).fill);
        document.documentElement.style.removeProperty('--logo-accent');
        document.documentElement.style.removeProperty('--logo-accent-hi');
        return values;
      });
      assert.deepEqual(fills, ['rgb(18, 52, 86)', 'rgb(171, 205, 239)']);
    });
    await check('light/dark switch retains the existing saved preference', async () => {
      const expected = await page.locator('#darkModeCheckbox').isChecked() ? 'light' : 'dark';
      await page.locator('#darkModeToggle').click();
      await page.reload();
      assert.equal(await page.evaluate(() => document.documentElement.dataset.theme), expected);
      assert.equal(await page.evaluate(() => localStorage.getItem('meshcore-theme')), expected);
    });
    for (const width of [320, 360, 390, 768]) {
      await page.setViewportSize({ width, height: 800 });
      await check(width + 'px: brand, home and bottom navigation fit without page overflow', async () => {
        const fit = await page.evaluate(() => {
          const brand = document.querySelector('.nav-brand').getBoundingClientRect();
          const hero = document.querySelector('.home-hero').getBoundingClientRect();
          return { brand: brand.right <= innerWidth, hero: hero.right <= innerWidth,
            page: document.documentElement.scrollWidth <= innerWidth,
            bottom: getComputedStyle(document.querySelector('.bottom-nav')).display !== 'none' };
        });
        assert.deepEqual(fit, { brand: true, hero: true, page: true, bottom: true });
      });
    }
    await check('mobile mark replaces desktop mark without clipping', async () => {
      await page.setViewportSize({ width: 360, height: 800 });
      assert(await page.locator('.brand-mark-only').isVisible());
      assert(!(await page.locator('.brand-logo').isVisible()));
    });
    await page.goto(BASE + '/#/live');
    await page.locator('#vcrLcdCanvas').waitFor();
    for (const width of [320, 360, 375, 390]) {
      await page.setViewportSize({ width, height: 800 });
      await check(width + 'px: VCR controls, scrubber and clock fit in one row', async () => {
        const layout = await page.evaluate(() => {
          const bar = document.querySelector('#vcrBar');
          const selectors = ['.vcr-controls', '.vcr-scope-btns', '.vcr-timeline-container', '.vcr-lcd'];
          return selectors.map(selector => {
            const el = bar.querySelector(selector);
            const rect = el.getBoundingClientRect();
            return { selector, parentIsBar: el.parentElement === bar, left: rect.left,
              right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width };
          });
        });
        for (const item of layout) {
          assert(item.parentIsBar, item.selector + ' must stay in the VCR row');
          assert(item.left >= 0 && item.right <= width, item.selector + ' clipped: ' + JSON.stringify(item));
          assert(item.top >= 0 && item.bottom <= 800, item.selector + ' vertically clipped');
          assert(item.top < layout[0].bottom && item.bottom > layout[0].top, item.selector + ' wrapped outside the controls row');
        }
        for (let i = 1; i < layout.length; i++) {
          assert(layout[i].left >= layout[i - 1].right, 'VCR items overlap');
        }
        assert(layout[2].width >= 24, 'timeline must retain a usable scrub target');
        assert(layout[3].width < 100, 'mobile clock must remain compact');
        const clock = await page.locator('#vcrLcdCanvas').boundingBox();
        assert(clock.width > 0 && clock.x >= layout[3].left && clock.x + clock.width <= layout[3].right, 'clock canvas clipped inside LCD');
        for (const button of await page.locator('.vcr-controls button, .vcr-scope-btn:visible').all()) {
          const box = await button.boundingBox();
          assert(box.width >= 24 && box.height >= 24, 'VCR button target below 24px');
        }
        await page.locator('#vcrPauseBtn').click();
        await page.waitForFunction(() => document.querySelector('#vcrLcdMode').textContent === 'PAUSE');
        await page.locator('#vcrLiveBtn').click();
        await page.waitForFunction(() => document.querySelector('#vcrLcdMode').textContent === 'LIVE');
      });
    }
    console.log(passed + ' brand theme/layout checks passed');
  } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
