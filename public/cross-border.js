/* Peer telemetry stays on this page; local telemetry is reference evidence. */
'use strict';
(function () {
  var WINDOWS = [15, 60, 180, 360];
  var PACKET_LIMIT = 100;
  var METADATA_LIMIT = 200;
  var OBSERVATION_LIMIT = 8;
  var active = null;
  var QUERY_FILTERS = ['localIata', 'peerIata', 'scope', 'windowMinutes'];
  var dashboardAPI = typeof CrossBorderDashboard !== 'undefined' ? CrossBorderDashboard :
    (typeof module !== 'undefined' && module.exports && typeof require === 'function' ? require('./cross-border-dashboard') : null);
  var flowAPI = typeof CrossBorderFlow !== 'undefined' ? CrossBorderFlow :
    (typeof module !== 'undefined' && module.exports && typeof require === 'function' ? require('./cross-border-flow') : null);
  function dataKey(filters) { return JSON.stringify(QUERY_FILTERS.map(function (key) { return filters[key]; })); }
  var REGION_LABELS = { YKF:'Waterloo', YYZ:'Toronto', YHM:'Hamilton', YCM:'Niagara', YLK:'Barrie', XPH:'Port Hope',
    BUF:'Buffalo', ROC:'Rochester', SYR:'Syracuse', YGK:'Kingston', YOW:'Ottawa–Gatineau', YUL:'Montréal' };
  function regionLabel(code, regions) {
    code=String(code||'');if(!/^[A-Z0-9]{3}$/.test(code))return code;
    var option=(Array.isArray(regions)?regions:[]).slice(0,500).find(function(r){return r && r.iata===code;});
    var label=String(option && option.label || '');
    if(!label || label===code) label=REGION_LABELS[code] || code;
    return label===code || new RegExp('\\b'+code+'\\b').test(label) ? label : label+' ('+code+')';
  }
  function regionChoices(regions, selected) {
    var selectedCodes=new Set(String(selected || '').split(',').map(function(s){return s.trim().toUpperCase();}));
    var unique=new Map();
    (Array.isArray(regions)?regions:[]).slice(0,500).forEach(function(row){
      if(!row || typeof row.iata!=='string' || !/^[A-Z0-9]{3}$/.test(row.iata) || selectedCodes.has(row.iata) || unique.has(row.iata))return;
      unique.set(row.iata,{iata:row.iata,label:regionLabel(row.iata,[row])});
    });
    return Array.from(unique.values()).sort(function(a,b){return a.label.localeCompare(b.label);});
  }

  function normalizeFilters(input) {
    input = input || {};
    function iata(value) {
      var items = String(Array.isArray(value) ? value.join(',') : (value || '')).split(',');
      return Array.from(new Set(items.map(function (s) { return s.trim().toUpperCase(); }).filter(Boolean))).join(',');
    }
    var minutes = Number(input.windowMinutes);
    var result = {
      localIata: iata(input.localIata), peerIata: iata(input.peerIata),
      scope: String(input.scope || 'all'), windowMinutes: WINDOWS.indexOf(minutes) >= 0 ? minutes : '',
      packet: String(input.packet || ''), quality: input.quality === '1' ? '1' : ''
    };
    ['evidence', 'live', 'sources', 'advancedRegions'].forEach(function (key) { if (input[key] === '0' || input[key] === '1') result[key] = input[key]; });
    if (input.repeater) result.repeater = String(input.repeater).slice(0, 80);
    if (input.node) result.node = String(input.node).slice(0, 140);
    if (Number(input.repeaterPage) > 0) result.repeaterPage = String(Math.min(39, Math.floor(Number(input.repeaterPage))));
    if (input.tab != null && dashboardAPI) result.tab = dashboardAPI.normalizeTab(String(input.tab));
    if (input.direction != null) result.direction = input.direction === 'ca_us' || input.direction === 'us_ca' ? input.direction : 'both';
    if (input.path) result.path = String(input.path).slice(0, 160);
    if (input.scopeSearch) result.scopeSearch = String(input.scopeSearch).slice(0, 64);
    if (input.lat != null && input.lon != null && input.lat !== '' && input.lon !== '') {
      var lat = Number(input.lat), lon = Number(input.lon), zoom = Number(input.zoom || 8);
      if (Number.isFinite(lat) && Number.isFinite(lon) && Math.abs(lat) <= 90 && Math.abs(lon) <= 180 && Number.isFinite(zoom)) {
        result.lat = String(lat); result.lon = String(lon); result.zoom = String(Math.min(19, Math.max(2, zoom)));
      }
    }
    return result;
  }

  function numberLabel(value) {
    return (typeof value === 'number' || (typeof value === 'string' && value.trim() !== '')) && Number.isFinite(Number(value))
      ? Number(value).toLocaleString() : 'Unknown';
  }

  function scopeLabel(scope) {
    if (scope && scope.kind === 'unscoped') return 'Unscoped';
    return scope && scope.kind === 'named' && scope.name ? String(scope.name) : 'Unknown scope';
  }

  function ageAt(timestamp, snapshot) {
    var then = timestamp ? Date.parse(timestamp) : NaN;
    var now = snapshot ? Date.parse(snapshot) : NaN;
    if (!Number.isFinite(then) || !Number.isFinite(now)) return 'Age unknown';
    var seconds = Math.floor((now - then) / 1000);
    if (seconds < 0) return 'After snapshot';
    if (seconds < 60) return seconds + 's ago';
    if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago';
    if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago';
    return Math.floor(seconds / 86400) + 'd ago';
  }

  function uniqueRows(items, key, limit) {
    var result = [], seen = new Set();
    if (!Array.isArray(items)) return result;
    for (var i = 0; i < items.length && result.length < limit; i++) {
      var item = items[i];
      if (!item || typeof item[key] !== 'string' || !item[key] || seen.has(item[key])) continue;
      seen.add(item[key]);
      result.push(item);
    }
    return result;
  }
  function qualityNotice(data) {
    if (data.enabled === false) return '';
    var complete = data.completeness || {}, sources = data.sources || {};
    var sourceRows = [sources.peer || {}, sources.local || {}], notes = [];
    if (data.dashboard) {
      var dashboardSources = [data.dashboard.local || {}, data.dashboard.peer || {}];
      if (dashboardSources.some(function (source) { return source.status === 'unavailable'; })) notes.push('One or more sources are unavailable.');
      if (dashboardSources.some(function (source) { return source.truncated; })) notes.push('Some evidence is omitted by sample limits; gaps remain unknown.');
      if (dashboardSources.some(function (source) { return source.nodeIndexComplete === false; })) notes.push('Repeater identities are incomplete.');
      if (dashboardSources.some(function (source) { return source.repeatersTruncated; })) notes.push('The repeater list is limited.');
      if (dashboardSources.some(function (source) { return source.scopesTruncated; })) notes.push('Scope categories are limited.');
      if (data.dashboard.regionPairsTruncated) notes.push('Region comparisons are limited.');
      if (dashboardSources.some(function (source) { return source.radio && source.radio.truncated; })) notes.push('Airtime reports are limited.');
      if (data.dashboard.identityComplete === false) notes.push('Observer identity check incomplete for regional matches.');
      if (sourceRows.some(function (source) { return source.status === 'stale'; })) notes.push('One or more sources contain stale evidence.');
      if (sourceRows.some(function (source) { return source.latestAfterSnapshotAt != null; })) notes.push('Observations after the snapshot cutoff were excluded; refresh to compare current evidence.');
      if(flowAPI)notes=notes.concat(flowAPI.scopeLimitMessages(data));
      if(data.dashboard.flow && data.dashboard.flow.identityComplete===false)notes.push('Repeater identity coverage is incomplete for cross-border paths.');
      var detailOnlyLimit=complete.packetsTruncated===true || complete.peerRepeatersTruncated===true;
      if(!notes.length && !detailOnlyLimit && (data.status==='partial' || complete.complete===false))notes.push('Some comparison evidence is incomplete.');
      return notes.length ? 'Partial comparison. ' + notes.join(' ') : '';
    }
    if (data.status === 'partial' || complete.complete === false) notes.push('Partial comparison.');
    if (data.status === 'unavailable' || sourceRows.some(function (source) { return source.status === 'unavailable'; })) notes.push('One or more sources are unavailable.');
    if (sourceRows.some(function (source) { return source.status === 'stale'; })) notes.push('One or more sources contain stale evidence.');
    if (sourceRows.some(function (source) { return source.latestAfterSnapshotAt != null; })) notes.push('Observations after the snapshot cutoff were excluded; refresh to compare current evidence.');
    if (complete.packetsTruncated === true || complete.peerRepeatersTruncated === true || sourceRows.some(function (source) {
      return source.observationsTruncated === true || source.observersTruncated === true || source.nodesTruncated === true;
    })) notes.push('Some evidence is omitted by sample limits.');
    if (complete.complete == null) notes.push('Comparison completeness is unknown.');
    if (Number(data.summary && data.summary.identityUnverifiedPackets) > 0) notes.push('Observer identity check incomplete for some packets.');
    return notes.join(' ');
  }

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { normalizeFilters, dataKey, numberLabel, scopeLabel, ageAt, uniqueRows, qualityNotice, regionLabel, regionChoices };
  }
  if (typeof registerPage !== 'function') return;

  function text(node, value) {
    value = String(value);
    if (node.textContent !== value) node.textContent = value;
  }
  function known(value) { return value == null || value === '' ? 'Unknown' : String(value); }
  function array(value) { return Array.isArray(value) ? value : []; }
  function list(value) {
    var values = array(value);
    return values.length ? values.slice(0, 20).map(String).join(', ') + (values.length > 20 ? ' … (list capped)' : '') : 'None observed';
  }
  function reported(value, timestamp) {
    return known(value) + '\nReported: ' + known(timestamp);
  }
  function scopes(value) { return Array.isArray(value) ? list(value.map(scopeLabel)) : 'Unknown scope'; }
  function widths(value) { return array(value).length ? list(value) + ' bytes' : 'Unknown'; }
  function sourceAvailable(data, source) {
    return ['available', 'stale', 'empty', 'after_snapshot'].indexOf(data.sources && data.sources[source] && data.sources[source].status) >= 0;
  }
  function referenceAvailable(data) { return sourceAvailable(data, 'local'); }
  function overlapLabel(value) {
    return ({ independent: 'Match · distinct observers', shared_observer_only: 'Match · same observer present',
      shared_feed_identity: 'Match · shared-feed identity',
      identity_unverified: 'Observer identity check incomplete', not_seen_locally: 'No reference match', local_unavailable: 'Reference unavailable' })[value] || 'Unknown';
  }
  function locationLabel(location) {
    if (!location || location.lat == null || location.lon == null) return 'Location unknown';
    return 'Reported location: ' + numberLabel(location.lat) + ', ' + numberLabel(location.lon) +
      '\nReported: ' + known(location.reportedAt);
  }
  function hashFor(filters) { return URLState.buildHash('cross-border', filters); }
  function windowLabel(minutes) { return minutes < 60 ? minutes + ' minutes' : minutes / 60 + (minutes === 60 ? ' hour' : ' hours'); }
  function setHash(state, replace) {
    var hash = hashFor(state.filters);
    if (location.hash !== hash) history[replace ? 'replaceState' : 'pushState'](null, '', hash);
  }

  function table(id, caption, headings) {
    return '<div class="cb-table-scroll" tabindex="0" role="region" aria-label="' + caption + '">' +
      '<table class="cb-table"><caption>' + caption + '</caption><thead><tr>' +
      headings.map(function (label) { return '<th scope="col">' + label + '</th>'; }).join('') +
      '</tr></thead><tbody id="' + id + '"></tbody></table></div>';
  }
  function pageHtml() {
    return '<div class="cb-page analytics-page">' +
      '<header class="cb-header analytics-header"><div><a href="#/tools">Tools</a><h1>Cross-border traffic</h1>' +
      '<p id="cb-intro">Canada ↔ USA · See where packets cross, which scopes they use, and which repeaters carry them.</p></div>' +
      '<div class="cb-live-controls"><label><input type="checkbox" id="cb-live" checked> Live updates · every minute</label><button type="button" id="cb-refresh">Refresh</button></div></header>' +
      '<p id="cb-status" role="status" aria-live="polite">Loading peer telemetry…</p>' +
      '<form id="cb-filters" class="cb-filters" aria-label="Border link filters">' +
      '<div class="cb-region-picker"><label for="cb-local-add">Canadian listener regions</label><div id="cb-local-chips" class="cb-region-chips" aria-label="Selected Canadian regions"></div><select id="cb-local-add" aria-label="Add Canadian listener region"><option value="">Add a region…</option></select></div>' +
      '<div class="cb-region-picker"><label for="cb-peer-add">US listener regions</label><div id="cb-peer-chips" class="cb-region-chips" aria-label="Selected US regions"></div><select id="cb-peer-add" aria-label="Add US listener region"><option value="">Add a region…</option></select></div>' +
      '<div class="cb-scope-picker"><label>Find a scope<input id="cb-scope-search" type="search" placeholder="Name or region" maxlength="64" autocomplete="off"></label><label>Scope<select id="cb-scope" name="scope"><option value="all">All scopes</option><option value="unscoped">Unscoped</option><option value="unknown">Scoped · name unresolved</option></select></label></div>' +
      '<label class="cb-window-select" hidden>Window<select id="cb-window" name="windowMinutes"><option value="">Operator default</option></select></label><button type="submit">Apply filters</button>' +
      '<div id="cb-window-pills" class="analytics-time-range cb-window-pills" role="group" aria-label="Comparison time window"></div>' +
      '<div id="cb-scope-shortcuts" class="cb-filter-shortcuts" aria-label="Common scopes"></div><p id="cb-scope-context" class="cb-filter-help"></p>' +
      '<details id="cb-region-advanced" class="cb-region-advanced"><summary>Enter region codes directly</summary><div><label>Canadian region codes<input id="cb-local" name="localIata" list="cb-local-options" placeholder="e.g. YKF,YYZ" maxlength="160" autocomplete="off"></label><datalist id="cb-local-options"></datalist><label>US region codes<input id="cb-peer" name="peerIata" list="cb-peer-options" placeholder="e.g. BUF,ROC,SYR" maxlength="160" autocomplete="off"></label><datalist id="cb-peer-options"></datalist></div></details>' +
      '<p class="cb-filter-help">Regions select listener sources. Choose regions and a scope, then Apply filters. Radio load includes all scopes.</p></form>' +
      '<details id="cb-source-details" class="cb-source-details"><summary>Listener sources and freshness</summary><div class="cb-sources"><section class="cb-source" aria-labelledby="cb-peer-title"><h2 id="cb-peer-title">US / peer listeners</h2><p id="cb-peer-source"></p></section>' +
      '<section class="cb-source cb-reference" aria-labelledby="cb-local-title"><h2 id="cb-local-title">Canadian listeners</h2><p id="cb-local-source"></p></section></div></details>' +
      '<div id="cb-results" hidden><p id="cb-quality-warning" class="cb-warning" role="status" hidden></p>' +
      (typeof CrossBorderDashboard !== 'undefined' ? CrossBorderDashboard.html() : '<p class="cb-warning">The dashboard could not load. Inspect the saved evidence below.</p>') +
      '<details id="cb-evidence" class="cb-evidence-disclosure"><summary>Inspect packet and metadata evidence</summary><p class="cb-note">Packet details include Canadian matches in other scopes to help trace the same content.</p><p id="cb-detail-quality-warning" class="cb-note" hidden></p><dl class="cb-summary">' +
      '<div><dt>Peer packets</dt><dd id="cb-peer-count">Unknown</dd></div>' +
      '<div><dt>Matching Canadian packets</dt><dd id="cb-independent-count">Unknown</dd><small>Distinct observers · among peer packets</small></div>' +
      '<div><dt>Shared-identity matches</dt><dd id="cb-shared-count">Unknown</dd><small>Same observer or shared-feed identity · among peer packets</small></div>' +
      '<div class="cb-reference"><dt>All Canadian matches</dt><dd id="cb-local-count">Unknown</dd><small>Among peer packets; includes shared-identity matches</small></div></dl>' +
      '<details id="cb-quality" class="cb-quality"><summary>Data quality and limits</summary><div class="cb-quality-body">' +
      '<p class="cb-note">Missing observations are not packet loss or evidence of misconfiguration. Matches use packet content; observers present in both feeds do not count as distinct observers.</p>' +
      '<p class="cb-note">Retained evidence dates do not prove continuous collection. Declared scopes are the latest reports; Overview carrying uses attributable flood hops; detailed records retain individual observations. Reported radio metadata may describe hardware or a profile; radio and relay reports do not verify per-packet settings.</p>' +
      '<p id="cb-completeness" class="cb-note"></p><div class="cb-sources"><section><h3>Peer source</h3><p id="cb-peer-quality" class="cb-note"></p>' +
      '<p id="cb-peer-profile" class="cb-profile cb-note" hidden><strong>Community recommendation · reference only</strong><a></a><span></span></p></section>' +
      '<section class="cb-reference"><h3>Canadian reference source</h3><p id="cb-local-quality" class="cb-note"></p>' +
      '<p id="cb-local-profile" class="cb-profile cb-note" hidden><strong>Community recommendation · reference only</strong><a></a><span></span></p></section></div>' +
      '<ul id="cb-findings" class="cb-note"></ul></div></details>' +
      '<section aria-labelledby="cb-packets-title"><h2 id="cb-packets-title">Peer packet evidence</h2>' +
      '<p class="cb-scroll-hint cb-note">Scroll tables horizontally on smaller screens to see all evidence columns.</p>' +
      '<p id="cb-packet-count" class="cb-note"></p>' +
      table('cb-packets', 'Peer packets and Canadian reference matches', ['Packet / type', 'Scope', 'Peer observations', 'Canadian reference', 'Evidence', 'Latest observation']) +
      '<p id="cb-packets-empty" hidden>No peer packets in this selection. Try a different region, scope, or window.</p></section>' +
      '<section id="cb-detail" class="cb-detail" aria-labelledby="cb-detail-title" hidden><div class="cb-header"><h2 id="cb-detail-title" tabindex="-1">Packet evidence</h2><button id="cb-close-detail" type="button">Close evidence</button></div>' +
      '<p id="cb-detail-meta"></p><p class="cb-note">SNR and RSSI describe each listed reception only. Unknown direction is retained as uncertain evidence.</p><div class="cb-evidence-sources">' +
      '<section><h3>Peer observations · primary</h3><ul id="cb-peer-evidence"></ul></section>' +
      '<section class="cb-reference"><h3>Canadian observations · reference</h3><ul id="cb-local-evidence"></ul></section></div></section>' +
      '<section aria-labelledby="cb-repeaters-title"><h2 id="cb-repeaters-title">Peer repeaters · scopes</h2>' +
      '<p class="cb-note">Latest declared settings and observed forwarding.</p>' +
      '<p id="cb-repeater-count" class="cb-note"></p>' +
      table('cb-repeaters', 'Peer repeater scope reports and observed evidence', ['Repeater', 'Latest declared scopes', 'Observed forwarding', 'Hop widths', 'Latest evidence']) +
      '<p id="cb-repeaters-empty" hidden>No peer repeater metadata available for this selection.</p></section>' +
      '<section aria-labelledby="cb-observers-title"><h2 id="cb-observers-title">Peer observers · reported settings</h2>' +
      '<p class="cb-note">Reported radio metadata is the latest broker string, which may describe hardware or a profile. It does not verify per-packet settings.</p>' +
      '<p id="cb-observer-count" class="cb-note"></p>' +
      table('cb-observers', 'Peer observer provenance and latest reported settings', ['Observer', 'Reported radio metadata', 'Latest relay report', 'Sample evidence', 'Last seen / location']) +
      '<p id="cb-observers-empty" hidden>No peer observer metadata available for this selection.</p></section></details></div></div>';
  }

  // Each refresh visits at most the displayed cap and retains unchanged rows/cells.
  function syncRows(body, rows, key, cells, packetButtons) {
    var existing = new Map();
    Array.from(body.children).forEach(function (row) { existing.set(row.dataset.key, row); });
    var cursor = body.firstElementChild;
    rows.forEach(function (item) {
      var id = item[key], row = existing.get(id);
      var values = cells(item);
      if (!row) {
        row = document.createElement('tr'); row.dataset.key = id;
        values.forEach(function (_, index) {
          var cell = document.createElement('td');
          if (packetButtons && index === 0) {
            var button = document.createElement('button'); button.type = 'button'; button.dataset.packet = id;
            cell.appendChild(button);
          }
          row.appendChild(cell);
        });
      }
      values.forEach(function (value, index) { text(row.cells[index].firstElementChild || row.cells[index], value); });
      if (row !== cursor) body.insertBefore(row, cursor);
      cursor = row.nextElementSibling;
      existing.delete(id);
    });
    existing.forEach(function (row) { row.remove(); });
  }

  function options(node, rows, valueKey, labelKey, selected) {
    var values = array(rows).slice(0, 500).map(function (row) { return [String(row[valueKey] || ''), String(row[labelKey] || row[valueKey] || '')]; });
    if (selected && !values.some(function (value) { return value[0] === selected; })) values.unshift([selected, selected]);
    var signature = JSON.stringify(values);
    if (node.dataset.options !== signature) {
      node.replaceChildren();
      values.forEach(function (value) { var option = document.createElement('option'); option.value = value[0]; option.textContent = value[1]; node.appendChild(option); });
      node.dataset.options = signature;
    }
    if (selected != null) node.value = selected;
  }
  function controls(state) {
    state.el('cb-local').value = state.filters.localIata;
    state.el('cb-peer').value = state.filters.peerIata;
    var windowSelect = state.el('cb-window');
    if (state.filters.windowMinutes !== '' && !Array.from(windowSelect.options).some(function (option) { return option.value === String(state.filters.windowMinutes); })) {
      windowSelect.add(new Option(windowLabel(state.filters.windowMinutes), String(state.filters.windowMinutes)));
    }
    windowSelect.value = String(state.filters.windowMinutes);
    var scope = state.el('cb-scope');
    if (!Array.from(scope.options).some(function (option) { return option.value === state.filters.scope; })) {
      scope.add(new Option(state.filters.scope, state.filters.scope));
    }
    scope.value = state.filters.scope;
    state.el('cb-scope-search').value = state.filters.scopeSearch || '';
    state.el('cb-quality').open = state.filters.quality === '1';
    state.el('cb-evidence').open = state.filters.evidence === '1' || Boolean(state.filters.packet);
    state.el('cb-live').checked = state.filters.live !== '0';
    state.el('cb-source-details').open=state.filters.sources==='1';
    state.el('cb-region-advanced').open=state.filters.advancedRegions==='1';
    regionControls(state);
    windowPills(state);
  }
  function regionControls(state) {
    var opts=state.data && state.data.options || {};
    ['local','peer'].forEach(function(side){
      var field=state.el('cb-'+side), supplied=array(opts[side+'Regions']).slice(0,500);
      var labels=new Map(supplied.map(function(row){return [row.iata,regionLabel(row.iata,[row])];}));
      var selected=Array.from(new Set(String(field.value||'').split(',').map(function(s){return s.trim().toUpperCase();}).filter(function(code){return /^[A-Z0-9]{3}$/.test(code);}))).slice(0,128);
      var chips=state.el('cb-'+side+'-chips'), existing=new Map(Array.from(chips.children).map(function(button){return [button.dataset.regionCode,button];}));
      selected.forEach(function(code,index){
        var button=existing.get(code);if(!button){button=document.createElement('button');button.type='button';button.dataset.regionSide=side;button.dataset.regionCode=code;}
        var label=labels.get(code)||regionLabel(code,[]);text(button,label+' ×');button.setAttribute('aria-label','Remove '+label+' from '+(side==='local'?'Canadian':'US')+' listener regions');
        if(chips.children[index]!==button)chips.insertBefore(button,chips.children[index]||null);existing.delete(code);
      });existing.forEach(function(button){button.remove();});
      var select=state.el('cb-'+side+'-add'), choices=regionChoices(supplied,field.value);
      options(select,[{iata:'',label:selected.length?'Add a region…':'Use defaults or add a region…'}].concat(choices),'iata','label','');
      select.disabled=!choices.length;
    });
  }
  function scopeControls(state) {
    var data = state.data || {}, opts = data.options || {}, query = state.filters.scopeSearch || '';
    var catalog = typeof CrossBorderFlow !== 'undefined' ? CrossBorderFlow.catalogRows(data, query) : [];
    var known = new Set(catalog.map(function (item) { return item.filterValue; }));
    var scopeOptions = array(opts.scopes).filter(function (item) { return ['all', 'unscoped', 'unknown'].indexOf(item.value) >= 0 || !query || known.has(item.value); });
    var optionNames = new Set(scopeOptions.map(function (item) { return item.value; }));
    catalog.forEach(function (item) { if (!optionNames.has(item.filterValue)) scopeOptions.push({ value: item.filterValue, label: item.name + (item.label !== item.name ? ' · ' + item.label : '') }); });
    options(state.el('cb-scope'), scopeOptions, 'value', 'label', state.filters.scope);
    state.el('cb-scope-context').hidden=state.filters.scope==='all';
    var all = typeof CrossBorderFlow !== 'undefined' ? CrossBorderFlow.catalogRows(data, '') : [], selected = all.find(function (item) { return item.filterValue === state.filters.scope; });
    text(state.el('cb-scope-context'), selected ? selected.name + ' · ' + selected.evidence : state.filters.scope === 'all' ? 'All scopes, including unnamed scoped packets and unscoped traffic.' : state.filters.scope === 'unknown' ? 'A transport scope is present, but its name is unresolved.' : state.filters.scope === 'unscoped' ? 'No named transport scope. Transport wildcard evidence is identified separately.' : 'No catalog information for this scope.');
    var shortcuts = state.el('cb-scope-shortcuts'), common = ['all', 'onny', 'onqc', 'ykf', 'yyz', 'us-ny', 'unscoped'];
    var knownNames = new Set(all.map(function (item) { return item.name; }));
    common = common.filter(function (name) { return name === 'all' || name === 'unscoped' || knownNames.has(name); });
    var existing = new Map(Array.from(shortcuts.children).map(function (button) { return [button.dataset.scopeChoice, button]; }));
    common.forEach(function (name,index) { var button=existing.get(name); if(!button){button=document.createElement('button');button.type='button';button.dataset.scopeChoice=name;} text(button,name==='all'?'All scopes':name==='unscoped'?'Unscoped':name);button.setAttribute('aria-pressed',String(state.filters.scope===name));if(shortcuts.children[index]!==button)shortcuts.insertBefore(button,shortcuts.children[index]||null);existing.delete(name); });existing.forEach(function(button){button.remove();});
  }
  function windowPills(state) {
    var node = state.el('cb-window-pills'), selected = String(state.filters.windowMinutes);
    var minutes = Array.from(state.el('cb-window').options).map(function (option) { return Number(option.value); }).filter(function (value) { return WINDOWS.indexOf(value) >= 0; });
    var old = new Map(Array.from(node.children).map(function (button) { return [button.dataset.window, button]; }));
    minutes.forEach(function (value, index) {
      var key = String(value), button = old.get(key);
      if (!button) { button = document.createElement('button'); button.type = 'button'; button.dataset.window = key; }
      text(button, value < 60 ? value + 'm' : value / 60 + 'h'); button.setAttribute('aria-label', 'Last ' + windowLabel(value));
      button.setAttribute('aria-pressed', String(key === selected)); button.classList.toggle('active', key === selected);
      if (node.children[index] !== button) node.insertBefore(button, node.children[index] || null); old.delete(key);
    }); old.forEach(function (button) { button.remove(); });
  }
  function sourceText(source, snapshot) {
    source = source || {};
    var status = ({ available: 'Available', stale: 'Stale retained data', empty: 'No observations', unavailable: 'Unavailable',
      after_snapshot: 'Only after-snapshot observations', disabled: 'Disabled', selection_required: 'Select a region' })[source.status] || 'Status unknown';
    var stamp = source.latestObservationAt ? new Date(source.latestObservationAt) : null;
    var readable = stamp && Number.isFinite(stamp.getTime()) ? stamp.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Unknown';
    return known(source.label) + ' · ' + status + '\nRegions: ' + (array(source.selectionIata).join(', ') || 'Not selected') +
      '\nLatest observation: ' + readable + ' · ' + ageAt(source.latestObservationAt, snapshot) + ' at snapshot';
  }
  function sourceQuality(source) {
    source = source || {};
    var caps = ['observationsTruncated', 'observersTruncated', 'nodesTruncated'].filter(function (key) { return source[key] === true; });
    return known(source.label) + '\nRetained evidence from: ' + known(source.oldestStoredObservationAt) +
      '\nLatest excluded after snapshot: ' + known(source.latestAfterSnapshotAt) +
      '\nRows read: ' + numberLabel(source.rowsRead) + ' · TX excluded: ' + numberLabel(source.excludedTxObservations) +
      ' · Invalid headers excluded: ' + numberLabel(source.excludedInvalidObservations) +
      ' · Unknown direction: ' + numberLabel(source.unknownDirectionObservations) +
      '\nSource caps: ' + (caps.length ? caps.join(', ') : ([source.observationsTruncated, source.observersTruncated, source.nodesTruncated].every(function (v) { return v === false; }) ? 'Not reached' : 'Unknown'));
  }

  function profile(node, value) {
    node.hidden = !value;
    if (!value) return;
    var link = node.querySelector('a');
    text(link, known(value.label));
    link.removeAttribute('href');
    try {
      var url = new URL(value.url);
      if ((url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password) link.href = url.href;
    } catch (_) { /* Invalid public reference remains plain text. */ }
    text(node.querySelector('span'), 'Frequency: ' + numberLabel(value.frequencyMHz) + ' MHz · Bandwidth: ' + numberLabel(value.bandwidthKHz) +
      ' kHz · Spreading factor: ' + numberLabel(value.spreadingFactor) + ' · Coding-rate setting: ' + numberLabel(value.codingRate) +
      ' · Hop width: ' + numberLabel(value.hopWidthBytes) + ' bytes. These are community recommendations, not device measurements.');
  }

  function renderEvidence(state) {
    var hash = state.filters.packet;
    var detail = state.el('cb-detail');
    detail.hidden = !hash;
    if (!hash) return;
    var packet = state.packetRows.find(function (row) { return row.hash === hash; });
    text(state.el('cb-detail-title'), 'Packet evidence · ' + hash);
    text(state.el('cb-detail-meta'), packet ? overlapLabel(referenceAvailable(state.data) ? packet.overlap : 'local_unavailable') + ' · ' + scopeLabel(packet.scope) +
      (packet.observationsTruncated === true ? ' · Observation details capped' : packet.observationsTruncated === false ? ' · Observation details not capped' : ' · Observation detail completeness unknown') :
      'This packet is not in the current returned peer sample. It may be outside the filters or row cap.');
    ['peer', 'local'].forEach(function (source) {
      var listNode = state.el('cb-' + source + '-evidence');
      var observations = packet ? array(packet[source + 'Observations']).slice(0, OBSERVATION_LIMIT) : [];
      var labels = observations.map(function (observation) {
        return known(observation.observerId) + (observation.sharedObserver ? ' · Same identity in both feeds' : '') +
          '\nObserved: ' + known(observation.observedAt) + ' · Direction: ' + known(observation.direction) +
          '\nScope: ' + scopeLabel(observation.scope) + ' · Route: ' + (typeof routeTypeName === 'function' && observation.routeType != null ? routeTypeName(observation.routeType) : 'Unknown') +
          '\nSNR: ' + numberLabel(observation.snr) + ' dB · RSSI: ' + numberLabel(observation.rssi) + ' dBm' +
          '\nHop width: ' + numberLabel(observation.hopWidth) + ' bytes · Hop count: ' + numberLabel(observation.hopCount);
      });
      if (!labels.length) labels.push(source === 'local' && !referenceAvailable(state.data) ? 'Canadian reference unavailable.' : 'No listed observations in this sample.');
      while (listNode.children.length > labels.length) listNode.lastElementChild.remove();
      labels.forEach(function (label, index) {
        if (!listNode.children[index]) listNode.appendChild(document.createElement('li'));
        text(listNode.children[index], label);
      });
    });
  }

  function render(state, data) {
    state.data = data;
    var el = state.el, sources = data.sources || {}, limits = data.limits || {}, summary = data.summary || {}, complete = data.completeness || {};
    var available = referenceAvailable(data);
    var statuses = { ready: 'Comparison ready', partial: 'Partial comparison · some evidence is missing or limited', unavailable: 'Data unavailable',
      disabled: 'Border links is disabled on this instance', selection_required: 'Choose peer and Canadian reference regions to compare' };
    var updated = data.generatedAt ? new Date(data.generatedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : 'Unknown';
    var status = data.dashboard && data.enabled ? (qualityNotice(data) ? 'Partial comparison' : 'Comparison ready') : statuses[data.status] || 'Comparison status unknown';
    text(el('cb-status'), status + ' · Updated ' + updated);
    text(el('cb-intro'), 'Canada ↔ USA · See where packets cross, which scopes they use, and which repeaters carry them.');
    text(el('cb-peer-source'), sourceText(sources.peer, data.generatedAt));
    text(el('cb-local-source'), sourceText(sources.local, data.generatedAt));
    text(el('cb-peer-quality'), sourceQuality(sources.peer));
    text(el('cb-local-quality'), sourceQuality(sources.local));
    profile(el('cb-peer-profile'), sources.peer && sources.peer.referenceProfile);
    profile(el('cb-local-profile'), sources.local && sources.local.referenceProfile);
    el('cb-results').hidden = data.enabled === false;
    if (data.enabled === false) return;
    var notice = qualityNotice(data);
    text(el('cb-quality-warning'), notice);
    el('cb-quality-warning').hidden = !notice;
    var detailNotice = qualityNotice(Object.assign({}, data, { dashboard: null }));
    text(el('cb-detail-quality-warning'), detailNotice);
    el('cb-detail-quality-warning').hidden = !detailNotice;
    var peerAvailable = sourceAvailable(data, 'peer');
    text(el('cb-peer-count'), peerAvailable ? numberLabel(summary.peerPackets) : 'Unavailable');
    text(el('cb-independent-count'), available && peerAvailable ? numberLabel(summary.independentSharedPackets) : 'Unavailable');
    text(el('cb-shared-count'), available && peerAvailable ? numberLabel(
      summary.sharedObserverOnlyPackets == null || summary.sharedFeedIdentityPackets == null ? null :
        Number(summary.sharedObserverOnlyPackets) + Number(summary.sharedFeedIdentityPackets)) : 'Unavailable');
    text(el('cb-local-count'), available ? numberLabel(summary.localReferencePackets) : 'Unavailable');
    text(el('cb-completeness'), 'Completeness: ' + (complete.complete === true ? 'Within reported caps' : complete.complete === false ? 'Partial' : 'Unknown') +
      ' · Observation cap: ' + numberLabel(limits.observationRows) + ' per source · Metadata cap: ' + numberLabel(limits.metadataRows) +
      ' · Packet cap: ' + numberLabel(limits.packetRows) + ' · Details per packet: ' + numberLabel(limits.observationsPerPacket) +
      ' · Server cache: ' + numberLabel(limits.cacheSeconds) + 's. This page shows up to ' + PACKET_LIMIT + ' packets and ' + METADATA_LIMIT +
      ' observers or repeaters, with up to ' + OBSERVATION_LIMIT + ' observations per source in packet details. Counts describe the selected retained sample.');
    var findings = array(data.findings).slice(0, 20);
    var findingsNode = el('cb-findings');
    while (findingsNode.children.length > findings.length) findingsNode.lastElementChild.remove();
    findings.forEach(function (finding, index) {
      if (!findingsNode.children[index]) findingsNode.appendChild(document.createElement('li'));
      text(findingsNode.children[index], known(finding.message));
    });
    var opts = data.options || {};
    options(el('cb-local-options'), opts.localRegions, 'iata', 'label');
    options(el('cb-peer-options'), opts.peerRegions, 'iata', 'label');
    regionControls(state);
    scopeControls(state);
    var windows = array(opts.windowMinutes).map(Number).filter(function (minutes) { return WINDOWS.indexOf(minutes) >= 0; }).map(function (minutes) {
      return { value: String(minutes), label: windowLabel(minutes) };
    });
    options(el('cb-window'), windows, 'value', 'label');
    el('cb-window').value = String(state.filters.windowMinutes);
    windowPills(state);

    state.packetRows = uniqueRows(data.packets, 'hash', PACKET_LIMIT);
    syncRows(el('cb-packets'), state.packetRows, 'hash', function (row) {
      return [row.hash + '\n' + (typeof payloadTypeName === 'function' && row.payloadType != null ? payloadTypeName(row.payloadType) : 'Type unknown'),
        scopeLabel(row.scope), numberLabel(row.peerObservationCount), available ? numberLabel(row.localObservationCount) : 'Unavailable',
        overlapLabel(available ? row.overlap : 'local_unavailable'), known(row.lastSeenAt) + '\n' + ageAt(row.lastSeenAt, data.generatedAt) + ' at snapshot'];
    }, true);
    text(el('cb-packet-count'), 'Showing ' + state.packetRows.length + ' of ' + (peerAvailable ? numberLabel(summary.peerPackets) : 'unknown') +
      ' peer packets.' + (complete.packetsTruncated === true || array(data.packets).length > state.packetRows.length ? ' Sample limited.' : complete.packetsTruncated === false ? '' : ' Sample completeness unknown.'));
    el('cb-packets-empty').hidden = state.packetRows.length > 0;
    text(el('cb-packets-empty'), peerAvailable ? 'No peer packets in this selection. Try a different region, scope, or window.' : 'Peer data is unavailable for this selection. No packet comparison can be made.');

    var repeaters = uniqueRows(data.peerRepeaters, 'publicKey', METADATA_LIMIT);
    syncRows(el('cb-repeaters'), repeaters, 'publicKey', function (row) {
      var declared = row.declaredScopes;
      var declaration = !declared ? 'Not reported' : (array(declared.names).length ? list(declared.names) : 'No named scopes declared') + '\nUnscoped: ' +
        (declared.allowsUnscoped === true ? 'Declared allowed' : declared.allowsUnscoped === false ? 'Not declared allowed' : 'Unknown') +
        '\nReported: ' + known(declared.reportedAt) + '\nReport completeness: ' +
        (declared.truncated === true ? 'Capped' : declared.truncated === false ? 'Complete' : 'Unknown');
      return [known(row.name) + '\n' + row.publicKey, declaration, scopes(row.observedScopes) +
        '\nUnscoped: ' + (row.observedUnscoped === true ? 'Observed' : row.observedUnscoped === false ? 'Not observed' : 'Unknown') +
        '\nUnknown scope: ' + (row.observedUnknownScope === true ? 'Observed' : row.observedUnknownScope === false ? 'Not observed' : 'Unknown') +
        '\nSample packets: ' + numberLabel(row.sampleForwardedPackets), widths(row.hopWidths),
        'Forwarded: ' + known(row.lastForwardedAt) + '\nSeen: ' + known(row.lastSeenAt) + '\n' + locationLabel(row.location)];
    });
    text(el('cb-repeater-count'), 'Showing ' + repeaters.length + ' of ' + array(data.peerRepeaters).length +
      ' returned peer repeaters.' + (complete.peerRepeatersTruncated === true || array(data.peerRepeaters).length > repeaters.length ? ' Sample limited.' : complete.peerRepeatersTruncated === false ? '' : ' Sample completeness unknown.'));
    el('cb-repeaters-empty').hidden = repeaters.length > 0;

    var observers = uniqueRows(data.peerObservers, 'id', METADATA_LIMIT);
    syncRows(el('cb-observers'), observers, 'id', function (row) {
      return [known(row.name) + '\n' + row.id + ' · ' + known(row.iata) + (row.sharedIdentity ? '\nSame identity in both feeds' : ''),
        reported(row.latestReportedRadioHardware, row.radioHardwareReportedAt), reported(row.reportedCanRelay === true ? 'Can relay: Yes' : row.reportedCanRelay === false ? 'Can relay: No' : null, row.canRelayReportedAt),
        numberLabel(row.sampleObservations) + ' observations\n' + numberLabel(row.unknownDirectionObservations) + ' unknown direction\nScopes: ' + scopes(row.observedScopes) + '\nHop widths: ' + widths(row.hopWidths),
        known(row.lastSeenAt) + '\n' + ageAt(row.lastSeenAt, data.generatedAt) + ' at snapshot\n' + locationLabel(row.location)];
    });
    text(el('cb-observer-count'), 'Showing ' + observers.length + ' of ' + array(data.peerObservers).length + ' returned peer observers.' + (array(data.peerObservers).length > observers.length ? ' Some entries are not shown.' : ''));
    el('cb-observers-empty').hidden = observers.length > 0;
    renderEvidence(state);
    if (state.dashboard) state.dashboard.update(data, state.filters);
  }

  function schedule(state) {
    if (state.timer) clearTimeout(state.timer);
    state.timer = null;
    if (state !== active || state.filters.live === '0' || document.hidden || state.data.enabled === false) return;
    state.timer = setTimeout(function () {
      state.timer = null;
      if (state !== active || state.filters.live === '0' || state.data.enabled === false) return;
      if (!document.hidden && !state.loading) load(state);
      else schedule(state);
    }, 60000);
  }

  async function load(state) {
    if (state.request) state.request.abort();
    var request = new AbortController(); state.request = request;
    var generation = ++state.generation; state.loading = true;
    if (state.timer) clearTimeout(state.timer); state.timer = null;
    text(state.el('cb-status'), 'Updating comparison…');
    state.el('cb-results').setAttribute('aria-busy', 'true');
    var params = new URLSearchParams({ view: 'dashboard' });
    QUERY_FILTERS.forEach(function (key) { if (state.filters[key] !== '') params.set(key, state.filters[key]); });
    try {
      // A page-owned request can be cancelled without disturbing the shared API cache.
      var response = await fetch('/api/cross-border?' + params.toString(), { signal: request.signal });
      if (!response.ok) { var error = new Error('HTTP ' + response.status); error.status = response.status; throw error; }
      var data = await response.json();
      if (state !== active || generation !== state.generation) return;
      if (!data || typeof data.enabled !== 'boolean') throw new Error('Invalid response');
      if (data.filters) {
        state.filters = normalizeFilters(Object.assign({}, state.filters, data.filters, { packet: state.filters.packet, quality: state.filters.quality }));
        setHash(state, true); controls(state);
      }
      render(state, data);
    } catch (error) {
      if (state !== active || generation !== state.generation || error.name === 'AbortError') return;
      state.el('cb-results').hidden = true;
      text(state.el('cb-status'), error.status === 400 ? 'This filter selection is not supported. Choose region codes from the supplied options and apply again.' :
        'Border links is unavailable. Refresh to retry. Other CoreScope pages remain available.');
      text(state.el('cb-peer-source'), 'Peer feed unavailable. No current snapshot loaded.');
      text(state.el('cb-local-source'), 'Canadian reference unavailable. No comparison can be made.');
      state.el('cb-peer-profile').hidden = true; state.el('cb-local-profile').hidden = true;
    } finally {
      if (state === active && generation === state.generation) { state.loading = false; state.el('cb-results').removeAttribute('aria-busy'); schedule(state); }
    }
  }

  function init(container) {
    var state = { root: container, filters: normalizeFilters(URLState.parseHash(location.hash).params), request: null, generation: 0, packetRows: [], data: {}, timer: null, loading: false };
    state.el = function (id) { return container.querySelector('#' + id); };
    active = state;
    container.innerHTML = pageHtml();
    controls(state);
    if (typeof CrossBorderDashboard !== 'undefined') state.dashboard = CrossBorderDashboard.mount(state.el('cb-dashboard'), {
      onPair: function (local, peer) { state.filters = normalizeFilters(Object.assign({}, state.filters, { localIata: local, peerIata: peer, packet: '', node: '', repeaterPage: '' })); setHash(state, false); controls(state); load(state); },
      onView: function (change, replace) { state.filters = normalizeFilters(Object.assign({}, state.filters, change)); setHash(state, Boolean(replace)); },
      onScope: function (scope) { state.el('cb-scope').value=scope; if (state.el('cb-scope').value !== scope) state.el('cb-scope').add(new Option(scope,scope)); state.el('cb-scope').value=scope; state.el('cb-filters').requestSubmit(); }
    });
    state.submit = function (event) {
      event.preventDefault();
      var filters = normalizeFilters(Object.assign({}, state.filters, { localIata: state.el('cb-local').value, peerIata: state.el('cb-peer').value,
        scope: state.el('cb-scope').value, windowMinutes: state.el('cb-window').value, packet: '', path: '', node: '', repeaterPage: '' }));
      if ([filters.localIata, filters.peerIata].some(function (value) { return value && !/^[A-Z0-9]{3}(,[A-Z0-9]{3})*$/.test(value); })) {
        text(state.el('cb-status'), 'Enter three-character IATA codes separated by commas, or leave blank for the operator default.');
        return;
      }
      state.filters = filters; setHash(state, false); controls(state); load(state);
    };
    state.click = function (event) {
      var button = event.target.closest('button');
      if (!button || !container.contains(button)) return;
      if (button.dataset.window) { state.el('cb-window').value = button.dataset.window; state.el('cb-filters').requestSubmit(); return; }
      if (button.id === 'cb-refresh') { load(state); return; }
      if (button.dataset.regionSide && button.dataset.regionCode) { var field=state.el('cb-'+button.dataset.regionSide);field.value=String(field.value).split(',').filter(function(code){return code.trim().toUpperCase()!==button.dataset.regionCode;}).join(',');regionControls(state);return; }
      if (button.dataset.scopeChoice && state.el('cb-filters').contains(button)) { if (!Array.from(state.el('cb-scope').options).some(function(option){return option.value===button.dataset.scopeChoice;})) state.el('cb-scope').add(new Option(button.dataset.scopeChoice,button.dataset.scopeChoice));state.el('cb-scope').value=button.dataset.scopeChoice;state.el('cb-filters').requestSubmit();return; }
      if (button.dataset.packet || button.id === 'cb-close-detail') {
        var previous = state.filters.packet;
        state.filters.packet = button.dataset.packet || '';
        setHash(state, false); renderEvidence(state);
        if (state.filters.packet) state.el('cb-detail-title').focus();
        else {
          var trigger = Array.from(state.el('cb-packets').querySelectorAll('button')).find(function (node) { return node.dataset.packet === previous; });
          if (trigger) trigger.focus();
        }
      }
    };
    state.el('cb-filters').addEventListener('submit', state.submit);
    state.scopeSearch = function () { state.filters.scopeSearch=state.el('cb-scope-search').value.slice(0,64);setHash(state,true);scopeControls(state); };
    state.el('cb-scope-search').addEventListener('input',state.scopeSearch);
    state.regionAdd=function(event){var side=event.target.id==='cb-local-add'?'local':'peer',code=event.target.value;if(!code)return;var field=state.el('cb-'+side),next=field.value?field.value+','+code:code;if(next.length>160){text(state.el('cb-status'),'Too many selected regions. Remove one before adding another.');return;}field.value=next;regionControls(state);};
    state.el('cb-local-add').addEventListener('change',state.regionAdd);state.el('cb-peer-add').addEventListener('change',state.regionAdd);
    state.advancedToggle=function(){if(state!==active)return;var value=state.el('cb-region-advanced').open?'1':'0';if((state.filters.advancedRegions||'0')!==value){state.filters.advancedRegions=value;setHash(state,false);}};
    state.el('cb-region-advanced').addEventListener('toggle',state.advancedToggle);
    state.sourceToggle=function(){if(state!==active)return;var value=state.el('cb-source-details').open?'1':'0';if((state.filters.sources||'0')!==value){state.filters.sources=value;setHash(state,false);}};
    state.el('cb-source-details').addEventListener('toggle',state.sourceToggle);
    state.toggleQuality = function () {
      if (state !== active) return;
      var quality = state.el('cb-quality').open ? '1' : '';
      if (quality === state.filters.quality) return;
      state.filters.quality = quality;
      setHash(state, false);
    };
    state.el('cb-quality').addEventListener('toggle', state.toggleQuality);
    state.toggleEvidence = function () {
      if (state !== active) return;
      var value = state.el('cb-evidence').open ? '1' : '0';
      if ((state.filters.evidence || '0') === value) return;
      state.filters.evidence = value; setHash(state, false);
    };
    state.live = function () { state.filters.live = state.el('cb-live').checked ? '1' : '0'; setHash(state, false); schedule(state); };
    state.visibility = function () { if (state !== active) return; if (!document.hidden && state.filters.live !== '0' && !state.loading) load(state); else schedule(state); };
    state.el('cb-evidence').addEventListener('toggle', state.toggleEvidence);
    state.el('cb-live').addEventListener('change', state.live);
    document.addEventListener('visibilitychange', state.visibility);
    container.addEventListener('click', state.click);
    load(state);
  }

  function destroy() {
    var state = active;
    if (!state) return;
    active = null;
    state.generation++;
    if (state.request) state.request.abort();
    if (state.timer) clearTimeout(state.timer);
    if (state.dashboard) state.dashboard.destroy();
    state.el('cb-evidence').removeEventListener('toggle', state.toggleEvidence);
    state.el('cb-live').removeEventListener('change', state.live);
    document.removeEventListener('visibilitychange', state.visibility);
    state.el('cb-filters').removeEventListener('submit', state.submit);
    state.el('cb-scope-search').removeEventListener('input',state.scopeSearch);
    state.el('cb-local-add').removeEventListener('change',state.regionAdd);state.el('cb-peer-add').removeEventListener('change',state.regionAdd);
    state.el('cb-region-advanced').removeEventListener('toggle',state.advancedToggle);
    state.el('cb-source-details').removeEventListener('toggle',state.sourceToggle);
    state.el('cb-quality').removeEventListener('toggle', state.toggleQuality);
    state.root.removeEventListener('click', state.click);
    state.data = {}; state.packetRows = [];
  }
  function updateRoute() {
    var state = active;
    if (!state || !state.data.filters) return false;
    var parsed = URLState.parseHash(location.hash), allowed = QUERY_FILTERS.concat(['packet', 'quality', 'evidence', 'live', 'repeater', 'repeaterPage', 'node', 'tab', 'direction', 'path', 'scopeSearch', 'lat', 'lon', 'zoom', 'sources', 'advancedRegions']);
    if (parsed.route !== 'cross-border' || Object.keys(parsed.params).some(function (key) { return allowed.indexOf(key) < 0; })) return false;
    var next = normalizeFilters(parsed.params);
    if (dataKey(next) !== dataKey(state.filters) || dataKey(next) !== dataKey(normalizeFilters(state.data.filters))) return false;
    state.filters = next; controls(state); scopeControls(state); renderEvidence(state);
    if (state.dashboard) state.dashboard.setView(next); schedule(state);
    return true;
  }
  registerPage('cross-border', { init: init, destroy: destroy, updateRoute: updateRoute });
})();
