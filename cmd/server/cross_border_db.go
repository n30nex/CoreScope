package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
)

// All report SQL is SELECT-only. A peer never enters the main DB field, the
// packet store, its startup loader, poller, analytics, or WebSocket broadcasts.
func (svc *crossBorderService) peerReader(ctx context.Context) (*DB, error) {
	for {
		svc.peerMu.Lock()
		if svc.peer != nil {
			db := svc.peer
			svc.peerMu.Unlock()
			// A warm connection is not proof the peer is still import-ready.
			// Runtime peers are PostgreSQL; SQLite is used only by the native
			// SQL-fixture matrix. Keep a failed warm pool for readiness recovery.
			if db.Backend() == dbconfig.Postgres {
				if err := dbschema.AssertReadyContext(ctx, db.conn); err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					return nil, errCrossBorderUnavailable
				}
			}
			return db, nil
		}
		if waiting := svc.peerOpening; waiting != nil {
			svc.peerMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-waiting:
				continue
			}
		}
		if svc.peerError != nil && time.Since(svc.peerAttempt) < time.Duration(svc.cfg.CacheSeconds)*time.Second {
			svc.peerMu.Unlock()
			return nil, errCrossBorderUnavailable
		}
		svc.peerOpening = make(chan struct{})
		waiting := svc.peerOpening
		svc.peerMu.Unlock()
		db, err := openCrossBorderPeer(ctx, svc.local, svc.peerURL, svc.cfg)
		svc.mu.Lock()
		closed := svc.closed
		svc.peerMu.Lock()
		if closed {
			svc.peer, svc.peerError = nil, errCrossBorderUnavailable
		} else {
			svc.peer, svc.peerError = db, err
		}
		svc.peerAttempt = time.Now()
		svc.peerOpening = nil
		close(waiting)
		svc.peerMu.Unlock()
		svc.mu.Unlock()
		if closed {
			if db != nil {
				db.conn.Close()
			}
			return nil, errCrossBorderUnavailable
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errCrossBorderUnavailable
		}
		return db, nil
	}
}

// Like pgutil.AssertReadOnly, verify effective grants rather than trusting the
// session default alone. This variant is context-bound to the report deadline.
const crossBorderReaderPrivilegesSQL = `SELECT
 EXISTS (SELECT 1 FROM pg_roles WHERE rolname=current_user AND (rolsuper OR rolcreaterole OR rolcreatedb))
 OR has_database_privilege(current_database(), 'CREATE')
 OR EXISTS (SELECT 1 FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname <> 'information_schema' AND has_schema_privilege(oid, 'CREATE'))
 OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' AND c.relkind IN ('r','p','v','m','f')
 AND has_table_privilege(c.oid, 'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER'))`

func openCrossBorderPeer(ctx context.Context, local *DB, dsn string, cfg CrossBorderConfig) (*DB, error) {
	c, err := pgutil.ParseConfig(dsn)
	if err != nil {
		return nil, errCrossBorderUnavailable
	}
	c.ConnectTimeout = time.Duration(cfg.QueryTimeoutSeconds) * time.Second
	c.RuntimeParams["default_transaction_read_only"] = "on"
	c.RuntimeParams["timezone"] = "UTC"
	c.RuntimeParams["statement_timeout"] = fmt.Sprint(cfg.QueryTimeoutSeconds * 1000)
	conn := stdlib.OpenDB(*c)
	conn.SetMaxOpenConns(cfg.MaxConnections)
	conn.SetMaxIdleConns(cfg.MaxConnections)
	conn.SetConnMaxIdleTime(5 * time.Minute)
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	if err = conn.PingContext(ctx); err != nil {
		return nil, errCrossBorderUnavailable
	}
	var writable bool
	if err = conn.QueryRowContext(ctx, crossBorderReaderPrivilegesSQL).Scan(&writable); err != nil || writable {
		return nil, errCrossBorderUnavailable
	}
	// Enforce a different actual database name, not merely a different URL or
	// role. This also rejects aliases pointing to the main telemetry database.
	// A same-name database on another cluster is deliberately rejected too;
	// this feature's deployment contract uses separately named databases.
	if local != nil && local.conn != nil && local.Backend() == dbconfig.Postgres {
		var localName, peerName string
		if err = local.conn.QueryRowContext(ctx, "SELECT current_database()").Scan(&localName); err != nil {
			return nil, errCrossBorderUnavailable
		}
		if err = conn.QueryRowContext(ctx, "SELECT current_database()").Scan(&peerName); err != nil || localName == peerName {
			return nil, errCrossBorderUnavailable
		}
	}
	if err = dbschema.AssertReadyContext(ctx, conn); err != nil {
		return nil, errCrossBorderUnavailable
	}
	db := &DB{conn: conn, path: dsn, backend: dbconfig.Postgres}
	if err = db.detectSchema(ctx, conn); err != nil {
		return nil, errCrossBorderUnavailable
	}
	ok = true
	return db, nil
}

func crossBorderIn(db *DB, column string, values []string, args *[]any) string {
	if len(values) == 0 {
		return "1=0"
	}
	slots := make([]string, len(values))
	for i, v := range values {
		*args = append(*args, v)
		slots[i] = db.parameter(len(*args))
	}
	return column + " IN (" + strings.Join(slots, ",") + ")"
}

func crossBorderNullableTime(v sql.NullString) *string {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func crossBorderNumber(v sql.NullFloat64) *float64 {
	if !v.Valid || math.IsNaN(v.Float64) || math.IsInf(v.Float64, 0) {
		return nil
	}
	n := v.Float64
	return &n
}

func crossBorderLocation(lat, lon *float64) *CrossBorderLocation {
	if lat == nil || lon == nil || math.IsNaN(*lat) || math.IsNaN(*lon) || math.IsInf(*lat, 0) || math.IsInf(*lon, 0) || *lat < -90 || *lat > 90 || *lon < -180 || *lon > 180 {
		return nil
	}
	// The node row has no independent coordinate report timestamp. General
	// liveness must not be presented as the age of its advertised position.
	return &CrossBorderLocation{Lat: *lat, Lon: *lon}
}

func (db *DB) crossBorderObserverJoin() string {
	if db.isV3 {
		return "JOIN observers ob ON ob.rowid=o.observer_idx"
	}
	return "JOIN observers ob ON ob.id=o.observer_id"
}

func (db *DB) readCrossBorder(ctx context.Context, iatas []string, since, until time.Time, cfg CrossBorderConfig, hashes []string) (crossBorderSnapshot, error) {
	return db.readCrossBorderData(ctx, iatas, since, until, cfg, hashes, hashes == nil)
}

func (db *DB) readCrossBorderData(ctx context.Context, iatas []string, since, until time.Time, cfg CrossBorderConfig, hashes []string, readNodes bool) (crossBorderSnapshot, error) {
	return db.readCrossBorderDataSelected(ctx, iatas, since, until, cfg, hashes, readNodes, false)
}

func (db *DB) readCrossBorderDataSelected(ctx context.Context, iatas []string, since, until time.Time, cfg CrossBorderConfig, hashes []string, readNodes, dashboard bool) (crossBorderSnapshot, error) {
	snapshot := crossBorderSnapshot{Observers: []CrossBorderObserver{}, Nodes: []crossBorderNode{}, Observations: []crossBorderObservationRow{}, Regions: []CrossBorderRegion{}}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	if db == nil || db.conn == nil {
		return snapshot, errCrossBorderUnavailable
	}
	queryCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.QueryTimeoutSeconds)*time.Second)
	defer cancel()
	ctx = queryCtx
	cols, err := schemaColumns(ctx, db.conn, "observers", db.Backend())
	if err != nil {
		return snapshot, err
	}
	radio := "NULL"
	if cols["radio"] {
		radio = "substr(ob.radio,1,256)"
	}
	relay := "NULL"
	if cols["can_relay"] && cols["can_relay_seen"] {
		relay = "CASE WHEN ob.can_relay_seen=1 THEN ob.can_relay ELSE NULL END"
	}
	rowID := "NULL"
	if db.isV3 && cols["rowid"] {
		rowID = "ob.rowid"
	}
	args := []any{}
	where := crossBorderIn(db, "UPPER(TRIM(COALESCE(ob.iata,'')))", iatas, &args)
	args = append(args, cfg.MetadataLimit+1)
	rows, err := db.conn.QueryContext(ctx, `SELECT ob.id,substr(COALESCE(ob.name,''),1,256),COALESCE(ob.iata,''),ob.last_seen,`+radio+`,`+relay+`,n.lat,n.lon,`+rowID+`
 FROM observers ob LEFT JOIN nodes n ON n.public_key=LOWER(ob.id) WHERE `+where+` ORDER BY ob.id LIMIT `+db.parameter(len(args)), args...)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		if len(snapshot.Observers) == cfg.MetadataLimit {
			snapshot.ObserversTruncated = true
			break
		}
		var o CrossBorderObserver
		var seen, hardware sql.NullString
		var canRelay, observerRowID sql.NullInt64
		var lat, lon sql.NullFloat64
		if err = rows.Scan(&o.ID, &o.Name, &o.IATA, &seen, &hardware, &canRelay, &lat, &lon, &observerRowID); err != nil {
			rows.Close()
			return snapshot, err
		}
		o.IATA = strings.ToUpper(strings.TrimSpace(o.IATA))
		o.LastSeenAt = crossBorderNullableTime(seen)
		o.LatestReportedRadioHardware = nullStrPtr(hardware)
		if canRelay.Valid && (canRelay.Int64 == 0 || canRelay.Int64 == 1) {
			value := canRelay.Int64 == 1
			o.ReportedCanRelay = &value
		}
		o.Location = crossBorderLocation(crossBorderNumber(lat), crossBorderNumber(lon))
		snapshot.Observers = append(snapshot.Observers, o)
		if observerRowID.Valid {
			snapshot.SelectedObserverRows = append(snapshot.SelectedObserverRows, crossBorderSelectedObserver{RowID: observerRowID.Int64, ID: o.ID, IATA: o.IATA})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return snapshot, err
	}
	rows.Close()
	// Capture completeness before unrelated region-option caps can set the
	// display truncation flag. Never optimize from only the first 500 observers.
	snapshot.ObserverSelectionComplete = rowID != "NULL" && !snapshot.ObserversTruncated && len(snapshot.SelectedObserverRows) == len(snapshot.Observers)
	selection := db.crossBorderDashboardObserverSelection(snapshot, dashboard)
	snapshot.DashboardObserverSelection = selection
	selectedIATA := crossBorderSelectionIATAs(selection)
	// Options come from known metadata, not inferred nationalities. The local
	// list is constrained to the explicit operator-maintained allowlist.
	args = []any{}
	regionWhere := "1=1"
	if hashes != nil {
		regionWhere = crossBorderIn(db, "UPPER(TRIM(COALESCE(iata,'')))", cfg.AllowedLocalIATA, &args)
	}
	args = append(args, cfg.MetadataLimit+1)
	rows, err = db.conn.QueryContext(ctx, `SELECT UPPER(TRIM(COALESCE(iata,''))),COUNT(*) FROM observers WHERE `+regionWhere+` GROUP BY UPPER(TRIM(COALESCE(iata,''))) ORDER BY 1 LIMIT `+db.parameter(len(args)), args...)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var r CrossBorderRegion
		if err = rows.Scan(&r.IATA, &r.Observers); err != nil {
			rows.Close()
			return snapshot, err
		}
		if len(snapshot.Regions) == cfg.MetadataLimit {
			snapshot.ObserversTruncated = true
			break
		}
		if validCrossBorderIATA(r.IATA) {
			r.Label = r.IATA
			snapshot.Regions = append(snapshot.Regions, r)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return snapshot, err
	}
	rows.Close()
	if selection != nil && len(selection) == 0 {
		if readNodes {
			if err = db.readCrossBorderNodes(ctx, cfg, &snapshot); err != nil {
				return snapshot, err
			}
		}
		snapshot.Available = true
		return snapshot, nil
	}
	historyQuery, args := db.crossBorderHistoryQuery(iatas, selection, until, crossBorderHistoryOldest)
	var oldest sql.NullInt64
	if historyQuery != "" {
		err = db.conn.QueryRowContext(ctx, historyQuery, db.crossBorderSelectionArgs(args, selection)...).Scan(&oldest)
	} else {
		err = nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if oldest.Valid {
		s := crossBorderTime(oldest.Int64)
		snapshot.OldestStoredObservationAt = &s
	}
	// Source freshness is selected-feed activity, independent of matching peer
	// hashes. Zero overlap must not make a fresh reference look disconnected.
	var latest sql.NullInt64
	historyQuery, args = db.crossBorderHistoryQuery(iatas, selection, until, crossBorderHistoryLatest)
	err = db.conn.QueryRowContext(ctx, historyQuery, db.crossBorderSelectionArgs(args, selection)...).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if latest.Valid {
		s := crossBorderTime(latest.Int64)
		snapshot.LatestStoredObservationAt = &s
	}
	// A row after the report cutoff may be concurrent ingestion, or a clock
	// disagreement. Keep it explicit without treating it as zero-age evidence.
	var after sql.NullInt64
	historyQuery, args = db.crossBorderHistoryQuery(iatas, selection, until, crossBorderHistoryAfter)
	err = db.conn.QueryRowContext(ctx, historyQuery, db.crossBorderSelectionArgs(args, selection)...).Scan(&after)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if after.Valid {
		s := crossBorderTime(after.Int64)
		snapshot.LatestAfterSnapshotAt = &s
	}
	if hashes == nil || len(hashes) > 0 {
		query, args := db.crossBorderObservationQuerySelected(iatas, since, until, cfg, hashes, selection)
		// Candidate counts vary between reports. Reuse the existing uncached
		// pgx execution mode instead of filling the main pool's statement cache
		// with many large IN-list shapes. SQLite receives ordinary arguments.
		rows, err = db.conn.QueryContext(ctx, query, db.planWithValues(args)...)
		if err != nil {
			return snapshot, err
		}
		for rows.Next() {
			if len(snapshot.Observations) == cfg.ObservationLimit {
				snapshot.ObservationsTruncated = true
				break
			}
			var row crossBorderObservationRow
			var scope sql.NullString
			var snr, rssi sql.NullFloat64
			if err = rows.Scan(&row.Hash, &row.ObserverID, &row.Direction, &row.Timestamp, &row.PayloadType, &row.RouteType, &scope, &snr, &rssi, &row.RawHex, &row.RawHexLength, &row.CanonicalRawHex, &row.FromPubkey, &row.ObserverIATA); err != nil {
				rows.Close()
				return snapshot, err
			}
			row.ScopeName = nullStrPtr(scope)
			var selected bool
			row.ObserverIATA, selected = crossBorderSelectedIATA(selectedIATA, row.ObserverID, row.ObserverIATA)
			if !selected {
				rows.Close()
				return snapshot, errCrossBorderUnavailable
			}
			row.SNR = crossBorderNumber(snr)
			row.RSSI = crossBorderNumber(rssi)
			snapshot.Observations = append(snapshot.Observations, row)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return snapshot, err
		}
		rows.Close()
	}
	if readNodes {
		if err = db.readCrossBorderNodes(ctx, cfg, &snapshot); err != nil {
			return snapshot, err
		}
	}
	snapshot.Available = true
	return snapshot, nil
}

// A single LIMIT bounds fetched observations. The local query is additionally
// restricted to capped peer hash candidates, so unrelated local traffic cannot
// displace matching reference evidence. ORDER BY uses the observation-time
// index; hash/transmission and observer joins use their existing keys.
func (db *DB) crossBorderObservationQuery(iatas []string, since, until time.Time, cfg CrossBorderConfig, hashes []string) (string, []any) {
	return db.crossBorderObservationQuerySelected(iatas, since, until, cfg, hashes, nil)
}

func (db *DB) crossBorderObservationQuerySelected(iatas []string, since, until time.Time, cfg CrossBorderConfig, hashes []string, selection []crossBorderSelectedObserver) (string, []any) {
	args := []any{since.Unix(), until.Unix()}
	where := []string{"o.timestamp >= " + db.parameter(1), "o.timestamp <= " + db.parameter(2), db.crossBorderObserverWhere(iatas, selection, "o.observer_idx", &args)}
	if hashes != nil {
		where = append(where, crossBorderIn(db, "t.hash", hashes, &args))
	}
	scope := "NULL"
	if db.hasScopeName {
		// Never turn an overlong scope into a different, truncated identity.
		scope = "CASE WHEN length(t.scope_name)<=128 THEN t.scope_name WHEN t.scope_name IS NULL THEN NULL ELSE '' END"
	}
	raw, rawLen := "''", "0"
	if db.hasObsRawHex {
		// A valid firmware packet is at most 254 bytes (header/transport,
		// MAX_PATH_SIZE and MAX_PACKET_PAYLOAD). Keep raw server-only; the
		// scope matcher needs the complete encrypted payload for its HMAC.
		raw = "substr(COALESCE(o.raw_hex,''),1,512)"
		rawLen = "length(COALESCE(o.raw_hex,''))"
	}
	args = append(args, cfg.ObservationLimit+1)
	query := `SELECT t.hash,ob.id,COALESCE(o.direction,''),o.timestamp,COALESCE(t.payload_type,0),COALESCE(t.route_type,0),` + scope + `,o.snr,o.rssi,` + raw + `,` + rawLen + `,substr(COALESCE(t.raw_hex,''),1,12),COALESCE(t.from_pubkey,''),UPPER(TRIM(COALESCE(ob.iata,'')))
 FROM observations o JOIN transmissions t ON t.id=o.transmission_id ` + db.crossBorderObserverJoin() + ` WHERE ` + strings.Join(where, " AND ") + ` ORDER BY o.timestamp DESC,o.id DESC LIMIT ` + db.parameter(len(args))
	return query, args
}

func (db *DB) readCrossBorderNodes(ctx context.Context, cfg CrossBorderConfig, snapshot *crossBorderSnapshot) error {
	return db.readCrossBorderNodesForKeys(ctx, cfg, snapshot, nil)
}

func (db *DB) readCrossBorderNodesForKeys(ctx context.Context, cfg CrossBorderConfig, snapshot *crossBorderSnapshot, keys []string) error {
	scope, scopeAt, truncated := "NULL", "NULL", "0"
	if db.hasConfiguredScope {
		scope = "substr(configured_scope,1,4096)"
		scopeAt = "configured_scope_at"
		truncated = "CASE WHEN length(configured_scope)>4096 THEN 1 ELSE 0 END"
	}
	args := []any{}
	where := "1=1"
	if keys != nil {
		if len(keys) == 0 {
			return nil
		}
		where = crossBorderIn(db, "LOWER(public_key)", keys, &args)
	}
	args = append(args, cfg.MetadataLimit+1)
	rows, err := db.conn.QueryContext(ctx, `SELECT public_key,substr(COALESCE(name,''),1,256),COALESCE(role,''),last_seen,lat,lon,`+scope+`,`+scopeAt+`,`+truncated+` FROM nodes WHERE `+where+` ORDER BY last_seen DESC NULLS LAST,public_key LIMIT `+db.parameter(len(args)), args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		if len(snapshot.Nodes) == cfg.MetadataLimit {
			snapshot.NodesTruncated = true
			break
		}
		var n crossBorderNode
		var seen, scopeValue, reportedAt sql.NullString
		var lat, lon sql.NullFloat64
		var cut int
		if err = rows.Scan(&n.PublicKey, &n.Name, &n.Role, &seen, &lat, &lon, &scopeValue, &reportedAt, &cut); err != nil {
			rows.Close()
			return err
		}
		n.LastSeenAt = crossBorderNullableTime(seen)
		n.Lat = crossBorderNumber(lat)
		n.Lon = crossBorderNumber(lon)
		n.ConfiguredScope = nullStrPtr(scopeValue)
		n.ScopeAt = crossBorderNullableTime(reportedAt)
		if cut != 0 {
			value := true
			n.DeclaredTruncated = &value
		}
		snapshot.Nodes = append(snapshot.Nodes, n)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !db.hasDeclaredRegionsTable || len(snapshot.Nodes) == 0 {
		return nil
	}
	keys = make([]string, 0, len(snapshot.Nodes))
	byKey := map[string]int{}
	for i, n := range snapshot.Nodes {
		key := crossBorderID(n.PublicKey)
		keys = append(keys, key)
		byKey[key] = i
	}
	args = []any{}
	where = crossBorderIn(db, "LOWER(target)", keys, &args)
	args = append(args, cfg.MetadataLimit+1)
	rows, err = db.conn.QueryContext(ctx, `WITH ranked AS (
 SELECT target,observed_at,substr(regions_csv,1,4096) AS regions_csv,CASE WHEN length(regions_csv)>4096 THEN 1 ELSE truncated END AS truncated,
 ROW_NUMBER() OVER(PARTITION BY LOWER(target) ORDER BY observed_at DESC NULLS LAST) AS rn
 FROM node_declared_regions WHERE `+where+`)
 SELECT target,observed_at,regions_csv,truncated FROM ranked WHERE rn=1 LIMIT `+db.parameter(len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		var at, csv sql.NullString
		var cut sql.NullInt64
		if err = rows.Scan(&target, &at, &csv, &cut); err != nil {
			return err
		}
		i, ok := byKey[crossBorderID(target)]
		if !ok {
			continue
		}
		n := &snapshot.Nodes[i]
		reported := crossBorderNullableTime(at)
		if n.ConfiguredScope != nil && n.ScopeAt != nil && (reported == nil || *reported <= *n.ScopeAt) {
			continue
		}
		n.ConfiguredScope = nullStrPtr(csv)
		n.ScopeAt = reported
		n.DeclaredTruncated = nil
		if cut.Valid {
			v := cut.Int64 != 0
			n.DeclaredTruncated = &v
		}
	}
	return rows.Err()
}

func crossBorderIdentityKeys(ids map[string]bool) []string {
	keys := make([]string, 0, len(ids))
	for id := range ids {
		if id != "" {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	return keys
}

func (db *DB) crossBorderIdentitySQL(column string) string {
	// Match canonical full public keys while preserving opaque IDs verbatim.
	return db.nativeSQL(`CASE WHEN length(`+column+`)=64 AND lower(`+column+`) NOT GLOB '*[^0-9a-f]*' THEN lower(`+column+`) ELSE `+column+` END`, `CASE WHEN `+column+` ~ '^[0-9a-fA-F]{64}$' THEN lower(`+column+`) ELSE `+column+` END`)
}

// The union returns at most one row per bounded candidate identity. Name
// matching uses full DB names, not the display prefixes or metadata caps.
func (db *DB) crossBorderHiddenIDs(ctx context.Context, ids map[string]bool, cfg *Config) (map[string]bool, error) {
	hidden := map[string]bool{}
	keys := crossBorderIdentityKeys(ids)
	if len(keys) == 0 || cfg == nil {
		return hidden, nil
	}
	cfg.IsNameHidden("") // Materialize the existing atomic prefix snapshot.
	prefixes := cfg.hiddenPrefixesPtr.Load()
	args := []any{}
	observerID, nodeID := db.crossBorderIdentitySQL("id"), db.crossBorderIdentitySQL("public_key")
	observerWhere := crossBorderIn(db, observerID, keys, &args)
	// Both branches reuse the same bound candidate parameters.
	nodeWhere := strings.Replace(observerWhere, observerID, nodeID, 1)
	names := []string{}
	for _, prefix := range *prefixes {
		if prefix != "" {
			args = append(args, prefix)
			p := db.parameter(len(args))
			names = append(names, "substr(COALESCE(name,''),1,length("+p+"))="+p)
		}
	}
	if len(names) == 0 {
		return hidden, nil
	}
	nameWhere := "(" + strings.Join(names, " OR ") + ")"
	query := `SELECT ` + observerID + ` FROM observers WHERE ` + observerWhere + ` AND ` + nameWhere +
		` UNION SELECT ` + nodeID + ` FROM nodes WHERE ` + nodeWhere + ` AND ` + nameWhere
	rows, err := db.conn.QueryContext(ctx, query, db.planWithValues(args)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		hidden[id] = true
	}
	return hidden, rows.Err()
}

func (db *DB) crossBorderSharedIDs(ctx context.Context, ids map[string]bool) ([]string, error) {
	keys := crossBorderIdentityKeys(ids)
	out := []string{}
	if len(keys) == 0 {
		return out, nil
	}
	args := []any{}
	identity := db.crossBorderIdentitySQL("id")
	where := crossBorderIn(db, identity, keys, &args)
	rows, err := db.conn.QueryContext(ctx, `SELECT DISTINCT `+identity+` FROM observers WHERE `+where+` LIMIT `+fmt.Sprint(len(keys)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
