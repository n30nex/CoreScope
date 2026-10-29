/* Peer telemetry stays on this page; local telemetry is reference evidence. */
'use strict';
(function () {
  var WINDOWS = [15, 60, 180, 360];
  var PACKET_LIMIT = 100;
  var METADATA_LIMIT = 200;
  var OBSERVATION_LIMIT = 8;
  var active = null;

  function normalizeFilters(input) {
    input = input || {};
    function iata(value) {
      var items = String(Array.isArray(value) ? value.join(',') : (value || '')).split(',');
      return Array.from(new Set(items.map(function (s) { return s.trim().toUpperCase(); }).filter(Boolean))).join(',');
    }
    var minutes = Number(input.windowMinutes);
    return {
      localIata: iata(input.localIata), peerIata: iata(input.peerIata),
      scope: String(input.scope || 'all'), windowMinutes: WINDOWS.indexOf(minutes) >= 0 ? minutes : '',
      packet: String(input.packet || ''), quality: input.quality === '1' ? '1' : ''
    };
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
    module.exports = { normalizeFilters, numberLabel, scopeLabel, ageAt, uniqueRows, qualityNotice };
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
    return '<div class="cb-page">' +
      '<header class="cb-header"><div><a href="#/tools">Tools</a><h1>Border links</h1>' +
      '<p id="cb-intro">Compare packets from the peer feed with Canadian observations.</p></div>' +
      '<button type="button" id="cb-refresh">Refresh</button></header>' +
      '<p id="cb-status" role="status" aria-live="polite">Loading peer telemetry…</p>' +
      '<form id="cb-filters" class="cb-filters" aria-label="Border link filters">' +
      '<label>Peer region / IATA<input id="cb-peer" name="peerIata" list="cb-peer-options" placeholder="Operator default" maxlength="160" autocomplete="off"></label>' +
      '<datalist id="cb-peer-options"></datalist>' +
      '<label>Canadian reference region / IATA<input id="cb-local" name="localIata" list="cb-local-options" placeholder="Operator default" maxlength="160" autocomplete="off"></label>' +
      '<datalist id="cb-local-options"></datalist>' +
      '<label>Peer scope<select id="cb-scope" name="scope"><option value="all">All scopes</option><option value="unscoped">Unscoped</option><option value="unknown">Unknown scope</option></select></label>' +
      '<label>Window<select id="cb-window" name="windowMinutes"><option value="">Operator default</option></select></label><button type="submit">Apply filters</button>' +
      '<p class="cb-filter-help">Use three-letter IATA codes, separated by commas. Blank uses the operator default. Scope selects peer traffic; the window applies to both sources. Canadian matches may have a different or unknown scope.</p></form>' +
      '<div class="cb-sources"><section class="cb-source" aria-labelledby="cb-peer-title"><h2 id="cb-peer-title">Peer feed · primary</h2><p id="cb-peer-source"></p></section>' +
      '<section class="cb-source cb-reference" aria-labelledby="cb-local-title"><h2 id="cb-local-title">Canadian feed · reference</h2><p id="cb-local-source"></p></section></div>' +
      '<p class="cb-caveat">Matching content in both feeds does not prove a cross-border radio path.</p>' +
      '<div id="cb-results" hidden><p id="cb-quality-warning" class="cb-warning" role="status" hidden></p><dl class="cb-summary">' +
      '<div><dt>Peer packets</dt><dd id="cb-peer-count">Unknown</dd></div>' +
      '<div><dt>Matching Canadian packets</dt><dd id="cb-independent-count">Unknown</dd><small>Distinct observers · among peer packets</small></div>' +
      '<div><dt>Shared-identity matches</dt><dd id="cb-shared-count">Unknown</dd><small>Same observer or shared-feed identity · among peer packets</small></div>' +
      '<div class="cb-reference"><dt>All Canadian matches</dt><dd id="cb-local-count">Unknown</dd><small>Among peer packets; includes shared-identity matches</small></div></dl>' +
      '<details id="cb-quality" class="cb-quality"><summary>Data quality and limits</summary><div class="cb-quality-body">' +
      '<p class="cb-note">Missing observations are not packet loss or evidence of misconfiguration. Matches use packet content; observers present in both feeds do not count as distinct observers.</p>' +
      '<p class="cb-note">Retained evidence dates do not prove continuous collection. Declared scopes are the latest reports; observed forwarding uses unambiguous flood last-hop evidence. Radio hardware and relay reports do not establish per-packet settings.</p>' +
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
      '<p class="cb-note">Latest hardware and relay reports; per-device RF settings are unknown.</p>' +
      '<p id="cb-observer-count" class="cb-note"></p>' +
      table('cb-observers', 'Peer observer provenance and latest reported settings', ['Observer', 'Reported radio hardware', 'Latest relay report', 'Sample evidence', 'Last seen / location']) +
      '<p id="cb-observers-empty" hidden>No peer observer metadata available for this selection.</p></section></div></div>';
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
    state.el('cb-quality').open = state.filters.quality === '1';
  }
  function sourceText(source, snapshot) {
    source = source || {};
    var status = ({ available: 'Available', stale: 'Stale retained data', empty: 'No observations', unavailable: 'Unavailable',
      after_snapshot: 'Only after-snapshot observations', disabled: 'Disabled', selection_required: 'Select a region' })[source.status] || 'Status unknown';
    return known(source.label) + ' · ' + status + '\nRegions: ' + (array(source.selectionIata).join(', ') || 'Not selected') +
      '\nLatest observation: ' + known(source.latestObservationAt) + ' · ' + ageAt(source.latestObservationAt, snapshot) + ' at snapshot';
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
    text(el('cb-status'), (statuses[data.status] || 'Comparison status unknown') + ' · Updated: ' + known(data.generatedAt));
    text(el('cb-intro'), 'Compare packets reported by ' + (sources.peer && sources.peer.label || 'the peer feed') + ' with Canadian observations.');
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
    options(el('cb-scope'), opts.scopes, 'value', 'label', state.filters.scope);
    var windows = array(opts.windowMinutes).map(Number).filter(function (minutes) { return WINDOWS.indexOf(minutes) >= 0; }).map(function (minutes) {
      return { value: String(minutes), label: windowLabel(minutes) };
    });
    options(el('cb-window'), windows, 'value', 'label');
    el('cb-window').value = String(state.filters.windowMinutes);

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
  }

  async function load(state) {
    if (state.request) state.request.abort();
    var request = new AbortController(); state.request = request;
    var generation = ++state.generation;
    text(state.el('cb-status'), 'Loading peer telemetry…');
    state.el('cb-results').setAttribute('aria-busy', 'true');
    var params = new URLSearchParams();
    ['localIata', 'peerIata', 'scope', 'windowMinutes'].forEach(function (key) { if (state.filters[key] !== '') params.set(key, state.filters[key]); });
    try {
      // A page-owned request can be cancelled without disturbing the shared API cache.
      var response = await fetch('/api/cross-border?' + params.toString(), { signal: request.signal });
      if (!response.ok) { var error = new Error('HTTP ' + response.status); error.status = response.status; throw error; }
      var data = await response.json();
      if (state !== active || generation !== state.generation) return;
      if (!data || typeof data.enabled !== 'boolean') throw new Error('Invalid response');
      if (data.filters) {
        state.filters = normalizeFilters(Object.assign({}, data.filters, { packet: state.filters.packet, quality: state.filters.quality }));
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
      if (state === active && generation === state.generation) state.el('cb-results').removeAttribute('aria-busy');
    }
  }

  function init(container) {
    var state = { root: container, filters: normalizeFilters(URLState.parseHash(location.hash).params), request: null, generation: 0, packetRows: [], data: {} };
    state.el = function (id) { return container.querySelector('#' + id); };
    active = state;
    container.innerHTML = pageHtml();
    controls(state);
    state.submit = function (event) {
      event.preventDefault();
      var filters = normalizeFilters({ localIata: state.el('cb-local').value, peerIata: state.el('cb-peer').value,
        scope: state.el('cb-scope').value, windowMinutes: state.el('cb-window').value, quality: state.filters.quality });
      if ([filters.localIata, filters.peerIata].some(function (value) { return value && !/^[A-Z]{3}(,[A-Z]{3})*$/.test(value); })) {
        text(state.el('cb-status'), 'Enter three-letter IATA codes separated by commas, or leave blank for the operator default.');
        return;
      }
      state.filters = filters; setHash(state, false); controls(state); load(state);
    };
    state.click = function (event) {
      var button = event.target.closest('button');
      if (!button || !container.contains(button)) return;
      if (button.id === 'cb-refresh') { load(state); return; }
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
    state.toggleQuality = function () {
      if (state !== active) return;
      var quality = state.el('cb-quality').open ? '1' : '';
      if (quality === state.filters.quality) return;
      state.filters.quality = quality;
      setHash(state, false);
    };
    state.el('cb-quality').addEventListener('toggle', state.toggleQuality);
    container.addEventListener('click', state.click);
    load(state);
  }

  function destroy() {
    var state = active;
    if (!state) return;
    active = null;
    state.generation++;
    if (state.request) state.request.abort();
    state.el('cb-filters').removeEventListener('submit', state.submit);
    state.el('cb-quality').removeEventListener('toggle', state.toggleQuality);
    state.root.removeEventListener('click', state.click);
    state.data = {}; state.packetRows = [];
  }
  registerPage('cross-border', { init: init, destroy: destroy });
})();
