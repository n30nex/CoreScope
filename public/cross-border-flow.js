/* Geographic crossing evidence. The server resolves identities and computes measurements. */
'use strict';
(function () {
  var DIRECTIONS = [['ca_us', 'Canada → USA'], ['us_ca', 'USA → Canada']];
  var MAX_PATHS = 100, MAX_HOPS = 65, MAX_CATALOG = 256;
  function rows(value) { return Array.isArray(value) ? value : []; }
  function number(value) { return value != null && value !== '' && typeof value !== 'boolean' && Number.isFinite(Number(value)) && Number(value) >= 0 ? Number(value) : null; }
  function normalizeDirection(value) { return value === 'ca_us' || value === 'us_ca' ? value : 'both'; }
  function usable(flow) { return flow && flow.identityComplete!==false && ['available', 'partial', 'empty'].indexOf(flow.status) >= 0; }
  function directionSummary(flow, key) {
    var source = rows(flow && flow.directions).find(function (r) { return r.key === key; }) || {};
    var counts = ['contents', 'scoped', 'unknownScopeName', 'unscoped', 'transportWildcard', 'mixed'].map(function (field) { return usable(flow) ? number(source[field]) : null; });
    var valid = counts.every(function (value) { return value != null; }) && counts[2] <= counts[1] && counts[1] + counts[3] + counts[4] + counts[5] === counts[0];
    var categories = [['Named scope', valid ? counts[1] - counts[2] : null], ['Scoped · name unresolved', counts[2]], ['Unscoped', counts[3]], ['Transport wildcard', counts[4]], ['Mixed scope states', counts[5]]];
    return { key: key, contents: counts[0], scopedPercent: valid && counts[0] > 0 ? counts[1] / counts[0] * 100 : null,
      unscopedPercent: valid && counts[0] > 0 ? counts[3] / counts[0] * 100 : null,
      repeaters: usable(flow) ? number(source.repeaters) : null, furthestKm: usable(flow) ? number(source.furthestKm) : null,
      composition: categories.map(function (r) { return { label: r[0], count: valid ? r[1] : null, percent: valid && counts[0] > 0 ? r[1] / counts[0] * 100 : null }; }) };
  }
  function timeline(flow) {
    var sources = DIRECTIONS.map(function (d) { return rows(flow && flow.directions).find(function (r) { return r.key === d[0]; }) || {}; });
    var maps = sources.map(function (source) { return new Map(rows(source.timeline).slice(0, 360).map(function (p) { return [p.at, usable(flow) && p.complete ? number(p.contents) : null]; })); });
    var labels = Array.from(new Set(Array.from(maps[0].keys()).concat(Array.from(maps[1].keys())))).filter(function (at) { return typeof at === 'string' && Number.isFinite(Date.parse(at)); }).sort().slice(0, 720);
    return { labels: labels, series: DIRECTIONS.map(function (d, index) { return { key: d[0], label: d[1], values: labels.map(function (at) { return maps[index].has(at) ? maps[index].get(at) : null; }) }; }) };
  }
  function point(hop) {
    var p = hop && hop.location;
    if (!p || p.lat == null || p.lon == null || hop.kind === 'unknown' || hop.reason) return null;
    if (![p.lat,p.lon].every(function(v){return typeof v==='number' || typeof v==='string' && v.trim()!=='';})) return null;
    var lat = Number(p.lat), lon = Number(p.lon);
    return Number.isFinite(lat) && Number.isFinite(lon) && Math.abs(lat) <= 90 && Math.abs(lon) <= 180 && (lat !== 0 || lon !== 0) ? [lat, lon] : null;
  }
  function pathSegments(path) {
    var segments = [], current = [], previous = null;
    rows(path && path.hops).slice(0, MAX_HOPS).forEach(function (hop) {
      var location = point(hop);
      if (!location || (previous && Number(hop.index) !== Number(previous.index) + 1)) {
        if (current.length > 1) segments.push(current); current = [];
      }
      if (location) current.push(location);
      previous = location ? hop : null;
    });
    if (current.length > 1) segments.push(current);
    return segments;
  }
  function selectedPaths(flow, direction) {
    if(flow && flow.identityComplete===false)return [];
    var selected = normalizeDirection(direction), seen = new Set();
    return rows(flow && flow.paths).slice(0, MAX_PATHS).filter(function (path) {
      if (!path || typeof path.id !== 'string' || seen.has(path.id)) return false;
      seen.add(path.id); return selected === 'both' || rows(path.directions).indexOf(selected) >= 0;
    });
  }
  function catalogRows(response, query) {
    var catalog = new Map(), search = String(query || '').trim().toLowerCase();
    function filterValue(name) {return ['all','unscoped','unknown'].indexOf(name)>=0 || name.indexOf('named:')===0 ? 'named:'+name : name;}
    rows(response && response.scopeCatalog).slice(0, MAX_CATALOG).forEach(function (item) {
      if (!item || typeof item.name !== 'string' || !item.name || item.name.length > 64) return;
      var sources = rows(item.sources).slice(0, 8);
      catalog.set(item.name, { name: item.name, filterValue:filterValue(item.name), label: String(item.label || item.name), sources: sources,
        evidence: sources.map(function (s) { return String(s.label || 'Reference') + (s.status ? ' · ' + s.status : ''); }).join('; ') || 'Known name',
        regions: rows(item.meshMapperRegions).slice(0, 32).join(', ') });
    });
    rows(response && response.options && response.options.scopes).slice(0, MAX_CATALOG).forEach(function (item) {
      var value=String(item.value || '');
      if (['all', 'unscoped', 'unknown'].indexOf(value) >= 0) return;
      var name=value.indexOf('named:')===0 ? value.slice(6) : value;
      if (!name || name.length>64 || catalog.has(name)) return;
      catalog.set(name, { name:name, filterValue:filterValue(name), label:String(item.label || name), sources:[], evidence:'Observed in this sample', regions:'' });
    });
    return Array.from(catalog.values()).filter(function (item) { return !search || (item.name + ' ' + item.label + ' ' + item.evidence + ' ' + item.regions).toLowerCase().indexOf(search) >= 0; })
      .sort(function (a, b) { return a.name.localeCompare(b.name); }).slice(0, MAX_CATALOG);
  }
  function crossingRepeaters(flow) {
    if(flow && flow.identityComplete===false)return [];
    return rows(flow && flow.repeaters).slice(0,500).filter(function(r){return r && typeof r.publicKey==='string' && r.publicKey;}).sort(function(a,b){
      return (number(b.crossingContents)||0)-(number(a.crossingContents)||0) || (number(b.contents)||0)-(number(a.contents)||0) || String(a.name||a.publicKey).localeCompare(String(b.name||b.publicKey));
    }).slice(0,25);
  }
  function meshMapperStatusLabel(status) {
    var label={ready:'available',available:'available',stale:'stale cached data',loading:'loading',rate_limited:'rate limited; cached data may be shown',partial:'partially available',access_denied:'access not permitted',unavailable:'unavailable',disabled:'not enabled'}[status];
    return typeof label==='string' ? label : 'status unknown';
  }
  function scopeLimitMessages(response) {
    var messages=[];
    if(response && response.scopeResolution && response.scopeResolution.limited===true)messages.push('Scope-name matching is limited; missing matches are not proof of no traffic.');
    if(response && response.scopeCatalogTruncated===true)messages.push('The scope catalog is limited; additional names may be omitted.');
    return messages;
  }
  function hopReasonLabel(reason) {
    var label={unresolved_hop:'Repeater could not be identified',one_byte_path:'ID too short to identify reliably',identity_index_incomplete:'Identity coverage incomplete',position_unavailable:'Position unavailable',position_conflict:'Conflicting position reports',country_unresolved:'Country unknown'}[reason];
    return typeof label==='string' ? label : 'Identity or location unavailable';
  }
  function pathScopeLabel(path) {
    var matched=path.scopeKey!=='transport-zero' && path.scope && path.scope.kind==='named' && path.scope.resolution==='catalog_match';
    return scopeName(path)+(matched?' · matched against known names':'');
  }
  var api = { normalizeDirection: normalizeDirection, directionSummary: directionSummary, timeline: timeline, pathSegments: pathSegments, selectedPaths: selectedPaths, catalogRows: catalogRows, crossingRepeaters: crossingRepeaters, meshMapperStatusLabel:meshMapperStatusLabel, scopeLimitMessages:scopeLimitMessages, hopReasonLabel:hopReasonLabel, pathScopeLabel:pathScopeLabel };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  if (typeof window === 'undefined') return;

  function text(node, value) { if (node && node.textContent !== String(value)) node.textContent = String(value); }
  function amount(value) { var n = number(value); return n == null ? '—' : n.toLocaleString(); }
  function percent(value) { var n = number(value); return n == null ? '—' : n.toLocaleString(undefined, { maximumFractionDigits: 1 }) + '%'; }
  function km(value) { var n = number(value); return n == null ? 'Unknown' : n.toLocaleString(undefined, { maximumFractionDigits: 1 }) + ' km'; }
  function stamp(value) { var date = new Date(value); return value && Number.isFinite(date.getTime()) ? date.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'Unknown'; }
  function directionLabel(key) { return key === 'ca_us' ? 'Canada → USA' : key === 'us_ca' ? 'USA → Canada' : 'Both directions'; }
  function scopeName(path) { return path.scopeKey === 'transport-zero' ? 'Transport wildcard' : path.scope && path.scope.kind === 'named' ? String(path.scope.name || 'Unnamed scope') : path.scope && path.scope.kind === 'unscoped' ? 'Unscoped' : 'Scoped · name unresolved'; }
  function safeLink(url) { try { var u = new URL(url); return u.protocol === 'https:' && !u.username && !u.password ? u.href : null; } catch (_) { return null; } }
  api.html = function () {
    return '<section id="cb-flow" data-cb-views="overview" aria-label="Cross-border traffic">' +
      '<p id="cb-flow-status" class="cb-note" role="status"></p><div class="cb-flow-cards">' + DIRECTIONS.map(function (d) {
        return '<article class="analytics-card cb-direction-card" data-direction-card="' + d[0] + '"><h3>' + d[1] + '</h3><strong id="cb-flow-' + d[0] + '-count">—</strong><span class="cb-flow-unit">distinct flood packets · grouped by content</span>' +
          '<dl><div><dt>Scoped</dt><dd id="cb-flow-' + d[0] + '-scoped">—</dd></div><div><dt>Unscoped</dt><dd id="cb-flow-' + d[0] + '-unscoped">—</dd></div><div><dt>Repeaters on paths</dt><dd id="cb-flow-' + d[0] + '-repeaters">—</dd></div><div><dt title="Shortest distance from the Canada–US border using reported positions. Reception may extend farther.">Furthest into ' + (d[0] === 'ca_us' ? 'the USA' : 'Canada') + '</dt><dd id="cb-flow-' + d[0] + '-distance">—</dd></div></dl></article>';
      }).join('') + '</div><p class="cb-note">A packet can cross in both directions. These counts describe retained path evidence, not every radio transmission.</p>' +
      '<section class="cb-flow-map-section analytics-card"><div class="cb-header"><div><h2>Live cross-border paths</h2><p class="cb-note" id="cb-flow-map-status"></p></div><div class="cb-map-controls"><div class="cb-direction-controls" role="group" aria-label="Map path direction"><button type="button" data-direction="both" aria-pressed="true">Both directions</button><button type="button" data-direction="ca_us" aria-pressed="false">Canada → USA</button><button type="button" data-direction="us_ca" aria-pressed="false">USA → Canada</button></div><button type="button" id="cb-flow-fit">Fit paths</button></div></div>' +
      '<div class="cb-map-legend"><span class="cb-map-ca">━━ Canada → USA</span><span class="cb-map-us">┄┄ USA → Canada</span><span>● Repeater · ○ Receiving observer</span></div>' +
      '<div id="cb-flow-map" class="cb-flow-map" role="region" aria-label="Observed cross-border paths map"></div><p id="cb-flow-map-fallback" class="cb-note" hidden>The map is unavailable. All returned paths remain accessible in the list below.</p>' +
      '<p class="cb-note">Lines join resolved adjacent hops at reported positions. Missing hops are gaps; paths do not show terrain or prove maximum radio range.</p><p class="cb-map-sources">Country geometry: <a href="https://www.geoboundaries.org/">geoBoundaries</a> / <a href="https://www.openstreetmap.org/copyright">© OpenStreetMap</a> · Border: <a href="https://internationalboundarycommission.org/en/maps-coordinates/coordinates.php">International Boundary Commission</a></p>' +
      '<div class="cb-flow-inspector"><section><h3>Observed paths</h3><p id="cb-flow-path-count" class="cb-note"></p><div id="cb-flow-path-list" class="cb-flow-path-list" role="group" aria-label="Observed crossing paths"></div></section>' +
      '<section id="cb-flow-detail" class="cb-flow-detail" aria-labelledby="cb-flow-detail-title"><div class="cb-header"><h3 id="cb-flow-detail-title" tabindex="-1">Select a path</h3><button type="button" id="cb-flow-clear" hidden>Clear selection</button></div><p id="cb-flow-detail-summary" class="cb-note">Choose a line on the map or a path from the list to inspect its repeaters and scope.</p><ol id="cb-flow-hop-list" class="cb-flow-hop-list"></ol></section></div></section>' +
      '<div class="cb-chart-pair analytics-grid cb-flow-charts"><section class="analytics-card cb-visual"><h3>Crossing evidence over time</h3><div class="cb-chart"><canvas id="cb-flow-timeline" role="img" aria-label="Canada to USA and USA to Canada crossing evidence over time"></canvas></div><p class="cb-note">First recorded path times. Gaps remain unknown.</p><details class="cb-chart-help"><summary>Show chart values</summary><div id="cb-flow-timeline-values"></div></details></section>' +
      '<section class="analytics-card cb-visual"><h3>How much crossing traffic is scoped?</h3><div class="cb-chart"><canvas id="cb-flow-scopes" role="img" aria-label="Scope composition of crossing traffic in each direction"></canvas></div><div id="cb-flow-scope-values" class="cb-chart-values"></div><p class="cb-note">Percentages cover resolved crossings; unresolved crossing evidence is excluded. A scope can have an unresolved name. Mixed means the same content was seen under different scope states, not just different names.</p></section></div>' +
      '<details class="cb-chart-help"><summary>What these measurements mean</summary><p>Crossing totals cover received flood packets. Direction comes from resolved adjacent hops in Canada and the United States, not from the MQTT broker or which listener heard a packet first. Direct-route headers describe remaining routes and cannot establish earlier crossings. Reach is the shortest distance from the international border to a downstream observed position. A repeater or receiver can establish it; reception may extend farther. Locations are reported by devices and may have changed. Measured radio load is available under Radio load; airtime, when available, estimates one saved frame under the stated reference radio settings, not total route airtime. A repeater can change a transport scope; the recorded header does not prove every earlier hop carried that same scope.</p></details></section>' +
      '<section class="cb-dashboard-section" data-cb-views="repeaters" aria-labelledby="cb-border-repeaters-title"><h2 id="cb-border-repeaters-title">Repeaters on crossing paths</h2><p class="cb-note">Border-endpoint counts identify repeaters immediately before or after an observed crossing. Other path members may be farther inland.</p><p id="cb-border-repeaters-count" class="cb-note"></p><div class="cb-table-scroll"><table class="cb-border-repeaters-table"><caption>Most observed border endpoints in the returned sample</caption><thead><tr><th>Repeater</th><th>Country</th><th>Direction</th><th>Packets on crossing paths</th><th>Packets as border endpoint</th><th>Recorded packet scopes</th></tr></thead><tbody id="cb-border-repeaters"></tbody></table></div></section>' +
      '<section id="cb-scope-catalog" class="cb-dashboard-section" data-cb-views="scopes-regions" aria-labelledby="cb-catalog-title"><h2 id="cb-catalog-title">Known and discovered scopes</h2><p class="cb-note">Known names remain selectable even when this sample has no matching packets. Community plans, repeater reports and observed forwarding describe different evidence.</p><p id="cb-catalog-status" class="cb-note"></p><div id="cb-catalog-list" class="cb-catalog-list"></div></section>';
  };

  api.mount = function (root, callbacks) {
    var response = {}, flow = {}, ui = {}, charts = new Map(), map = null, tiles = null, mapRefTiles = null, paths = [], lines = new Map(), markers = new Map(), fitOnce = false, disposed = false, viewFrame = null, knownPaths = new Set(), hasSnapshot = false, updatingViewport = false;
    var mapper = new Map();
    function el(id) { return root.querySelector('#' + id); }
    function palette() { var css = getComputedStyle(root); return ['--accent', '--palette-purple-500', '--success', '--text-muted', '--warning', '--border', '--text'].map(function (key) { return css.getPropertyValue(key).trim(); }); }
    function change(update, replace) { ui = Object.assign({}, ui, update); callbacks.onView(update, replace); renderSelection(); }
    function listText(id, values) { var node = el(id); while (node.children.length > values.length) node.lastElementChild.remove(); values.forEach(function (v, index) { if (!node.children[index]) node.appendChild(document.createElement('p')); text(node.children[index], v); }); }
    function putChart(id, type, labels, datasets, stacked) {
      var canvas = el(id); if (typeof window.Chart !== 'function') { canvas.parentElement.hidden = true; return; }
      var colors = palette(); canvas.parentElement.hidden = false;
      var options = { responsive: true, maintainAspectRatio: false, animation: false, indexAxis: stacked ? 'y' : 'x', interaction: { mode: 'index', intersect: false }, plugins: { legend: { position: 'bottom', labels: { color: colors[6], boxWidth: 12 } }, tooltip: { callbacks: { label: function (c) { return c.dataset.label + ': ' + (stacked ? percent(c.raw) : amount(c.raw)); } } } },
        scales: { x: { stacked: stacked, beginAtZero: true, max: stacked ? 100 : undefined, grid: { color: colors[5] }, ticks: { color: colors[3], maxRotation: 0, maxTicksLimit: 8 } }, y: { stacked: stacked, beginAtZero: true, grid: { color: colors[5] }, ticks: { color: colors[3], precision: 0 } } } };
      var chart = charts.get(id);
      if (chart) { chart.data.labels = labels; chart.data.datasets = datasets; chart.options = options; chart.update('none'); }
      else { try { charts.set(id, new window.Chart(canvas, {type:type,data:{labels:labels,datasets:datasets},options:options})); } catch (_) { canvas.parentElement.hidden = true; } }
    }
    function renderCharts() {
      var colors = palette(), series = timeline(flow), summaries = DIRECTIONS.map(function (d) { return directionSummary(flow, d[0]); });
      putChart('cb-flow-timeline', 'line', series.labels.map(function (at) { return new Date(at).toLocaleTimeString([], {hour:'2-digit',minute:'2-digit'}); }), series.series.map(function (s, index) {
        return {label:s.label,data:s.values,borderColor:colors[index],backgroundColor:colors[index],borderDash:index ? [6,4] : [],pointRadius:2,borderWidth:2,spanGaps:false,tension:0};
      }), false);
      listText('cb-flow-timeline-values', series.labels.length ? series.labels.map(function (at, index) { return stamp(at) + ' · Canada → USA: ' + amount(series.series[0].values[index]) + ' · USA → Canada: ' + amount(series.series[1].values[index]); }) : ['No time-series evidence is available.']);
      putChart('cb-flow-scopes','bar',DIRECTIONS.map(function (d) {return d[1];}), summaries[0].composition.map(function (item,index) {
        return {label:item.label,data:summaries.map(function (s) {return s.composition[index].percent;}),backgroundColor:[colors[2],colors[0],colors[3],colors[1],colors[4]][index]};
      }), true);
      listText('cb-flow-scope-values', summaries.map(function (s,index) {return DIRECTIONS[index][1] + ': ' + s.composition.filter(function(c){return c.count!==0;}).map(function (c) { return c.label + ' ' + percent(c.percent); }).join(' · ');}));
    }
    function mapReady() {
      if (map) return true;
      if (!window.L || typeof window.getTileUrl !== 'function') { el('cb-flow-map-fallback').hidden = false; el('cb-flow-map').hidden = true; return false; }
      el('cb-flow-map-fallback').hidden = true; el('cb-flow-map').hidden = false;
      map = L.map(el('cb-flow-map'), {scrollWheelZoom:false,preferCanvas:true}).setView([43.4,-79.4],8);
      tiles = L.tileLayer(window.getTileUrl(), {maxZoom:19,attribution:'© OpenStreetMap © CARTO'}).addTo(map);
      syncTiles();
      map.on('moveend', function () {
        // Leaflet also emits moveend when a hidden map is resized. Its zero-size
        // center is not a user-selected viewport and must never enter the URL.
        if (disposed || updatingViewport || !map.getContainer().offsetWidth || !map.getContainer().offsetHeight) return;
        var center=map.getCenter(), viewport={lat:center.lat.toFixed(5),lon:center.lng.toFixed(5),zoom:String(map.getZoom())};
        ui=Object.assign({},ui,viewport);callbacks.onViewport(viewport);
      });
      return true;
    }
    function syncTiles() {
      if (!map || !tiles) return;
      var provider = typeof window.getActiveTileProvider === 'function' ? window.getActiveTileProvider() : null;
      tiles.setUrl(window.getTileUrl());
      var attribution=provider && provider.attribution || '© OpenStreetMap © CARTO';if(tiles.options.attribution!==attribution){map.attributionControl.removeAttribution(tiles.options.attribution);tiles.options.attribution=attribution;map.attributionControl.addAttribution(attribution);}
      if (provider && provider.refUrl) { if (!mapRefTiles) mapRefTiles = L.tileLayer(provider.refUrl,{maxZoom:19,attribution:provider.attribution || ''}).addTo(map); else mapRefTiles.setUrl(provider.refUrl); }
      else if (mapRefTiles) { map.removeLayer(mapRefTiles); mapRefTiles = null; }
      if (typeof window.MC_applyTileFilter === 'function') window.MC_applyTileFilter();
    }
    function fitPaths() {
      if (!map || !paths.length) return;
      var points = []; paths.forEach(function (path) { pathSegments(path).forEach(function (part) { part.forEach(function (p) { points.push(p); }); }); });
      if (points.length) map.fitBounds(L.latLngBounds(points),{padding:[24,24],maxZoom:12,animate:false});
    }
    function restoreViewport() {
      if (!map || ui.lat == null || ui.lon == null || ui.lat === '' || ui.lon === '') return;
      var lat=Number(ui.lat),lon=Number(ui.lon),zoom=Number(ui.zoom),center=map.getCenter();
      if (!Number.isFinite(lat) || !Number.isFinite(lon) || !Number.isFinite(zoom) || Math.abs(lat)>90 || Math.abs(lon)>180 || zoom<2 || zoom>19) return;
      if (Math.abs(center.lat-lat)>0.00002 || Math.abs(center.lng-lon)>0.00002 || map.getZoom()!==zoom) map.setView([lat,lon],zoom,{animate:false});
    }
    function refreshMapSize() {
      if (!map || !map.getContainer().offsetWidth || !map.getContainer().offsetHeight) return;
      updatingViewport=true;
      try {map.invalidateSize({pan:false});restoreViewport();}
      finally {updatingViewport=false;}
    }
    function renderMap() {
      if (!mapReady()) return;
      var colors = palette(), retained = new Set(), visibleMarkers = new Map();
      paths.forEach(function (path) {
        var segments = pathSegments(path), key = path.id, color = rows(path.directions).indexOf('ca_us') >= 0 ? colors[0] : colors[1];
        retained.add(key);
        if (segments.length) {
          var line = lines.get(key);
          if (!line) { line = L.polyline(segments,{color:color,weight:2,opacity:.55}).addTo(map); line.on('click',function () {change({path:key}); el('cb-flow-detail-title').focus();}); lines.set(key,line); }
          else line.setLatLngs(segments);
          line.setStyle({color:color,dashArray:rows(path.directions).length > 1 ? '2 4' : rows(path.directions)[0] === 'us_ca' ? '7 4' : null,weight:ui.path === key ? 5 : 2,opacity:ui.path && ui.path !== key ? .18 : .7});
        } else if (lines.has(key)) { map.removeLayer(lines.get(key)); lines.delete(key); }
        rows(path.hops).slice(0,MAX_HOPS).forEach(function (hop) {
          var p = point(hop); if (!p || !hop.publicKey || visibleMarkers.size >= 1000) return;
          var identity = String(hop.publicKey), existing = visibleMarkers.get(identity);
          if (!existing || ui.path === path.id) visibleMarkers.set(identity,{hop:hop,point:p,path:path.id,selected:ui.path === path.id});
        });
      });
      lines.forEach(function (line,key) {if (!retained.has(key)) {map.removeLayer(line);lines.delete(key);}});
      visibleMarkers.forEach(function (item,key) {
        var marker = markers.get(key), hop = item.hop;
        if (!marker) { marker = L.circleMarker(item.point,{radius:5,weight:2,fillOpacity:.9}).addTo(map); marker.on('click',function () {change({path:marker.options.crossBorderPath}); el('cb-flow-detail-title').focus();}); markers.set(key,marker); }
        marker.options.crossBorderPath = item.path; marker.setLatLng(item.point); marker.setStyle({color:hop.country === 'CA' ? colors[0] : hop.country === 'US' ? colors[1] : colors[3],fillColor:colors[6],fillOpacity:hop.kind === 'receiver' ? 0 : .9,radius:item.selected ? 7 : 4});
        var label = document.createElement('span'); label.textContent = String(hop.name || key.slice(0,12)) + ' · ' + (hop.country || 'country unknown') + (hop.kind === 'receiver' ? ' · receiver' : ''); marker.bindTooltip(label);
      });
      markers.forEach(function (marker,key) {if (!visibleMarkers.has(key)) {map.removeLayer(marker);markers.delete(key);}});
      if (!fitOnce && (paths.length || ui.lat)) { fitOnce=true;var lat = Number(ui.lat), lon = Number(ui.lon), zoom = Number(ui.zoom); if (ui.lat && ui.lon && Number.isFinite(lat) && Number.isFinite(lon) && Number.isFinite(zoom)) map.setView([lat,lon],zoom,{animate:false}); else fitPaths(); }
    }
    function scopeEvidence(hop) {
      var repeater = mapper.get(String(hop.publicKey || '').toLowerCase()); if (!repeater) return 'MeshMapper scope report unavailable.';
      var evidence = rows(repeater.scopes).slice(0,32).map(function (s) {
        var sources = []; if (s.reportedAt) sources.push('queried ' + stamp(s.reportedAt)); if (s.uploadedAt) sources.push('uploaded ' + stamp(s.uploadedAt));
        if (s.observedAt) sources.push('forwarding inferred ' + stamp(s.observedAt) + (s.proof === 'floor' ? ' · trusted 1-byte evidence' : s.proof === 'multibyte' ? ' · multibyte evidence' : ''));
        return String(s.name || s.scope || 'Unnamed') + ': ' + (sources.join('; ') || 'time/source unknown');
      });
      if (!evidence.length) evidence.push(repeater.scopesCheckedAt ? 'Scope query answered ' + stamp(repeater.scopesCheckedAt) + '; no named scopes returned.' : 'No completed scope report.');
      if (repeater.defaultScopeState) evidence.push('Own-advert default: ' + (repeater.defaultScopeState === 'named' ? repeater.defaultScope : repeater.defaultScopeState === 'none' ? 'none' : 'name unresolved') + ' · ' + stamp(repeater.defaultScopeAt));
      return evidence.join('\n');
    }
    function renderDetail() {
      var path = paths.find(function (p) {return p.id === ui.path;});
      text(el('cb-flow-detail-title'), path ? directionLabel(rows(path.directions).length === 1 ? path.directions[0] : 'both') + ' · ' + pathScopeLabel(path) : ui.path ? 'Path outside this snapshot' : 'Select a path');
      el('cb-flow-clear').hidden = !ui.path;
      var detail = !path ? ui.path ? 'This path is outside the current direction, time window, or returned path limit. Choose another path.' : 'Choose a line on the map or a path from the list to inspect its repeaters and scope.' :
        'Packet ' + path.hash + '\nFirst recorded: ' + stamp(path.firstRecordedAt) + ' · Path width: ' + amount(path.hopWidth) + ' bytes\nResolved path distance: ' + km(path.distanceKm) + ' · Distance into the receiving country: ' + km(path.furthestKm) +
        '\nAirtime: ' + (number(path.airtimeMs) != null ? Number(path.airtimeMs).toLocaleString(undefined,{maximumFractionDigits:1}) + ' ms estimated · ' + String(path.airtimeSource || 'model not specified') : 'Unavailable for this packet') +
        '\nReception signal: SNR ' + (path.snr == null ? 'unknown' : String(path.snr) + ' dB') + ' · RSSI ' + (path.rssi == null ? 'unknown' : String(path.rssi) + ' dBm');
      text(el('cb-flow-detail-summary'),detail);
      var hops = rows(path && path.hops).slice(0,MAX_HOPS), node = el('cb-flow-hop-list');
      while (node.children.length > hops.length) node.lastElementChild.remove();
      hops.forEach(function (hop,index) {
        var li = node.children[index]; if (!li) {li = document.createElement('li');li.appendChild(document.createElement('strong'));li.appendChild(document.createElement('p'));var disclosure=document.createElement('details');var summary=document.createElement('summary');summary.textContent='Repeater scope evidence';disclosure.appendChild(summary);disclosure.appendChild(document.createElement('p'));li.appendChild(disclosure);node.appendChild(li);}
        li.classList.toggle('cb-hop-unknown',!point(hop));text(li.children[0],String(hop.name || (hop.publicKey ? hop.publicKey.slice(0,12) : 'Unresolved hop')));
        text(li.children[1],point(hop) ? (hop.country || 'Country unknown') + ' · ' + (hop.kind === 'receiver' ? 'receiving observer' : 'repeater') + '\nPosition report: ' + stamp(hop.location.reportedAt) : hopReasonLabel(hop.reason) + ' · pathway stops here');
        li.children[2].hidden = !hop.publicKey || hop.kind === 'receiver';text(li.children[2].children[1],scopeEvidence(hop));
      });
    }
    function renderPathList() {
      var list = el('cb-flow-path-list'), existing = new Map(Array.from(list.children).map(function (node) {return [node.dataset.path,node];}));
      paths.forEach(function (path,index) {
        var node = existing.get(path.id); if (!node) {node=document.createElement('button');node.type='button';node.dataset.path=path.id;node.appendChild(document.createElement('strong'));node.appendChild(document.createElement('span'));}
        text(node.children[0],directionLabel(rows(path.directions).length === 1 ? path.directions[0] : 'both') + ' · ' + scopeName(path));
        text(node.children[1],stamp(path.firstRecordedAt) + ' · ' + rows(path.hops).length + ' path entries · ' + km(path.furthestKm) + ' reach');
        node.setAttribute('aria-pressed',String(ui.path===path.id));if(list.children[index]!==node)list.insertBefore(node,list.children[index]||null);existing.delete(path.id);
      });existing.forEach(function (node) {node.remove();});
      text(el('cb-flow-path-count'),paths.length ? 'Showing ' + paths.length + ' returned paths' + (flow.pathsTruncated ? ' · path details capped' : '') + '.' : 'No resolved crossing paths in this selection. This does not prove that no packets crossed.');
    }
    function renderSelection() {
      if (disposed) return; paths=selectedPaths(flow,ui.direction);
      root.querySelectorAll('[data-direction]').forEach(function (button) {button.setAttribute('aria-pressed',String(button.dataset.direction===normalizeDirection(ui.direction)));});
      renderPathList(); renderDetail();
      if (ui.tab == null || ui.tab === 'overview') renderMap();
    }
    function renderCatalog() {
      var entries=catalogRows(response,''), node=root.querySelector('#cb-catalog-list');if(!node)return;
      var existing=new Map(Array.from(node.children).map(function (item) {return [item.dataset.scope,item];}));
      entries.forEach(function (item,index) {
        var entry=existing.get(item.name);if(!entry){entry=document.createElement('article');entry.dataset.scope=item.name;var button=document.createElement('button');button.type='button';button.dataset.scopeChoice=item.filterValue;entry.appendChild(button);entry.appendChild(document.createElement('p'));entry.appendChild(document.createElement('div'));}
        text(entry.children[0],item.name + (item.label!==item.name ? ' · '+item.label : ''));text(entry.children[1],item.evidence+(item.regions ? '\nMeshMapper regions: '+item.regions : ''));
        var links=item.sources.filter(function (s) {return safeLink(s.url);});var target=entry.children[2];while(target.children.length>links.length)target.lastElementChild.remove();links.forEach(function(s,i){if(!target.children[i])target.appendChild(document.createElement('a'));target.children[i].href=safeLink(s.url);text(target.children[i],s.label||'Source');});
        if(node.children[index]!==entry)node.insertBefore(entry,node.children[index]||null);existing.delete(item.name);
      });existing.forEach(function (item) {item.remove();});
      var mapperStatus=response.meshMapper && response.meshMapper.status;
      text(root.querySelector('#cb-catalog-status'),entries.length+' known or discovered names.'+(response.scopeCatalogTruncated===true?' Catalog limited; not all names are listed.':'')+' '+(response.meshMapper ? 'MeshMapper: '+meshMapperStatusLabel(mapperStatus)+' · fetched '+stamp(response.meshMapper.fetchedAt) : 'MeshMapper metadata is not available in this snapshot.'));
    }
    function renderRepeaters() {
      var shown=crossingRepeaters(flow),body=el('cb-border-repeaters'),existing=new Map(Array.from(body.children).map(function(row){return [row.dataset.key,row];})),pathsByNode=new Map();
      selectedPaths(flow,'both').forEach(function(path){rows(path.hops).slice(0,MAX_HOPS).forEach(function(hop){if(hop.publicKey && !pathsByNode.has(hop.publicKey))pathsByNode.set(hop.publicKey,path.id);});});
      shown.forEach(function(item,index){
        var row=existing.get(item.publicKey);if(!row){row=document.createElement('tr');row.dataset.key=item.publicKey;for(var i=0;i<6;i++)row.appendChild(document.createElement('td'));var button=document.createElement('button');button.type='button';row.cells[0].appendChild(button);}
        var button=row.cells[0].firstElementChild,path=pathsByNode.get(item.publicKey);text(button,item.name||item.publicKey.slice(0,12));button.dataset.borderPath=path||'';button.disabled=!path;button.title=path?'Inspect an observed path':'Path details not included in this snapshot';
        text(row.cells[1],item.country||'Unknown');text(row.cells[2],rows(item.directions).map(directionLabel).join(' · ')||'Unknown');text(row.cells[3],amount(item.contents));text(row.cells[4],amount(item.crossingContents));
        text(row.cells[5],rows(item.scopes).slice(0,10).map(function(s){return String(s.label||s.key)+' ('+amount(s.contents)+')';}).join(', ')+(rows(item.scopes).length>10?' · more scopes omitted':''));
        if(body.children[index]!==row)body.insertBefore(row,body.children[index]||null);existing.delete(item.publicKey);
      });existing.forEach(function(row){row.remove();});
      text(el('cb-border-repeaters-count'),shown.length?'Showing '+shown.length+' of '+rows(flow.repeaters).length+' returned repeaters. Scope counts may overlap; a recorded scope does not prove every earlier hop carried it unchanged.':'No resolved crossing repeater evidence in this snapshot.');
    }
    function update(data, filters) {
      response=data || {};flow=response.dashboard && response.dashboard.flow || {};ui=filters || ui;
      mapper=new Map();rows(response.meshMapper && response.meshMapper.repeaters).slice(0,5000).forEach(function(r){if(typeof r.publicKey==='string' && /^[a-f0-9]{64}$/i.test(r.publicKey))mapper.set(r.publicKey.toLowerCase(),r);});
      DIRECTIONS.forEach(function(d){var summary=directionSummary(flow,d[0]);text(el('cb-flow-'+d[0]+'-count'),amount(summary.contents));text(el('cb-flow-'+d[0]+'-scoped'),percent(summary.scopedPercent));text(el('cb-flow-'+d[0]+'-unscoped'),percent(summary.unscopedPercent));text(el('cb-flow-'+d[0]+'-repeaters'),amount(summary.repeaters));text(el('cb-flow-'+d[0]+'-distance'),km(summary.furthestKm));});
      var scopeLimited=scopeLimitMessages(response).length>0;
      text(el('cb-flow-status'),flow.identityComplete===false ? 'Repeater identity coverage is incomplete. Crossing counts and paths cannot be attributed.' : !usable(flow) ? 'Crossing analysis is unavailable. Listener activity remains available in the other views.' : (flow.status==='partial' || scopeLimited ? 'Partial path evidence. ' : '')+amount(flow.eligibleContents)+' eligible packet contents · '+amount(flow.nonCrossingContents)+' with no crossing in resolved paths · '+amount(flow.unresolvedContents)+' with crossing unresolved.'+(scopeLimited?' Scope-name evidence is incomplete.':''));
      var next=new Set(rows(flow.paths).slice(0,MAX_PATHS).map(function(p){return p.id;})),fresh=0;if(hasSnapshot)next.forEach(function(id){if(!knownPaths.has(id))fresh++;});knownPaths=next;hasSnapshot=true;
      text(el('cb-flow-map-status'),(ui.live==='0' ? 'Paused snapshot' : 'Updates every minute')+' · '+stamp(response.generatedAt)+(flow.identityComplete===false?' · Repeater identity coverage incomplete':fresh ? ' · '+fresh+' newly seen paths' : ''));
      renderCharts();renderSelection();renderCatalog();renderRepeaters();
    }
    function click(event) {
      var button=event.target.closest('button');if(!button || !root.contains(button))return;
      if(button.dataset.direction){change({direction:button.dataset.direction,path:''});return;}
      if(button.dataset.path){change({path:button.dataset.path});el('cb-flow-detail-title').focus();return;}
      if(button.id==='cb-flow-clear'){change({path:''});return;}
      if(button.id==='cb-flow-fit'){fitPaths();return;}
      if(button.dataset.borderPath){change({tab:'overview',direction:'both',path:button.dataset.borderPath});el('cb-flow-detail-title').focus();return;}
      if(button.dataset.scopeChoice)callbacks.onScope(button.dataset.scopeChoice);
    }
    function repaint(){if(disposed)return;syncTiles();renderCharts();renderSelection();}
    root.addEventListener('click',click);window.addEventListener('theme-changed',repaint);
    return {update:update,setView:function(filters){ui=filters;if(ui.tab==null||ui.tab==='overview')refreshMapSize();renderSelection();},resize:function(){if(viewFrame!=null)cancelAnimationFrame(viewFrame);viewFrame=requestAnimationFrame(function(){viewFrame=null;if(disposed)return;if(ui.tab==null||ui.tab==='overview'){refreshMapSize();renderMap();charts.forEach(function(c){c.resize();});}});},destroy:function(){disposed=true;if(viewFrame!=null)cancelAnimationFrame(viewFrame);root.removeEventListener('click',click);window.removeEventListener('theme-changed',repaint);charts.forEach(function(c){c.destroy();});charts.clear();if(map)map.remove();map=null;lines.clear();markers.clear();mapper.clear();knownPaths.clear();}};
  };
  window.CrossBorderFlow=api;
})();
