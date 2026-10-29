package main

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	crossBorderDashboardRows          = 50000
	crossBorderDashboardKeys          = 50000
	crossBorderDashboardMetadata      = 500
	crossBorderDashboardMetrics       = 50000
	crossBorderDashboardSampleSeconds = 300
)

func crossBorderDashboardLimit(cfg CrossBorderConfig) int {
	limit := cfg.ObservationLimit
	if limit <= 0 {
		limit = 5000
	}
	if limit >= crossBorderDashboardRows/10 {
		return crossBorderDashboardRows
	}
	return limit * 10
}

func (svc *crossBorderService) loadDashboard(ctx context.Context, f CrossBorderFilters) (CrossBorderResponse, error) {
	now := time.Now().UTC()
	peerDB, err := svc.peerReader(ctx)
	if err != nil {
		return CrossBorderResponse{}, err
	}
	cfg := svc.cfg
	cfg.ObservationLimit = crossBorderDashboardLimit(cfg)
	if cfg.MetadataLimit <= 0 || cfg.MetadataLimit > crossBorderDashboardMetadata {
		cfg.MetadataLimit = crossBorderDashboardMetadata
	}
	peer, peerIDs, peerIDsComplete, err := peerDB.readCrossBorderDashboard(ctx, f.PeerIATA, f, cfg, svc.serverCfg, now)
	if err != nil {
		return CrossBorderResponse{}, err
	}
	local, localIDs, localIDsComplete, localErr := svc.local.readCrossBorderDashboard(ctx, f.LocalIATA, f, cfg, svc.serverCfg, now)
	if ctx.Err() != nil {
		return CrossBorderResponse{}, ctx.Err()
	}
	if localErr != nil {
		local = crossBorderSnapshot{}
	}
	local.SharedIdentityChecked = true
	local.SharedIdentityComplete = local.Available && peerIDsComplete && localIDsComplete
	visibleIDs := crossBorderSnapshotIDs(local)
	for id := range crossBorderSnapshotIDs(peer) {
		visibleIDs[id] = true
	}
	for _, s := range []crossBorderSnapshot{local, peer} {
		for _, metric := range s.DashboardMetrics {
			visibleIDs[crossBorderID(metric.Observer.ID)] = true
		}
	}
	for id := range peerIDs {
		if localIDs[id] && visibleIDs[id] {
			local.SharedObserverIDs = append(local.SharedObserverIDs, id)
		}
	}
	// Keep the established peer packet drill-down in this bulk response, while
	// the dashboard below aggregates independent populations on both sides.
	response, err := buildCrossBorder(ctx, cfg, f, local, peer, now)
	if err != nil {
		return CrossBorderResponse{}, err
	}
	response.Dashboard, err = buildCrossBorderDashboard(ctx, svc.cfg, f, local, peer, now)
	if err != nil {
		return CrossBorderResponse{}, err
	}
	// Cached detail slices must not retain the omitted50k-record backing
	// arrays. Scope-name lists have an independent display budget too.
	response.Packets = append([]CrossBorderPacket{}, response.Packets...)
	// The dashboard used the complete collision index. Do not expose the
	// legacy metadata-only resolver's stronger claim beside that evidence.
	response.PeerRepeaters = make([]CrossBorderRepeater, len(response.Dashboard.Peer.Repeaters))
	for i, repeater := range response.Dashboard.Peer.Repeaters {
		response.PeerRepeaters[i] = repeater.CrossBorderRepeater
	}
	response.Completeness.PeerRepeatersTruncated = response.Dashboard.Peer.RepeatersTruncated
	response.Sources.Local.NodesTruncated = response.Sources.Local.NodesTruncated || !response.Dashboard.Local.NodeIndexComplete || response.Dashboard.Local.RepeatersTruncated
	response.Sources.Peer.NodesTruncated = response.Sources.Peer.NodesTruncated || !response.Dashboard.Peer.NodeIndexComplete || response.Dashboard.Peer.RepeatersTruncated
	for i := range response.PeerObservers {
		if len(response.PeerObservers[i].ObservedScopes) > 32 {
			response.PeerObservers[i].ObservedScopes = append([]CrossBorderScope{}, response.PeerObservers[i].ObservedScopes[:32]...)
			response.Dashboard.Peer.ScopesTruncated = true
		}
	}
	for i := range response.PeerRepeaters {
		if len(response.PeerRepeaters[i].ObservedScopes) > 32 {
			response.PeerRepeaters[i].ObservedScopes = append([]CrossBorderScope{}, response.PeerRepeaters[i].ObservedScopes[:32]...)
			response.Dashboard.Peer.ScopesTruncated = true
		}
	}
	for i := range response.Findings {
		if response.Findings[i].Code == "sample_availability" {
			response.Findings[i].Message = "Dashboard populations are sampled independently by source. Packet details remain peer-primary. Stored records are first-recorded observer/path evidence, not a raw RF arrival counter; their headers may later be refreshed."
		}
	}
	response.Findings = append(response.Findings, CrossBorderFinding{Code: "radio_measurement_limits", Severity: "info", Message: "Radio counters cover all reported activity, independently of the packet scope filter. TX is reported sending duration; RX estimates successfully parsed airtime. Neither identifies which country caused congestion."})
	if !response.Dashboard.Local.NodeIndexComplete || !response.Dashboard.Peer.NodeIndexComplete || response.Dashboard.RegionPairsTruncated || response.Dashboard.Local.RepeatersTruncated || response.Dashboard.Peer.RepeatersTruncated || response.Dashboard.Local.Radio.Truncated || response.Dashboard.Peer.Radio.Truncated || response.Dashboard.Local.ScopesTruncated || response.Dashboard.Peer.ScopesTruncated {
		response.Completeness.Complete = false
		response.Status = "partial"
	}
	// The dashboard scope selector applies to both independent populations.
	scopes := map[string]bool{}
	for _, row := range local.Observations {
		wire, valid := crossBorderReadWire(row)
		if valid && wire.scope.Name != nil {
			scopes[*wire.scope.Name] = true
		}
	}
	for _, source := range []CrossBorderDashboardSource{response.Dashboard.Local, response.Dashboard.Peer} {
		for _, item := range source.Scopes {
			if strings.HasPrefix(item.Key, "named:") {
				scopes[strings.TrimPrefix(item.Key, "named:")] = true
			}
		}
	}
	for _, option := range response.Options.Scopes {
		if option.Value != "all" && option.Value != "unscoped" && option.Value != "unknown" {
			scopes[option.Value] = true
		}
	}
	response.Options.Scopes = append([]CrossBorderScopeOption{}, response.Options.Scopes[:3]...)
	names := crossBorderIdentityKeys(scopes)
	if len(names) > 128 {
		names = names[:128]
	}
	for _, name := range names {
		response.Options.Scopes = append(response.Options.Scopes, CrossBorderScopeOption{Value: name, Label: name})
	}
	return response, nil
}

func (db *DB) readCrossBorderDashboard(ctx context.Context, regions []string, f CrossBorderFilters, cfg CrossBorderConfig, serverCfg *Config, now time.Time) (crossBorderSnapshot, map[string]bool, bool, error) {
	if db == nil || db.conn == nil {
		return crossBorderSnapshot{}, nil, false, errCrossBorderUnavailable
	}
	since := now.Add(-time.Duration(f.WindowMinutes) * time.Minute)
	snapshot, err := db.readCrossBorderDataSelected(ctx, regions, since, now, cfg, nil, false, true)
	if err != nil {
		return snapshot, nil, false, err
	}
	// The scan is ordered newest first. Preserve its boundary before privacy
	// filtering can remove every retained row; ties at this second are partial.
	for _, row := range snapshot.Observations {
		if snapshot.DashboardScanCutoff == nil || row.Timestamp < *snapshot.DashboardScanCutoff {
			stamp := row.Timestamp
			snapshot.DashboardScanCutoff = &stamp
		}
	}
	inactive, err := db.crossBorderDashboardHasInactive(ctx)
	if err != nil {
		return snapshot, nil, false, err
	}
	hidden, err := db.crossBorderDashboardHidden(ctx, serverCfg, inactive)
	if err != nil {
		return snapshot, nil, false, err
	}
	isHidden := func(id string) bool {
		return hidden[crossBorderID(id)] || (serverCfg != nil && serverCfg.IsBlacklisted(id))
	}
	observers := snapshot.Observers[:0]
	for _, observer := range snapshot.Observers {
		if !isHidden(observer.ID) {
			observers = append(observers, observer)
		}
	}
	snapshot.Observers = observers
	observations := snapshot.Observations[:0]
	for _, row := range snapshot.Observations {
		if !isHidden(row.ObserverID) && !isHidden(row.FromPubkey) {
			observations = append(observations, row)
		}
	}
	snapshot.Observations = observations
	identity := db.crossBorderIdentitySQL("public_key")
	candidates := `SELECT ` + identity + ` AS public_key,COALESCE(role,'') AS role,0 AS source_rank FROM nodes`
	if inactive {
		candidates += ` UNION ALL SELECT ` + identity + `,COALESCE(role,''),1 FROM inactive_nodes`
	}
	// Deduplicate before LIMIT, with the current role taking precedence over
	// retained metadata for the same full identity. All roles remain candidates.
	query := `SELECT public_key,COALESCE(MAX(CASE WHEN source_rank=0 THEN role END),MAX(role),'') FROM (` + candidates + `) AS candidates GROUP BY public_key ORDER BY public_key LIMIT ` + fmt.Sprint(crossBorderDashboardKeys+1)
	rows, err := db.conn.QueryContext(ctx, query)
	if err != nil {
		return snapshot, nil, false, err
	}
	snapshot.DashboardNodeIndexComplete = true
	for rows.Next() {
		if len(snapshot.DashboardNodes) == crossBorderDashboardKeys {
			snapshot.DashboardNodeIndexComplete = false
			break
		}
		var node crossBorderNodeIdentity
		if err = rows.Scan(&node.PublicKey, &node.Role); err != nil {
			rows.Close()
			return snapshot, nil, false, err
		}
		node.PublicKey = crossBorderID(node.PublicKey)
		node.Hidden = isHidden(node.PublicKey)
		snapshot.DashboardNodes = append(snapshot.DashboardNodes, node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return snapshot, nil, false, err
	}
	// Candidate names/configuration are one bounded bulk lookup. All known
	// identity keys above still participate in collision detection.
	metadataCfg := cfg
	if metadataCfg.MetadataLimit <= 0 || metadataCfg.MetadataLimit > crossBorderDashboardMetadata {
		metadataCfg.MetadataLimit = crossBorderDashboardMetadata
	}
	keys, err := crossBorderDashboardCandidateKeys(ctx, snapshot, metadataCfg.MetadataLimit)
	if err != nil {
		return snapshot, nil, false, err
	}
	if err = db.readCrossBorderNodesForKeys(ctx, metadataCfg, &snapshot, keys); err != nil {
		return snapshot, nil, false, err
	}
	for i := range snapshot.Nodes {
		snapshot.Nodes[i].Hidden = isHidden(snapshot.Nodes[i].PublicKey)
	}
	if err = db.readCrossBorderDashboardMetrics(ctx, regions, since, now, hidden, serverCfg, &snapshot); err != nil {
		return snapshot, nil, false, err
	}
	ids := map[string]bool{}
	rows, err = db.conn.QueryContext(ctx, `SELECT id,UPPER(TRIM(COALESCE(iata,''))) FROM observers ORDER BY id LIMIT `+fmt.Sprint(crossBorderDashboardKeys+1))
	if err != nil {
		return snapshot, ids, false, nil
	}
	complete := true
	read := 0
	regionCounts := map[string]int{}
	for rows.Next() {
		if read == crossBorderDashboardKeys {
			complete = false
			break
		}
		read++
		var id, region string
		if err = rows.Scan(&id, &region); err != nil {
			rows.Close()
			return snapshot, ids, false, nil
		}
		key := crossBorderID(id)
		if !ids[key] && !isHidden(id) && validCrossBorderIATA(region) {
			regionCounts[region]++
		}
		ids[key] = true
	}
	if rows.Err() != nil {
		complete = false
	}
	rows.Close()
	regionKeys := make([]string, 0, len(regionCounts))
	for key := range regionCounts {
		regionKeys = append(regionKeys, key)
	}
	sort.Strings(regionKeys)
	snapshot.Regions = []CrossBorderRegion{}
	for _, key := range regionKeys {
		if len(snapshot.Regions) >= metadataCfg.MetadataLimit {
			snapshot.ObserversTruncated = true
			break
		}
		snapshot.Regions = append(snapshot.Regions, CrossBorderRegion{IATA: key, Label: key, Observers: regionCounts[key]})
	}
	if !complete {
		snapshot.ObserversTruncated = true
	}
	return snapshot, ids, complete, nil
}

// Catalog existence is not SELECT permission. A known retained set must be
// queried successfully, rather than silently disappearing behind catalog views
// that hide columns from restricted readers. Old databases may lack the set.
func (db *DB) crossBorderDashboardHasInactive(ctx context.Context) (bool, error) {
	var exists bool
	err := db.conn.QueryRowContext(ctx, db.nativeSQL(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE lower(name)='inactive_nodes' AND type IN ('table','view'))`, `SELECT to_regclass('inactive_nodes') IS NOT NULL`)).Scan(&exists)
	return exists, err
}

// A bounded full hidden-key set avoids display-cap leaks and SQL parameter
// limits. It includes active, retained inactive and observer names.
func (db *DB) crossBorderDashboardHidden(ctx context.Context, cfg *Config, inactive bool) (map[string]bool, error) {
	hidden := map[string]bool{}
	if cfg == nil {
		return hidden, nil
	}
	cfg.IsNameHidden("")
	prefixes := cfg.hiddenPrefixesPtr.Load()
	if prefixes == nil || len(*prefixes) == 0 {
		return hidden, nil
	}
	args := []any{}
	conditions := []string{}
	for _, prefix := range *prefixes {
		if prefix == "" {
			continue
		}
		args = append(args, prefix)
		p := db.parameter(len(args))
		conditions = append(conditions, "substr(COALESCE(name,''),1,length("+p+"))="+p)
	}
	if len(conditions) == 0 {
		return hidden, nil
	}
	where := strings.Join(conditions, " OR ")
	query := `SELECT ` + db.crossBorderIdentitySQL("public_key") + ` AS identity FROM nodes WHERE ` + where + ` UNION SELECT ` + db.crossBorderIdentitySQL("id") + ` FROM observers WHERE ` + where
	if inactive {
		query += ` UNION SELECT ` + db.crossBorderIdentitySQL("public_key") + ` FROM inactive_nodes WHERE ` + where
	}
	query += ` LIMIT ` + fmt.Sprint(crossBorderDashboardKeys+1)
	rows, err := db.conn.QueryContext(ctx, query, db.planWithValues(args)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(hidden) == crossBorderDashboardKeys {
			return nil, errCrossBorderUnavailable
		}
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		hidden[crossBorderID(id)] = true
	}
	return hidden, rows.Err()
}

func crossBorderDashboardCandidateKeys(ctx context.Context, snapshot crossBorderSnapshot, limit int) ([]string, error) {
	seen := map[string]int64{}
	prefixes := map[string]int64{}
	knownPrefixes := map[string]bool{}
	for _, node := range snapshot.DashboardNodes {
		key := crossBorderID(node.PublicKey)
		if crossBorderPublicKey(key) {
			for _, size := range []int{2, 4, 6} {
				knownPrefixes[key[:size]] = true
			}
		}
	}
	for i, row := range snapshot.Observations {
		if i&127 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		wire, valid := crossBorderReadWire(row)
		if !valid || strings.EqualFold(strings.TrimSpace(row.Direction), "tx") {
			continue
		}
		if wire.payload == PayloadADVERT && crossBorderPublicKey(row.FromPubkey) {
			if row.Timestamp > seen[crossBorderID(row.FromPubkey)] {
				seen[crossBorderID(row.FromPubkey)] = row.Timestamp
			}
		}
		if wire.route != RouteFlood && wire.route != RouteTransportFlood {
			continue
		}
		for _, hop := range crossBorderDashboardHops(row, wire) {
			if knownPrefixes[hop] && row.Timestamp > prefixes[hop] {
				prefixes[hop] = row.Timestamp
			}
		}
	}
	for _, observer := range snapshot.Observers {
		if crossBorderPublicKey(observer.ID) {
			if _, ok := seen[crossBorderID(observer.ID)]; !ok {
				seen[crossBorderID(observer.ID)] = 0
			}
		}
	}
	visible := map[string]bool{}
	for _, node := range snapshot.DashboardNodes {
		key := node.PublicKey
		if node.Hidden || !crossBorderPublicKey(key) || (node.Role != "repeater" && node.Role != "room") {
			continue
		}
		for _, length := range []int{2, 4, 6} {
			if at, ok := prefixes[key[:length]]; ok {
				if at > seen[key] {
					seen[key] = at
				}
			}
		}
		if _, ok := seen[key]; ok {
			visible[key] = true
		}
	}
	keys := crossBorderIdentityKeys(visible)
	sort.Slice(keys, func(i, j int) bool {
		if seen[keys[i]] != seen[keys[j]] {
			return seen[keys[i]] > seen[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, ctx.Err()
}

func crossBorderMetricsCutoff(now time.Time) time.Time {
	// Ingestion rounds to the nearest interval and can update that bucket
	// until half an interval after its nominal timestamp.
	step := time.Duration(crossBorderDashboardSampleSeconds) * time.Second
	return now.Add(-step / 2).Truncate(step)
}

func (db *DB) readCrossBorderDashboardMetrics(ctx context.Context, regions []string, since, now time.Time, hidden map[string]bool, cfg *Config, snapshot *crossBorderSnapshot) error {
	cols, err := schemaColumns(ctx, db.conn, "observer_metrics", db.Backend())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	if !cols["timestamp"] || !cols["observer_id"] || (!cols["tx_air_secs"] && !cols["rx_air_secs"]) {
		return nil
	}
	column := func(name string) string {
		if cols[name] {
			return "m." + name
		}
		return "NULL"
	}
	args := []any{since.UTC().Format(time.RFC3339), crossBorderMetricsCutoff(now).UTC().Format(time.RFC3339)}
	where := db.crossBorderObserverWhere(regions, snapshot.DashboardObserverSelection, "ob.rowid", &args)
	selectedIATA := crossBorderSelectionIATAs(snapshot.DashboardObserverSelection)
	observerCols, err := schemaColumns(ctx, db.conn, "observers", db.Backend())
	if err != nil {
		return err
	}
	relay := "NULL"
	if observerCols["can_relay"] && observerCols["can_relay_seen"] {
		relay = "CASE WHEN ob.can_relay_seen=1 THEN ob.can_relay ELSE NULL END"
	}
	query := `SELECT ob.id,substr(COALESCE(ob.name,''),1,256),UPPER(TRIM(COALESCE(ob.iata,''))),` + relay + `,m.timestamp,` + column("tx_air_secs") + `,` + column("rx_air_secs") + `,` + column("recv_errors") + `,` + column("packets_sent") + `,` + column("packets_recv") + ` FROM observer_metrics m JOIN observers ob ON ob.id=m.observer_id WHERE m.timestamp >= ` + db.parameter(1) + ` AND m.timestamp <= ` + db.parameter(2) + ` AND ` + where + ` ORDER BY m.timestamp DESC,ob.id LIMIT ` + fmt.Sprint(crossBorderDashboardMetrics+1)
	rows, err := db.conn.QueryContext(ctx, query, db.crossBorderSelectionArgs(args, snapshot.DashboardObserverSelection)...)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	defer rows.Close()
	read := 0
	for rows.Next() {
		if read == crossBorderDashboardMetrics {
			snapshot.DashboardMetricsTruncated = true
			break
		}
		read++
		var row crossBorderMetricRow
		var relayValue, tx, rx, recv, sent, packets sql.NullInt64
		if err = rows.Scan(&row.Observer.ID, &row.Observer.Name, &row.Observer.IATA, &relayValue, &row.Sample.Timestamp, &tx, &rx, &recv, &sent, &packets); err != nil {
			return err
		}
		var selected bool
		row.Observer.IATA, selected = crossBorderSelectedIATA(selectedIATA, row.Observer.ID, row.Observer.IATA)
		if !selected {
			return errCrossBorderUnavailable
		}
		if hidden[crossBorderID(row.Observer.ID)] || (cfg != nil && cfg.IsBlacklisted(row.Observer.ID)) {
			continue
		}
		if relayValue.Valid && (relayValue.Int64 == 0 || relayValue.Int64 == 1) {
			v := relayValue.Int64 == 1
			row.Observer.ReportedCanRelay = &v
		}
		integer := func(value sql.NullInt64) *int {
			if !value.Valid {
				return nil
			}
			v := int(value.Int64)
			return &v
		}
		row.Sample.TxAirSecs, row.Sample.RxAirSecs = integer(tx), integer(rx)
		row.Sample.RecvErrors, row.Sample.PacketsSent, row.Sample.PacketsRecv = integer(recv), integer(sent), integer(packets)
		snapshot.DashboardMetrics = append(snapshot.DashboardMetrics, row)
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	snapshot.DashboardMetricsAvailable = true
	return nil
}
