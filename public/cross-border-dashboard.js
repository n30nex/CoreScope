/* Bounded, source-labelled dashboard views. The server owns all measurements. */
'use strict';
(function () {
  var WIDTHS = [1, 2, 3], MAX_ROWS = 500, PAGE_SIZE = 25;
  var TABS = [['overview', 'Overview'], ['hash-widths', 'Hash widths'], ['scopes-regions', 'Scopes & regions'], ['radio-load', 'Radio load'], ['repeaters', 'Repeaters']];
  function normalizeTab(value) { return TABS.some(function (tab) { return tab[0] === value; }) ? value : 'overview'; }
  function rows(value) { return Array.isArray(value) ? value : []; }
  function numeric(value) { return value != null && value !== '' && typeof value !== 'boolean' && Number.isFinite(Number(value)) ? Number(value) : null; }
  function count(value) { var n = numeric(value); return n != null && n >= 0 ? n : null; }
  function rate(value) { var n = count(value); return n != null && n <= 100 ? n : null; }
  function available(source) { return source && (source.status === 'available' || source.status === 'empty'); }
  function widthSummary(source, field) {
    var values = WIDTHS.map(function (bytes) {
      var row = rows(source && source.widths).slice(0, 3).find(function (r) { return Number(r.bytes) === bytes; });
      return available(source) && Array.isArray(source.widths) ? (row ? count(row[field]) : 0) : null;
    });
    var denominator = values.every(function (n) { return n != null; }) ? values.reduce(function (a, b) { return a + b; }, 0) : null;
    return WIDTHS.map(function (bytes, i) { return { bytes: bytes, count: values[i], total: denominator, percent: denominator > 0 ? values[i] / denominator * 100 : null }; });
  }
  function timelineData(source) {
    var points = rows(source && source.timeline).slice(0, 360);
    var counts = points.map(function (p) {
      var values = [p.oneByte, p.twoByte, p.threeByte].map(count);
      return p.complete && values.every(function (n) { return n != null; }) ? values.reduce(function (a, b) { return a + b; }, 0) : null;
    });
    return { labels: points.map(function (p) { return p.at; }), counts: counts, series: WIDTHS.map(function (bytes, index) {
      return { bytes: bytes, values: points.map(function (p, i) {
        return counts[i] > 0 ? count(p[['oneByte', 'twoByte', 'threeByte'][index]]) / counts[i] * 100 : null;
      }) };
    }) };
  }
  function recordSeries(source) {
    var points = rows(source && source.timeline).slice(0, 360);
    return { labels: points.map(function (point) { return point.at; }), values: points.map(function (point) { return available(source) && point.complete ? count(point.records) : null; }) };
  }
  function latestRadioReport(radio) {
    if (!radio || radio.status !== 'available') return null;
    var latest = null, latestMs = -Infinity;
    rows(radio.observers).slice(0, MAX_ROWS).forEach(function (observer) {
      var stamp = typeof observer.latestAt === 'string' ? Date.parse(observer.latestAt) : NaN;
      if (Number.isFinite(stamp) && stamp > latestMs) { latest = observer.latestAt; latestMs = stamp; }
    });
    return latest;
  }
  function scopeComparison(local, peer) {
    var all = new Map();
    [local, peer].forEach(function (source) { rows(source && source.scopes).slice(0, 256).forEach(function (r) {
      if (typeof r.key !== 'string' || count(r.records) == null) return;
      var item = all.get(r.key) || { key: r.key, label: String(r.label || r.key), total: 0 };
      item.total += Number(r.records); all.set(r.key, item);
    }); });
    var fixed = ['unscoped', 'transport-zero', 'unknown'];
    function order(key) { var index = fixed.indexOf(key); return index < 0 ? 3 : index; }
    var ordered = Array.from(all.values()).sort(function (a, b) { return order(a.key) - order(b.key) || a.key.localeCompare(b.key); });
    var selected = ordered.slice(0, 6), other = ordered.slice(6);
    if (other.length) selected.push({ key: '__other__', label: 'Other scopes' });
    function percentages(source) {
      if (!available(source)) return selected.map(function () { return null; });
      var byKey = new Map(rows(source.scopes).slice(0, 256).map(function (r) { return [r.key, count(r.records) || 0]; }));
      var total = Array.from(byKey.values()).reduce(function (a, b) { return a + b; }, 0);
      return selected.map(function (r) {
        var value = r.key === '__other__' ? other.reduce(function (n, item) { return n + (byKey.get(item.key) || 0); }, 0) : byKey.get(r.key) || 0;
        return total > 0 ? value / total * 100 : null;
      });
    }
    return { keys: selected.map(function (r) { return r.key; }), labels: selected.map(function (r) { return r.label; }), local: percentages(local), peer: percentages(peer) };
  }
  function carriedScopes(row) {
    var observed = rows(row.observedScopes).slice(0, 64);
    var names = observed.filter(function (s) { return s.kind === 'named' && s.name; }).map(function (s) { return s.name; });
    if (row.observedUnscoped) names.push('Unscoped flood');
    if (row.observedTransportWildcard) names.push('Transport wildcard');
    if (row.observedUnknownScope || observed.some(function (s) { return s.kind === 'unknown'; })) names.push('Unresolved scope');
    return Array.from(new Set(names));
  }
  function radioRanking(local, peer) {
    var result = [];
    [local, peer].forEach(function (source, side) { rows(source && source.radio && source.radio.status !== 'unavailable' && source.radio.observers).slice(0, MAX_ROWS).forEach(function (o) {
      var tx = rate(o.txAirtimePct), rx = rate(o.rxAirtimePct);
      if (typeof o.id !== 'string' || (tx == null && rx == null)) return;
      result.push(Object.assign({}, o, { source: side ? 'peer' : 'local', txAirtimePct: tx, rxAirtimePct: rx, score: Math.max(tx || 0, rx || 0) }));
    }); });
    return result.sort(function (a, b) { return b.score - a.score || a.id.localeCompare(b.id); }).slice(0, 10);
  }
  function carryLabel(row, bytes) {
    var observed = rows(row.hopWidths).map(Number).indexOf(bytes) >= 0;
    if (observed && bytes !== 1) return 'Observed';
    if (rows(row.ambiguousWidths).map(Number).indexOf(bytes) >= 0 || (bytes === 1 && observed)) return 'Ambiguous';
    return count(row.sampleForwardedPackets) > 0 ? 'Not observed' : 'No evidence';
  }
  function regionGrid(data) {
    function regions(source) { return Array.from(new Set(rows(source && source.regions).slice(0, 128).map(function (r) { return String(r.iata || ''); }).filter(function (v) { return /^[A-Z0-9]{3}$/.test(v); }))).slice(0, 16); }
    var local = regions(data.local), peer = regions(data.peer), pairs = new Map();
    rows(data.regionPairs).slice(0, 256).forEach(function (p) { pairs.set(p.localIata + ':' + p.peerIata, count(p.contents)); });
    var cells = [];
    var usable = data.identityComplete === true && available(data.local) && available(data.peer);
    var incomplete = data.regionPairsTruncated || data.local && data.local.truncated || data.peer && data.peer.truncated;
    local.forEach(function (l) { peer.forEach(function (p) { var key = l + ':' + p; cells.push({ local: l, peer: p,
      value: !usable ? null : pairs.has(key) ? pairs.get(key) : incomplete ? null : 0 }); }); });
    return { local: local, peer: peer, cells: cells };
  }
  var api = { normalizeTab: normalizeTab, recordSeries: recordSeries, widthSummary: widthSummary, timelineData: timelineData, latestRadioReport: latestRadioReport, scopeComparison: scopeComparison, radioRanking: radioRanking, carryLabel: carryLabel, carriedScopes: carriedScopes, regionGrid: regionGrid };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (typeof window === 'undefined') return;

  function text(node, value) { if (node.textContent !== String(value)) node.textContent = String(value); }
  function amount(value) { var n = count(value); return n == null ? 'Unknown' : n.toLocaleString(); }
  function pct(value) { var n = rate(value); return n == null ? 'Unknown' : n.toLocaleString(undefined, { maximumFractionDigits: 1 }) + '%'; }
  function time(value) { var d = new Date(value); return value && Number.isFinite(d.getTime()) ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : 'Unknown'; }
  function reportTime(value) { var d = new Date(value); return value && Number.isFinite(d.getTime()) ? d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Unknown'; }
  function regionName(iata) { return window.IATA_CITIES && window.IATA_CITIES[iata] ? window.IATA_CITIES[iata] + ' (' + iata + ')' : iata; }
  function chart(id, label, variant) { return '<div class="cb-chart' + (variant ? ' ' + variant : '') + '"><canvas id="' + id + '" role="img" aria-label="' + label + '"></canvas></div>'; }
  api.html = function () {
    return '<section id="cb-dashboard" hidden aria-label="Cross-border overview">' +
      '<div id="cb-analytics-tabs" class="analytics-tabs cb-analytics-tabs" aria-label="Cross-border analytics">' + TABS.map(function (tab, i) {
        return '<button type="button" class="tab-btn' + (!i ? ' active' : '') + '" id="cb-tab-' + tab[0] + '" data-tab="' + tab[0] + '" aria-controls="cb-tab-panel">' + tab[1] + '</button>';
      }).join('') + '</div><div id="cb-tab-panel" role="tabpanel" tabindex="0" aria-labelledby="cb-tab-overview">' +
      '<div class="cb-overview-metrics stats-grid"><div class="stat-card"><span class="stat-label">Saved records · Canadian listeners</span><strong id="cb-records-local" class="stat-value">—</strong>' + chart('cb-local-spark', 'Saved record history heard by Canadian listeners', 'cb-spark') + '<small><span id="cb-dash-local">—</span> distinct packet contents</small><small id="cb-dash-local-note"></small></div>' +
      '<div class="stat-card"><span id="cb-peer-kpi-label" class="stat-label">Saved records · peer listeners</span><strong id="cb-records-peer" class="stat-value">—</strong>' + chart('cb-peer-spark', 'Saved record history heard by peer listeners', 'cb-spark') + '<small><span id="cb-dash-peer">—</span> distinct packet contents</small><small id="cb-dash-peer-note"></small></div>' +
      '<div class="stat-card"><span class="stat-label">Heard in both feeds</span><strong id="cb-dash-shared" class="stat-value">—</strong><small>Shared packet contents with distinct observer identities</small></div>' +
      '<div class="stat-card"><span class="stat-label">Radios reporting airtime</span><strong id="cb-radio-reporters" class="stat-value">—</strong><small id="cb-radio-reporters-note"></small><small>Available reports, not all repeaters</small></div></div>' +
      '<p id="cb-dashboard-coverage" class="cb-note"></p><p id="cb-chart-fallback" class="cb-warning" hidden>Charts could not load. All values remain available below each chart.</p>' +
      '<section class="analytics-card cb-visual cb-dashboard-section" data-cb-views="overview" aria-labelledby="cb-activity-title"><h2 id="cb-activity-title">Saved path evidence over time</h2><p class="cb-section-intro">Compare reporting activity on one time scale. Gaps remain unknown.</p>' + chart('cb-activity-chart', 'Saved observer/path records over time by reporting feed', 'cb-chart-large') + '<p class="cb-note">These are first-stored observer/path records, not every radio reception. Listener coverage differs between feeds.</p></section>' +
      '<section class="cb-dashboard-section" data-cb-views="overview" aria-labelledby="cb-own-adverts-title"><h2 id="cb-own-adverts-title">Own-advert width composition</h2><p class="cb-section-intro">Grouped by the listeners that heard each repeater; the same repeater may appear in both groups.</p><div class="analytics-grid cb-chart-pair">' +
      '<article class="analytics-card cb-visual"><h3 id="cb-local-donut-title">Heard by Canadian listeners</h3><div class="cb-donut-wrap">' + chart('cb-local-donut', 'Own-advert width composition heard by Canadian listeners', 'cb-chart-donut') + '<div class="cb-donut-center"><strong id="cb-local-donut-total">—</strong><span>known widths</span></div></div><div id="cb-local-donut-values" class="cb-chart-values"></div></article>' +
      '<article class="analytics-card cb-visual"><h3 id="cb-peer-donut-title">Heard by peer listeners</h3><div class="cb-donut-wrap">' + chart('cb-peer-donut', 'Own-advert width composition heard by peer listeners', 'cb-chart-donut') + '<div class="cb-donut-center"><strong id="cb-peer-donut-total">—</strong><span>known widths</span></div></div><div id="cb-peer-donut-values" class="cb-chart-values"></div></article></div></section>' +
      '<section class="cb-dashboard-section" data-cb-views="hash-widths" aria-labelledby="cb-adoption-title"><h2 id="cb-adoption-title">1-, 2- and 3-byte adoption</h2>' +
      '<p class="cb-section-intro">A repeater’s own adverts show the width it originates. It can still carry packets using other widths.</p>' +
      '<div class="cb-chart-pair analytics-grid"><article class="cb-visual analytics-card"><h3>Repeaters’ latest own adverts</h3>' + chart('cb-advert-chart', 'Repeater own-advert width shares in Canada and the peer feed') + '<div id="cb-advert-values" class="cb-chart-values"></div></article>' +
      '<article class="cb-visual analytics-card"><h3>Path widths carried</h3>' + chart('cb-width-chart', 'Saved flood path width shares in Canada and the peer feed') + '<div id="cb-width-values" class="cb-chart-values"></div></article></div>' +
      '<details class="cb-chart-help"><summary>How to read adoption</summary><p>Own-advert counts use each repeater’s latest qualifying advert. Carried shares count saved flood-path records, not devices. Wider paths reduce prefix ambiguity but are not automatically lower airtime.</p></details></section>' +
      '<section class="cb-dashboard-section" data-cb-views="hash-widths" aria-labelledby="cb-trends-title"><h2 id="cb-trends-title">Observed path widths over time</h2>' +
      '<div class="cb-chart-pair analytics-grid"><article class="cb-visual analytics-card"><h3 id="cb-local-trend-title">Canada</h3>' + chart('cb-local-trend', 'Canadian observed path width shares over time') + '</article>' +
      '<article class="cb-visual analytics-card"><h3 id="cb-peer-trend-title">US / peer</h3>' + chart('cb-peer-trend', 'Peer observed path width shares over time') + '</article></div>' +
      '<p class="cb-note">Gaps mean incomplete history. These are saved packet records, not every radio reception.</p>' +
      '<details class="cb-chart-help"><summary>Time-series values and method</summary><p>Time buckets use first-stored observer/path records. Qualifying flood records are the denominator of each width share. Stored headers can be refreshed after the original timestamp, so these charts reconstruct retained evidence rather than an immutable over-air event history.</p><div class="cb-table-scroll"><table class="cb-trend-table"><caption>Observed path-width shares by time</caption><thead><tr><th>Feed</th><th>Time</th><th>Qualifying flood records</th><th>1-byte</th><th>2-byte</th><th>3-byte</th></tr></thead><tbody id="cb-trend-values"></tbody></table></div></details></section>' +
      '<div class="cb-chart-pair analytics-grid" data-cb-views="overview scopes-regions"><section class="cb-visual analytics-card cb-dashboard-section"><h2>Flood scoping</h2>' + chart('cb-scope-chart', 'Scoped and unscoped flood evidence by feed') + '<div id="cb-scope-values" class="cb-chart-values"></div>' +
      '<p class="cb-note">Transport wildcard and ordinary unscoped flooding are shown separately.</p></section>' +
      '<section class="cb-visual analytics-card cb-dashboard-section"><h2>Shared content by region</h2><p class="cb-note">Select a cell to compare those two regions.</p><div id="cb-region-grid" class="cb-region-grid"></div><p id="cb-region-note" class="cb-note"></p></section></div>' +
      '<section class="cb-dashboard-section" data-cb-views="radio-load" aria-labelledby="cb-radio-title"><h2 id="cb-radio-title">Where are reporting radios busiest?</h2>' +
      '<div class="cb-chart-pair analytics-grid"><article class="cb-visual analytics-card"><h3>Reported airtime</h3>' + chart('cb-radio-chart', 'Reported transmitting and estimated received-packet airtime by feed') + '<div id="cb-radio-values" class="cb-chart-values"></div></article>' +
      '<article class="cb-visual analytics-card"><h3>Reporting radios to inspect</h3><div id="cb-radio-ranking" class="cb-radio-ranking"></div></article></div>' +
      '<p class="cb-note">TX is time spent transmitting; RX estimates successfully received packets. These reports help locate load, not prove which country caused congestion. Radio counters include all scopes.</p></section>' +
      '<section class="cb-dashboard-section" data-cb-views="repeaters" aria-labelledby="cb-carry-title"><div class="cb-header"><div><h2 id="cb-carry-title">What do repeaters carry?</h2><p class="cb-note">Observed carrying, missing evidence and ambiguous identities stay separate.</p></div>' +
      '<label class="cb-search-label">Find a repeater<input id="cb-repeater-search" type="search" maxlength="80" placeholder="Name or public key"></label></div>' +
      '<p id="cb-carry-count" class="cb-note"></p><p class="cb-scroll-hint cb-note">Scroll sideways to compare carried widths and scopes.</p><div class="cb-table-scroll" tabindex="0" role="region" aria-label="Repeater carrying matrix; scroll horizontally for scope columns"><table class="cb-carry-table"><caption>Own adverts and flood evidence by reporting feed</caption><thead><tr><th>Repeater / heard by</th><th>Own advert</th><th>1-byte carried</th><th>2-byte carried</th><th>3-byte carried</th><th>Scopes carried</th><th>Reported allowed scopes</th></tr></thead><tbody id="cb-carry-rows"></tbody></table></div>' +
      '<div class="cb-page-controls"><button id="cb-carry-prev" type="button">Previous</button><span id="cb-carry-page"></span><button id="cb-carry-next" type="button">Next</button></div>' +
      '<p class="cb-note">“Not observed” is not a confirmed block. One-byte hops cannot be attributed confidently; a full-origin advert can still identify a one-byte advertiser.</p>' +
      '<section id="cb-repeater-detail" class="cb-detail" hidden><div class="cb-header"><h3 id="cb-repeater-title" tabindex="-1"></h3><button id="cb-repeater-close" type="button">Close repeater</button></div><p id="cb-repeater-facts"></p></section></section></div></section>';
  };

  api.mount = function (root, callbacks) {
    var charts = new Map(), current = {}, ui = {}, repeaterRows = [], page = 0, resizeFrame = null;
    function el(id) { return root.querySelector('#' + id); }
    function colors() {
      var css = getComputedStyle(root);
      return ['--accent', '--success', '--palette-purple-500', '--text-muted', '--danger', '--link-color', '--border'].map(function (key) { return css.getPropertyValue(key).trim(); });
    }
    function putChart(id, type, labels, datasets, options) {
      var canvas = el(id);
      if (typeof window.Chart !== 'function') { el('cb-chart-fallback').hidden = false; canvas.parentElement.hidden = true; return; }
      el('cb-chart-fallback').hidden = true;
      canvas.parentElement.hidden = false;
      var existing = charts.get(id);
      if (existing) { existing.data.labels = labels; existing.data.datasets = datasets; existing.options = options; existing.update('none'); }
      else charts.set(id, new window.Chart(canvas, { type: type, data: { labels: labels, datasets: datasets }, options: options }));
    }
    function chartUpdate(id, type, labels, datasets, horizontal, percent, denominators) {
      var css = getComputedStyle(root), ink = css.getPropertyValue('--text-muted').trim(), grid = css.getPropertyValue('--border').trim();
      var opts = { responsive: true, maintainAspectRatio: false, animation: false, indexAxis: horizontal ? 'y' : 'x',
        interaction: { mode: 'index', intersect: false },
        plugins: { legend: { position: 'bottom', labels: { color: ink, boxWidth: 11, padding: 14, font: { size: 12 } } },
          tooltip: { callbacks: {
            label: function (ctx) { return ctx.dataset.label + ': ' + (percent ? pct(ctx.raw) : amount(ctx.raw)); },
            footer: function (items) { return denominators && items.length ? 'Qualifying flood records: ' + amount(denominators[items[0].dataIndex]) : ''; }
          } } },
        scales: { x: { stacked: type === 'bar', grid: { color: grid }, ticks: { color: ink, maxTicksLimit: 7, maxRotation: 0 }, beginAtZero: true },
          y: { stacked: type === 'bar', grid: { color: grid }, ticks: { color: ink }, beginAtZero: true } } };
      if (percent) { var axis = horizontal ? opts.scales.x : opts.scales.y; axis.max = 100; axis.ticks.callback = function (v) { return v + '%'; }; }
      putChart(id, type, labels, datasets, opts);
    }
    function paragraphs(id, values) {
      var node = el(id);
      while (node.children.length > values.length) node.lastChild.remove();
      values.forEach(function (value, i) { if (!node.children[i]) node.appendChild(document.createElement('p')); text(node.children[i], value); });
    }
    function label(side) { return side === 'local' ? 'Canadian listeners' : (current.peer && current.peer.label || 'Peer listeners'); }
    function overview() {
      var palette = colors(), css = getComputedStyle(root), ink = css.getPropertyValue('--text-muted').trim(), surface = css.getPropertyValue('--card-bg').trim();
      var series = ['local', 'peer'].map(function (side) { return recordSeries(current[side]); });
      var times = Array.from(new Set(series[0].labels.concat(series[1].labels))).sort().slice(0, 720);
      var datasets = series.map(function (data, i) {
        var byTime = new Map(data.labels.map(function (at, index) { return [at, data.values[index]]; }));
        return { label: label(i ? 'peer' : 'local'), data: times.map(function (at) { return byTime.has(at) ? byTime.get(at) : null; }),
          borderColor: palette[i ? 2 : 0], backgroundColor: i ? palette[2] : css.getPropertyValue('--accent-bg').trim(), fill: i === 0,
          borderWidth: 2, borderDash: i ? [6, 3] : [], pointRadius: 2, tension: 0, spanGaps: false };
      });
      chartUpdate('cb-activity-chart', 'line', times.map(time), datasets, false, false);
      ['local', 'peer'].forEach(function (side, index) {
        var data = series[index], widths = widthSummary(current[side], 'advertisers'), color = palette[index ? 2 : 0];
        putChart('cb-' + side + '-spark', 'line', data.labels.map(time), [{ label: 'Saved records', data: data.values, borderColor: color, backgroundColor: css.getPropertyValue('--accent-bg').trim(), fill: !index, pointRadius: 0, borderWidth: 1.5, tension: 0, spanGaps: false }], {
          responsive: true, maintainAspectRatio: false, animation: false, plugins: { legend: { display: false }, tooltip: { callbacks: { label: function (ctx) { return amount(ctx.raw) + ' saved records'; } } } }, scales: { x: { display: false }, y: { display: false, beginAtZero: true } }
        });
        text(el('cb-' + side + '-donut-title'), 'Own adverts heard by ' + label(side));
        text(el('cb-' + side + '-donut-total'), amount(widths[0].total));
        putChart('cb-' + side + '-donut', 'doughnut', WIDTHS.map(function (bytes) { return bytes + '-byte'; }), [{ data: widths.map(function (w) { return w.count; }), backgroundColor: palette.slice(0, 3), borderColor: surface, borderWidth: 2 }], {
          responsive: true, maintainAspectRatio: false, animation: false, cutout: '72%', plugins: {
            legend: { position: 'bottom', labels: { color: ink, boxWidth: 11, font: { size: 12 } } },
            tooltip: { callbacks: { label: function (ctx) { var w = widths[ctx.dataIndex]; return w.bytes + '-byte: ' + amount(w.count) + ' / ' + amount(w.total) + ' (' + pct(w.percent) + ')'; } } }
          }
        });
        paragraphs('cb-' + side + '-donut-values', [widths.map(function (w) { return w.bytes + 'B ' + amount(w.count) + ' (' + pct(w.percent) + ')'; }).join(' · '), amount(current[side].unknownAdvertisers) + ' without a qualifying own advert']);
      });
    }
    function widthChart(id, field, target) {
      var pairs = [widthSummary(current.local, field), widthSummary(current.peer, field)], palette = colors();
      chartUpdate(id, 'bar', [label('local'), label('peer')], WIDTHS.map(function (n, i) { return { label: n + '-byte', data: [pairs[0][i].percent, pairs[1][i].percent], backgroundColor: palette[i] }; }), true, true);
      paragraphs(target, pairs.map(function (values, side) { return label(side ? 'peer' : 'local') + ': ' + values.map(function (v) { return v.bytes + 'B ' + amount(v.count) + (v.percent == null ? '' : ' (' + pct(v.percent) + ')'); }).join(' · ') +
        (field === 'advertisers' ? ' · ' + amount((side ? current.peer : current.local).unknownAdvertisers) + ' without a qualifying own advert' : ' · saved flood records'); }));
    }
    function trends() {
      var values = [];
      ['local', 'peer'].forEach(function (side) {
        var series = timelineData(current[side]), palette = colors();
        text(el('cb-' + side + '-trend-title'), label(side));
        chartUpdate('cb-' + side + '-trend', 'line', series.labels.map(time), series.series.map(function (s, i) {
          return { label: s.bytes + '-byte', data: s.values, borderColor: palette[i], backgroundColor: palette[i], borderWidth: 2,
            borderDash: [[], [6, 3], [2, 3]][i], pointStyle: ['circle', 'rect', 'triangle'][i], pointRadius: 2, tension: 0, spanGaps: false };
        }), false, true, series.counts);
        series.labels.forEach(function (at, i) { values.push([label(side), time(at), amount(series.counts[i])].concat(series.series.map(function (s) { return pct(s.values[i]); }))); });
      });
      var body = el('cb-trend-values');
      while (body.children.length > values.length) body.lastChild.remove();
      values.forEach(function (row, index) {
        if (!body.children[index]) { var tr = document.createElement('tr'); row.forEach(function () { tr.appendChild(document.createElement('td')); }); body.appendChild(tr); }
        row.forEach(function (v, i) { text(body.children[index].children[i], v); });
      });
    }
    function scopes() {
      var result = scopeComparison(current.local, current.peer), palette = colors();
      function color(key) {
        var fixed = { unscoped: 0, 'transport-zero': 1, unknown: 3, __other__: 6 };
        if (fixed[key] != null) return palette[fixed[key]];
        var hash = 0; for (var i = 0; i < key.length; i++) hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
        return palette[[2, 4, 5][hash % 3]];
      }
      chartUpdate('cb-scope-chart', 'bar', [label('local'), label('peer')], result.labels.map(function (name, i) { return { label: name, data: [result.local[i], result.peer[i]], backgroundColor: color(result.keys[i]) }; }), true, true);
      paragraphs('cb-scope-values', ['local', 'peer'].map(function (side) { return label(side) + ': ' + (result.labels.length ? result.labels.map(function (name, i) { return name + ' ' + pct(result[side][i]); }).join(' · ') : 'No qualifying flood evidence'); }));
    }
    function regions() {
      var data = regionGrid(current), node = el('cb-region-grid');
      text(el('cb-region-note'), current.identityComplete === false ? 'Shared observer identities could not be checked; regional matches are unknown.' : 'Shared content does not establish a direction or radio border crossing. A packet can appear in more than one pair.' + (current.regionPairsTruncated ? ' Missing capped pairs are unknown.' : ''));
      var empty = node.querySelector('p'), table = node.querySelector('table');
      if (!table) {
        empty = document.createElement('p'); text(empty, 'No region-pair evidence is available for this selection.'); node.appendChild(empty);
        table = document.createElement('table'); table.className = 'cb-region-table';
        var caption = document.createElement('caption'); text(caption, 'Canada rows · peer columns · shared content'); table.appendChild(caption);
        var head = document.createElement('thead'), tr = document.createElement('tr'), corner = document.createElement('th');
        text(corner, 'Canada / peer'); tr.appendChild(corner); head.appendChild(tr); table.appendChild(head); table.appendChild(document.createElement('tbody')); node.appendChild(table);
      }
      empty.hidden = data.cells.length > 0; table.hidden = !data.cells.length;
      var header = table.tHead.rows[0], oldHeaders = new Map(Array.from(header.children).slice(1).map(function (cell) { return [cell.dataset.iata, cell]; }));
      data.peer.forEach(function (iata, index) {
        var th = oldHeaders.get(iata); if (!th) { th = document.createElement('th'); th.scope = 'col'; th.dataset.iata = iata; }
        text(th, regionName(iata)); if (header.children[index + 1] !== th) header.insertBefore(th, header.children[index + 1] || null); oldHeaders.delete(iata);
      }); oldHeaders.forEach(function (cell) { cell.remove(); });
      var body = table.tBodies[0], oldRows = new Map(Array.from(body.children).map(function (row) { return [row.dataset.iata, row]; }));
      var max = Math.max(1, ...data.cells.map(function (c) { return c.value || 0; }));
      data.local.forEach(function (iata, i) {
        var row = oldRows.get(iata);
        if (!row) { row = document.createElement('tr'); row.dataset.iata = iata; var th = document.createElement('th'); th.scope = 'row'; row.appendChild(th); }
        text(row.children[0], regionName(iata));
        var oldCells = new Map(Array.from(row.children).slice(1).map(function (cell) { return [cell.dataset.peer, cell]; }));
        data.peer.forEach(function (peer, j) {
          var cell = data.cells[i * data.peer.length + j], td = oldCells.get(peer);
          if (!td) { td = document.createElement('td'); td.dataset.peer = peer; var button = document.createElement('button'); button.type = 'button'; button.dataset.local = iata; button.dataset.peer = peer; td.appendChild(button); }
          var button = td.firstElementChild;
          text(button, cell.value == null ? '?' : amount(cell.value));
          button.setAttribute('aria-label', regionName(iata) + ' and ' + regionName(peer) + ': ' + (cell.value == null ? 'unknown matches' : amount(cell.value) + ' shared contents. Compare regions'));
          button.style.setProperty('--cb-cell-strength', cell.value == null ? '0%' : Math.round(8 + cell.value / max * 42) + '%');
          if (row.children[j + 1] !== td) row.insertBefore(td, row.children[j + 1] || null); oldCells.delete(peer);
        }); oldCells.forEach(function (cell) { cell.remove(); });
        if (body.children[i] !== row) body.insertBefore(row, body.children[i] || null); oldRows.delete(iata);
      }); oldRows.forEach(function (row) { row.remove(); });
    }
    function radios() {
      var palette = colors(), sides = [current.local, current.peer];
      chartUpdate('cb-radio-chart', 'bar', [label('local'), label('peer')], [
        { label: 'Transmitting (TX)', data: sides.map(function (s) { return s.radio && s.radio.status === 'available' ? rate(s.radio.txAirtimePct) : null; }), backgroundColor: palette[0], stack: 'tx' },
        { label: 'Received packets (RX estimate)', data: sides.map(function (s) { return s.radio && s.radio.status === 'available' ? rate(s.radio.rxAirtimePct) : null; }), backgroundColor: palette[1], stack: 'rx' }
      ], true, true);
      paragraphs('cb-radio-values', sides.map(function (s, i) { var radio = s.radio || {}; return label(i ? 'peer' : 'local') + ': TX ' + pct(radio.status === 'available' ? radio.txAirtimePct : null) + ' · RX ' + pct(radio.status === 'available' ? radio.rxAirtimePct : null) +
        ' · ' + amount(radio.reportingObservers) + ' reporting radios · Latest available report: ' + reportTime(latestRadioReport(radio)) + (radio.excludedIntervals ? ' · ' + amount(radio.excludedIntervals) + ' invalid/gapped intervals omitted' : '') + (radio.truncated ? ' · sample capped' : ''); }));
      var ranking = radioRanking(current.local, current.peer);
      paragraphs('cb-radio-ranking', ranking.length ? ranking.map(function (r) { return (r.name || r.id.slice(0, 12)) + ' · ' + label(r.source) + ' / ' + regionName(r.iata || '?') + '\nTX ' + pct(r.txAirtimePct) + ' · RX ' + pct(r.rxAirtimePct) +
        ' · Relay: ' + (r.reportedCanRelay === true ? 'reported on' : r.reportedCanRelay === false ? 'reported off' : 'unknown'); }) : ['No usable airtime intervals in this selection.']);
    }
    function matrix() {
      var query = String(ui.repeater || '').toLowerCase(), selected = repeaterRows.filter(function (r) { return !query || (r.name + ' ' + r.publicKey).toLowerCase().includes(query); });
      page = Math.min(Math.max(0, Number(ui.repeaterPage) || 0), Math.max(0, Math.ceil(selected.length / PAGE_SIZE) - 1));
      var shown = selected.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE), body = el('cb-carry-rows'), existing = new Map();
      Array.from(body.children).forEach(function (row) { existing.set(row.dataset.key, row); });
      var cursor = body.firstElementChild;
      shown.forEach(function (item) {
        var key = item.source + ':' + item.publicKey, row = existing.get(key);
        if (!row) { row = document.createElement('tr'); row.dataset.key = key; for (var i = 0; i < 7; i++) row.appendChild(document.createElement('td')); var button = document.createElement('button'); button.type = 'button'; button.dataset.repeater = key; row.cells[0].appendChild(button); }
        text(row.cells[0].firstChild, (item.name || item.publicKey.slice(0, 12)) + '\n' + label(item.source));
        text(row.cells[1], WIDTHS.includes(Number(item.ownAdvertWidth)) ? item.ownAdvertWidth + '-byte' : 'Unknown');
        WIDTHS.forEach(function (bytes, index) { var value = carryLabel(item, bytes); text(row.cells[index + 2], value); row.cells[index + 2].dataset.evidence = value; });
        var carried = carriedScopes(item);
        text(row.cells[5], (carried.length ? carried.join(', ') : item.carryingAmbiguous ? 'Ambiguous' : 'None observed') + (item.scopesTruncated ? ' · list limited' : ''));
        text(row.cells[6], item.declaredScopes ? rows(item.declaredScopes.names).concat(item.declaredScopes.allowsUnscoped ? ['Unscoped allowed'] : []).join(', ') || 'No named scopes reported' : 'Not reported');
        if (row !== cursor) body.insertBefore(row, cursor); cursor = row.nextElementSibling; existing.delete(key);
      }); existing.forEach(function (row) { row.remove(); });
      text(el('cb-carry-count'), selected.length ? 'Showing ' + (page * PAGE_SIZE + 1) + '–' + (page * PAGE_SIZE + shown.length) + ' of ' + selected.length + ' repeater/feed entries.' : 'No matching repeater evidence in this selection.');
      text(el('cb-carry-page'), 'Page ' + (page + 1) + ' of ' + Math.max(1, Math.ceil(selected.length / PAGE_SIZE)));
      el('cb-carry-prev').disabled = page === 0; el('cb-carry-next').disabled = (page + 1) * PAGE_SIZE >= selected.length;
      var detail = repeaterRows.find(function (r) { return r.source + ':' + r.publicKey === ui.node; });
      el('cb-repeater-detail').hidden = !ui.node;
      text(el('cb-repeater-title'), detail ? (detail.name || 'Repeater') + ' · ' + label(detail.source) : 'Repeater not in this snapshot');
      text(el('cb-repeater-facts'), detail ? detail.publicKey + '\nOwn advert: ' + (detail.ownAdvertWidth ? detail.ownAdvertWidth + '-byte at ' + time(detail.ownAdvertAt) : 'Unknown') +
        '\nDistinct content carried: ' + amount(detail.sampleForwardedPackets) + '\nLatest carrying evidence: ' + time(detail.lastForwardedAt) + '\nReported scope time: ' + time(detail.declaredScopes && detail.declaredScopes.reportedAt) : 'Refresh or choose another repeater. Missing evidence is not a confirmed block.');
    }
    function applyTab() {
      var tab = normalizeTab(ui.tab || (ui.node ? 'repeaters' : 'overview'));
      root.dataset.tab = tab;
      root.querySelectorAll('#cb-analytics-tabs button').forEach(function (button) { var selected = button.dataset.tab === tab; button.classList.toggle('active', selected); button.setAttribute('aria-selected', String(selected)); button.tabIndex = selected ? 0 : -1; });
      el('cb-tab-panel').setAttribute('aria-labelledby', 'cb-tab-' + tab);
      root.querySelectorAll('[data-cb-views]').forEach(function (section) { section.hidden = section.dataset.cbViews.split(' ').indexOf(tab) < 0; });
      if (resizeFrame != null) cancelAnimationFrame(resizeFrame);
      resizeFrame = requestAnimationFrame(function () { resizeFrame = null; charts.forEach(function (chart) { if (chart.canvas.offsetParent) { chart.resize(); chart.update('none'); } }); });
    }
    function view(change, replace) { ui = Object.assign({}, ui, change); callbacks.onView(change, replace); matrix(); applyTab(); }
    function click(event) {
      var button = event.target.closest('button'); if (!button || !root.contains(button)) return;
      if (button.dataset.local && button.dataset.peer) callbacks.onPair(button.dataset.local, button.dataset.peer);
      if (button.id === 'cb-carry-prev') view({ repeaterPage: String(Math.max(0, page - 1)) });
      if (button.id === 'cb-carry-next') view({ repeaterPage: String(page + 1) });
      if (button.dataset.repeater) { view({ node: button.dataset.repeater }); el('cb-repeater-title').focus(); }
      if (button.id === 'cb-repeater-close') { var previous = ui.node; view({ node: '' }); var trigger = Array.from(el('cb-carry-rows').querySelectorAll('button')).find(function (b) { return b.dataset.repeater === previous; }); if (trigger) trigger.focus(); }
    }
    function search() { view({ repeater: el('cb-repeater-search').value.slice(0, 80), repeaterPage: '' }, true); }
    function repaint() { if (current.local) update({ dashboard: current }, ui); }
    function update(response, filters) {
      ui = filters || ui; current = response.dashboard || {};
      root.hidden = !current.local || !current.peer;
      if (root.hidden) return;
      applyTab();
      el('cb-repeater-search').value = ui.repeater || '';
      ['local', 'peer'].forEach(function (side) {
        var source = current[side]; text(el('cb-dash-' + side), available(source) ? amount(source.contents) : 'Unavailable');
        text(el('cb-records-' + side), available(source) ? amount(source.records) : 'Unavailable');
        text(el('cb-dash-' + side + '-note'), rows(source.regions).map(function (r) { return regionName(r.iata); }).join(' · ') || 'No selected-region evidence');
      });
      text(el('cb-peer-kpi-label'), 'Saved records · ' + label('peer'));
      text(el('cb-radio-reporters'), ['local', 'peer'].map(function (side) { var radio = current[side].radio || {}; return radio.status === 'available' ? amount(radio.reportingObservers) : 'Unknown'; }).join(' / '));
      text(el('cb-radio-reporters-note'), label('local') + ' / ' + label('peer'));
      text(el('cb-dash-shared'), current.identityComplete === true ? amount(current.independentSharedContents) : 'Unknown');
      text(el('cb-dashboard-coverage'), ['local', 'peer'].map(function (side) { var s = current[side]; return label(side) + ': ' + amount(s.records) + ' saved records' + (s.truncated ? ' · capped, coverage starts ' + time(s.coveredFromAt) : '') + (s.nodeIndexComplete === false ? ' · repeater identities incomplete' : '') + (s.repeatersTruncated ? ' · repeater list capped' : ''); }).join('  |  '));
      overview(); widthChart('cb-advert-chart', 'advertisers', 'cb-advert-values'); widthChart('cb-width-chart', 'records', 'cb-width-values'); trends(); scopes(); regions(); radios();
      repeaterRows = [];
      ['local', 'peer'].forEach(function (side) { rows(current[side].repeaters).slice(0, MAX_ROWS).forEach(function (row) { if (typeof row.publicKey === 'string' && row.publicKey) repeaterRows.push(Object.assign({}, row, { source: side })); }); });
      repeaterRows.sort(function (a, b) { return (b.sampleForwardedPackets || 0) - (a.sampleForwardedPackets || 0) || String(a.name).localeCompare(String(b.name)); }); matrix(); applyTab();
    }
    if (typeof initTabBar === 'function') initTabBar(el('cb-analytics-tabs'), function (button) { view({ tab: button.dataset.tab }); });
    root.addEventListener('click', click); el('cb-repeater-search').addEventListener('input', search); window.addEventListener('theme-changed', repaint);
    return { update: update, setView: function (filters) { ui = filters; if (current.local) { el('cb-repeater-search').value = ui.repeater || ''; matrix(); applyTab(); } }, destroy: function () { if (resizeFrame != null) cancelAnimationFrame(resizeFrame); root.removeEventListener('click', click); el('cb-repeater-search').removeEventListener('input', search); window.removeEventListener('theme-changed', repaint); charts.forEach(function (c) { c.destroy(); }); charts.clear(); repeaterRows = []; current = {}; } };
  };
  window.CrossBorderDashboard = api;
})();
