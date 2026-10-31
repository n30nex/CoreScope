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
let activeTheme = 'light';
const cssProperties = {};
const ctx = vm.createContext({
  window: { addEventListener() {}, removeEventListener() {}, dispatchEvent() {} },
  document: {
    addEventListener() {}, getElementById: () => null, querySelector: () => null,
    documentElement: {
      getAttribute: () => activeTheme,
      style: { setProperty: (key, value) => { cssProperties[key] = value; } }
    }
  },
  localStorage: { getItem: key => key === 'meshcore-user-level' ? 'experienced' : null },
  registerPage: (name, page) => { if (name === 'home') homePage = page; },
  escapeHtml: value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
  escapeAttr: value => String(value).replace(/&/g, '&amp;').replace(/"/g, '&quot;'),
  api: async () => { throw new Error('No telemetry in this fixture'); },
  CLIENT_TTL: {}, clearTimeout, console, CustomEvent: class { constructor(type) { this.type = type; } }
});
function load(name) {
  vm.runInContext(fs.readFileSync(path.join(root, 'public', name), 'utf8'), ctx, { filename: name });
}
load('payload-labels.js');
load('customize-v2.js');
const api = ctx.window._customizerV2;
// Use the API's actual default fields so palette drift is caught before a
// successful response replaces the stylesheet's initial fallback colors.
const routes = fs.readFileSync(path.join(root, 'cmd/server/routes.go'), 'utf8');
function serverPalette(name) {
  const block = routes.match(new RegExp(name + ' := mergeMap\\(map\\[string\\]interface\\{\\}\\{([\\s\\S]*?)\\n\\t\\},'));
  assert(block, 'server palette exists: ' + name);
  return Object.fromEntries(Array.from(block[1].matchAll(/"(\w+)":\s*"([^"\n]+)"/g), match => [match[1], match[2]]));
}
const defaults = {
  branding: { siteName: 'Canadaverse CoreScope', logoUrl: '' },
  theme: serverPalette('themeColors'), themeDark: serverPalette('themeDark')
};
const css = fs.readFileSync(path.join(root, 'public/canadaverse.css'), 'utf8');
const cssBlocks = Array.from(css.matchAll(/:root(?:\[data-theme='dark'\])?\s*\{([^}]+)\}/g), match =>
  Object.fromEntries(Array.from(match[1].matchAll(/(--[\w-]+):\s*([^;]+);/g), value => [value[1], value[2].trim()]))
);
assert.equal(cssBlocks.length, 2, 'theme has light and dark fallback tokens');
assert.equal(defaults.theme.background, '#f4f4f5');
assert.equal(defaults.themeDark.background, '#09090b');
assert.equal(defaults.themeDark.accent, '#3b82f6');
assert.match(defaults.theme.font, /^Inter, system-ui/);
assert.equal(defaults.themeDark.font, undefined, 'dark mode inherits the shared font and existing user choices');
function luminance(hex) {
  const rgb = hex.slice(1).match(/../g).map(value => parseInt(value, 16) / 255)
    .map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
  return rgb[0] * 0.2126 + rgb[1] * 0.7152 + rgb[2] * 0.0722;
}
function contrast(foreground, background) {
  const a = luminance(foreground), b = luminance(background);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}
ctx.window.SITE_CONFIG = defaults;
assert.equal(api.detectActivePreset(), 'default', 'API palette must match the default customizer preset');
for (const mode of ['light', 'dark']) {
  activeTheme = mode;
  assert.equal(api.detectActivePreset(), 'default', mode + ' detects the default preset');
  api.applyCSS(defaults, {});
  const palette = mode === 'dark' ? { ...defaults.theme, ...defaults.themeDark } : defaults.theme;
  const fallback = Object.assign({}, cssBlocks[0], mode === 'dark' ? cssBlocks[1] : {});
  const resolve = value => value.replace(/var\((--[\w-]+)\)/g, (_, key) => resolve(fallback[key]));
  for (const key of Object.keys(palette)) {
    assert.equal(cssProperties[api.THEME_CSS_MAP[key]], palette[key], mode + ' applies ' + key);
    assert.equal(resolve(fallback[api.THEME_CSS_MAP[key]]), palette[key], mode + ' CSS fallback matches API ' + key);
  }
  for (const foreground of ['text', 'textMuted', 'accent', 'statusGreen', 'statusYellow', 'statusRed']) {
    for (const background of ['background', 'surface1', 'surface2', 'surface3', 'rowHover', 'selectedBg']) {
      assert(contrast(palette[foreground], palette[background]) >= 4.5, mode + ' ' + foreground + ' on ' + background + ' meets AA');
    }
  }
  assert(contrast(resolve(fallback['--text-on-accent']), resolve(fallback['--accent-strong'])) >= 4.5, 'primary buttons meet AA');
}
console.log('  ✓ Beacon palette matches the API, customizer preset and applied theme');
const operator = { branding: { siteName: 'Operator mesh', logoUrl: '/operator.svg', homeUrl: 'https://example.org/' }, theme: { accent: '#112233' } };
const effective = api.computeEffective(defaults, operator);
assert.equal(effective.branding.siteName, 'Operator mesh');
assert.equal(effective.branding.logoUrl, '/operator.svg');
assert.equal(effective.branding.homeUrl, 'https://example.org/');
assert.equal(effective.theme.accent, '#112233');
assert.equal(effective.themeDark.accent, '#3b82f6');
activeTheme = 'light';
api.applyCSS(effective, operator);
assert.equal(cssProperties['--accent'], '#112233', 'visitor override reaches the live CSS variable');
const darkOverride = { themeDark: { accent: '#654321', font: 'Georgia, serif' } };
activeTheme = 'dark';
api.applyCSS(api.computeEffective(defaults, { theme: { font: 'Georgia, serif' } }), {});
assert.equal(cssProperties['--font'], 'Georgia, serif', 'dark mode retains a shared visitor font override');
api.applyCSS(api.computeEffective(defaults, darkOverride), darkOverride);
assert.equal(cssProperties['--accent'], '#654321');
assert.equal(cssProperties['--font'], 'Georgia, serif');
api.applyCSS(defaults, {});
assert.equal(cssProperties['--accent'], '#3b82f6', 'reset restores the default dark accent');
assert.equal(cssProperties['--font'], defaults.theme.font, 'reset restores the default body font');
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
