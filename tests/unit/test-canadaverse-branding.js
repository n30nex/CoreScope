'use strict';
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const { JSDOM } = require('jsdom');
const root = path.resolve(__dirname, '../..');
const html = fs.readFileSync(path.join(root, 'public/index.html'), 'utf8');
const dom = new JSDOM(html, { url: 'http://localhost/', runScripts: 'outside-only' });
const win = dom.window;
win.matchMedia = () => ({ matches: false, addEventListener() {} });
win.eval(fs.readFileSync(path.join(root, 'public/payload-labels.js'), 'utf8'));
win.eval(fs.readFileSync(path.join(root, 'public/customize-v2.js'), 'utf8'));
const api = win._customizerV2;

(async () => { try {
  assert.equal(win.document.title, 'Canadaverse CoreScope');
  assert(html.includes('canadaverse.css?v=__BUST__'), 'brand stylesheet uses automatic cache buster');
  assert(win.document.querySelector('.brand-text').textContent.includes('Canadaverse'));
  for (const selector of ['.brand-logo', '.brand-mark-only']) {
    assert.equal(win.document.querySelector(selector + ' image').getAttribute('href'), 'brand/canadaverse-emblem.svg');
  }
  assert(fs.existsSync(path.join(root, 'public/brand/canadaverse-emblem.svg')));
  console.log('  ✓ shell identity and shared emblem');

  const defaults = { branding: { siteName: 'Canadaverse CoreScope', logoUrl: '' }, theme: { accent: '#076a8c' }, themeDark: { accent: '#18b7ff' } };
  api.init(defaults);
  const operator = { branding: { siteName: 'Operator mesh', logoUrl: '/operator.svg', homeUrl: 'https://example.org/' }, theme: { accent: '#112233' } };
  api.applyCSS(api.computeEffective(defaults, operator), operator);
  assert.equal(win.document.title, 'Operator mesh');
  assert.equal(win.document.querySelector('.brand-text').textContent, 'Operator mesh');
  assert.equal(win.document.querySelector('.nav-brand').getAttribute('aria-label'), 'Operator mesh home');
  assert.equal(win.document.querySelector('.nav-brand').getAttribute('href'), 'https://example.org/');
  for (const selector of ['.brand-logo', '.brand-mark-only']) {
    assert.equal(win.document.querySelector(selector).getAttribute('src'), '/operator.svg', 'custom image reaches desktop and mobile');
  }
  assert.equal(win.document.documentElement.style.getPropertyValue('--accent'), '#112233');
  console.log('  ✓ operator branding and palette retain precedence');

  api.applyCSS(defaults, {});
  assert.equal(win.document.querySelector('.brand-text').textContent, 'Canadaverse CoreScope');
  assert.equal(win.document.querySelector('.nav-brand').getAttribute('href'), '#/');
  for (const selector of ['.brand-logo', '.brand-mark-only']) {
    assert.equal(win.document.querySelector(selector + ' image').getAttribute('href'), 'brand/canadaverse-emblem.svg', 'clearing logo restores the default without reload');
    assert(win.document.querySelector(selector + ' .logo-node-a'), 'packet pulse remains available after reset');
  }
  console.log('  ✓ reset restores both brand marks and packet indicators');
  assert(!JSON.stringify(api.computeEffective({}, {}).home).includes('910.525'), 'fallback guide must not prescribe a regional frequency');

  let homePage;
  win.registerPage = (name, page) => { if (name === 'home') homePage = page; };
  win.escapeHtml = value => { const el = win.document.createElement('div'); el.textContent = value; return el.innerHTML; };
  win.escapeAttr = value => win.escapeHtml(value).replace(/"/g, '&quot;');
  win.truncate = value => String(value).slice(0, 12);
  win.CLIENT_TTL = { nodeHealth: 0 };
  win.api = async () => { throw new Error('No telemetry in this fixture'); };
  win.localStorage.setItem('meshcore-user-level', 'experienced');
  win.localStorage.setItem('meshcore-my-nodes', JSON.stringify([{ pubkey: 'fixture', name: 'Fixture' }]));
  win.SITE_CONFIG.home = { heroTitle: 'Operator dashboard' };
  win.eval(fs.readFileSync(path.join(root, 'public/home.js'), 'utf8'));
  homePage.init(win.document.getElementById('app'));
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(win.document.querySelector('.home-hero h1').textContent, 'My Mesh');
  win.document.querySelector('.mnc-remove').click();
  assert.equal(win.document.querySelector('.home-hero h1').textContent, 'Operator dashboard');
  assert.deepEqual(Array.from(win.document.querySelectorAll('.home-action'), el => el.getAttribute('href')), ['#/packets', '#/map', '#/nodes']);
  assert(win.document.querySelector('.home-attribution a[href="https://github.com/Kpa-clawbot/CoreScope"]'));
  homePage.destroy();
  console.log('  ✓ home preserves operator title after last node removal and existing routes');
} finally {
  dom.window.close();
} })().catch(error => { console.error(error); process.exitCode = 1; });
