/* Exercise the real node analytics renderer with synthetic data. The 24-hour
 * heatmap must scroll locally when necessary, without widening the page. */
'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const PUBLIC = path.resolve(__dirname, '../../public');
const css = ['style.css', 'canadaverse.css'].map(file => fs.readFileSync(path.join(PUBLIC, file), 'utf8')).join('\n');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  let failures = 0;
  try {
    const page = await browser.newPage();
    await page.setContent('<style>' + css + '</style><main id="app"></main>');
    await page.evaluate(() => {
      window.Chart = class { static defaults = {}; destroy() {} };
      window.NodeHopAnalytics = { load() {}, destroy() {} };
      window.CLIENT_TTL = {};
      window.escapeHtml = String;
      window.api = async () => ({
        node: { public_key: '0'.repeat(64), name: 'Test node', role: 'repeater' },
        computedStats: { totalPackets: 1, availabilityPct: 1, signalGrade: 'A', avgPacketsPerDay: 1, uniqueObservers: 1, relayPct: 0 },
        peerInteractions: [], activityTimeline: [], snrTrend: [], packetTypeBreakdown: [], observerCoverage: [], hopDistribution: [],
        uptimeHeatmap: [{ dayOfWeek: 0, hour: 23, count: 1 }], samples: [],
      });
      window.registerPage = (_, renderer) => { window.testNodeAnalytics = renderer; };
    });
    await page.addScriptTag({ path: path.join(PUBLIC, 'node-analytics.js') });
    await page.evaluate(() => window.testNodeAnalytics.init(document.getElementById('app'), '0'.repeat(64) + '/analytics'));
    await page.waitForFunction(() => document.querySelectorAll('.analytics-heatmap-cell').length === 168);

    for (const theme of ['light', 'dark']) {
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      for (const width of [320, 375, 600, 1280]) {
        await page.setViewportSize({ width, height: 800 });
        const geometry = await page.locator('#heatmapGrid').evaluate(grid => {
          grid.scrollLeft = grid.scrollWidth;
          const r = grid.getBoundingClientRect();
          const lastHour = grid.children[24].getBoundingClientRect();
          return {
            pageWidth: document.documentElement.scrollWidth, viewport: innerWidth,
            clientWidth: grid.clientWidth, scrollWidth: grid.scrollWidth, scrollLeft: grid.scrollLeft,
            lastHourVisible: lastHour.left >= r.left - 1 && lastHour.right <= r.right + 1,
          };
        });
        try {
          assert.ok(geometry.pageWidth <= width, 'page widens to ' + geometry.pageWidth + 'px');
          assert.ok(geometry.lastHourVisible, '23:00 must remain reachable');
          if (geometry.scrollWidth > geometry.clientWidth + 1) assert.ok(geometry.scrollLeft > 0, 'overflow must scroll within the heatmap');
          console.log('PASS ' + theme + ' ' + width + 'px: heatmap fits and all hours remain reachable');
        } catch (err) {
          failures++;
          console.error('FAIL ' + theme + ' ' + width + 'px: ' + err.message + ' ' + JSON.stringify(geometry));
        }
      }
    }
  } finally {
    await browser.close();
  }
  process.exitCode = failures ? 1 : 0;
})().catch(err => { console.error(err); process.exitCode = 1; });
