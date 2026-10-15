'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.resolve(__dirname, '../..');
const html = fs.readFileSync(path.join(root, 'public/index.html'), 'utf8');

assert.match(html, /<title>Canadaverse CoreScope<\/title>/);
assert(html.includes('canadaverse.css?v=__BUST__'), 'brand stylesheet uses automatic cache buster');
assert.match(html, /class="brand-text">Canadaverse CoreScope<\/span>/);
for (const className of ['brand-logo', 'brand-mark-only']) {
  const mark = html.match(new RegExp('<svg class="' + className + '"[^>]*>[\\s\\S]*?<\\/svg>'));
  assert(mark, 'shell includes ' + className);
  assert.match(mark[0], /<image href="brand\/canadaverse-emblem\.svg"/);
  assert.match(mark[0], /class="logo-node-a"/, 'packet pulse remains available');
}
assert(fs.existsSync(path.join(root, 'public/brand/canadaverse-emblem.svg')));
console.log('  ✓ shell identity and shared emblem');

// Like the existing frontend unit suites, run the production scripts in a VM.
// DOM replacement, reset and click behavior run in the branding browser suite.
let homePage;
const ctx = vm.createContext({
  window: { addEventListener() {}, removeEventListener() {} },
  document: { addEventListener() {}, getElementById: () => null },
  localStorage: { getItem: key => key === 'meshcore-user-level' ? 'experienced' : null },
  registerPage: (name, page) => { if (name === 'home') homePage = page; },
  escapeHtml: value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
  escapeAttr: value => String(value).replace(/&/g, '&amp;').replace(/"/g, '&quot;'),
  api: async () => { throw new Error('No telemetry in this fixture'); },
  CLIENT_TTL: {}, clearTimeout, console
});
function load(name) {
  vm.runInContext(fs.readFileSync(path.join(root, 'public', name), 'utf8'), ctx, { filename: name });
}
load('payload-labels.js');
load('customize-v2.js');
const api = ctx.window._customizerV2;
const defaults = { branding: { siteName: 'Canadaverse CoreScope', logoUrl: '' }, theme: { accent: '#076a8c' }, themeDark: { accent: '#18b7ff' } };
const operator = { branding: { siteName: 'Operator mesh', logoUrl: '/operator.svg', homeUrl: 'https://example.org/' }, theme: { accent: '#112233' } };
const effective = api.computeEffective(defaults, operator);
assert.equal(effective.branding.siteName, 'Operator mesh');
assert.equal(effective.branding.logoUrl, '/operator.svg');
assert.equal(effective.branding.homeUrl, 'https://example.org/');
assert.equal(effective.theme.accent, '#112233');
assert.equal(effective.themeDark.accent, '#18b7ff');
const reset = api.computeEffective(defaults, {});
assert.equal(reset.branding.siteName, 'Canadaverse CoreScope');
assert.equal(reset.branding.logoUrl, '');
assert(!JSON.stringify(api.computeEffective({}, {}).home).includes('910.525'), 'fallback guide must not prescribe a regional frequency');
console.log('  ✓ operator branding and palette retain precedence');

ctx.window.SITE_CONFIG = { ...reset, home: { heroTitle: 'Operator dashboard' } };
load('home.js');
const container = { innerHTML: '', querySelectorAll: () => [] };
homePage.init(container);
assert.match(container.innerHTML, /<h1>Operator dashboard<\/h1>/);
assert.deepEqual(Array.from(container.innerHTML.matchAll(/class="home-action" href="([^"]+)"/g), match => match[1]), ['#/packets', '#/map', '#/nodes']);
assert.match(container.innerHTML, /class="home-attribution">[\s\S]*?<a href="https:\/\/github.com\/Kpa-clawbot\/CoreScope"/);
homePage.destroy();
console.log('  ✓ home preserves operator title, existing routes and upstream attribution');
