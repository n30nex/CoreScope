/* Populated MQTT sources must follow the viewport even when a status refresh
 * renders between resize events. Synthetic addresses only; no server needed. */
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const PUBLIC = path.resolve(__dirname, '../../public');
const css = ['style.css', 'canadaverse.css'].map(file => fs.readFileSync(path.join(PUBLIC, file), 'utf8')).join('\n');

(async () => {
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH || undefined });
  try {
    const page = await browser.newPage({ viewport: { width: 375, height: 812 } });
    await page.clock.install({ time: new Date('2026-01-01T12:00:00Z') });
    await page.clock.pauseAt(new Date('2026-01-01T12:00:01Z'));
    await page.setContent('<style>' + css + '</style><main id="app"><div class="observers-page"><div id="mqttStatusPanel"></div></div></main>');
    await page.addScriptTag({ path: path.join(PUBLIC, 'mqtt-status-panel.js') });
    await page.evaluate(() => {
      window.testSources = [1, 2].map(i => ({
        name: 'example-source-' + i, broker: 'wss://broker-' + i + '.example.invalid/mqtt',
        connected: i === 1, lastPacketUnix: Date.now() / 1000 - 20,
        packetsLast5m: 999, packetsTotal: 123456789, disconnectCount: 25,
      }));
      MqttStatusPanel.renderPanel(document.getElementById('mqttStatusPanel'), { sources: testSources });
    });
    assert.equal(await page.locator('.mqtt-status-card').count(), 2, 'fresh mobile load uses cards');

    for (const width of [375, 320]) {
      await page.setViewportSize({ width: 1280, height: 812 });
      await page.evaluate(() => {
        window.dispatchEvent(new Event('resize'));
        // A status refresh arrives before the debounced resize callback.
        MqttStatusPanel.renderPanel(document.getElementById('mqttStatusPanel'), { sources: testSources });
      });
      assert.equal(await page.locator('.mqtt-status-table').count(), 1, 'desktop refresh uses the table');
      await page.setViewportSize({ width, height: 812 });
      await page.evaluate(() => window.dispatchEvent(new Event('resize')));
      await page.clock.runFor(200);
      assert.equal(await page.locator('.mqtt-status-card').count(), 2, 'resize restores mobile cards');
      assert.equal(await page.locator('.mqtt-status-table').count(), 0, 'no stale desktop table');
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'sources stay within viewport');
      console.log('PASS ' + width + 'px: populated sources fit after refresh and resize');
    }
  } finally {
    await browser.close();
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
