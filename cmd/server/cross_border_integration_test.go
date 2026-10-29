package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestCrossBorderCappedAndEmptyNodeIndexCannotClaimForwarder(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, capped := range []bool{true, false} {
		peer := crossBorderSnapshot{Available: true, NodesTruncated: capped, Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB"}},
			Observations: []crossBorderObservationRow{{Hash: "flood", ObserverID: "peer", Direction: "rx", Timestamp: now.Unix() - 1, PayloadType: PayloadGRP_TXT, RouteType: RouteFlood, RawHex: "1541aaaaff", CanonicalRawHex: "1541aaaaff"}}}
		if capped {
			peer.Nodes = []crossBorderNode{{PublicKey: strings.Repeat("a", 64), Role: "repeater"}}
		}
		got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Packets) != 1 || got.Packets[0].PeerObservations[0].HopWidth == nil || *got.Packets[0].PeerObservations[0].HopWidth != 2 {
			t.Fatal("incomplete node metadata must preserve packet header evidence")
		}
		for _, r := range got.PeerRepeaters {
			if r.SampleForwardedPackets != 0 {
				t.Fatalf("capped/empty index fabricated an unambiguous forwarder: %+v", r)
			}
		}
		if capped && (got.Completeness.Complete || !got.Sources.Peer.NodesTruncated) {
			t.Fatal("node-index cap must be explicit")
		}
	}
}

func TestCrossBorderNativeQueryDeadline(t *testing.T) {
	db := crossBorderFixture(t)
	// Occupy the only fixture connection: cancellation must interrupt pool
	// acquisition instead of waiting for the configured report timeout.
	conn, err := db.conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = db.readCrossBorder(ctx, []string{"AAA"}, time.Now().Add(-time.Hour), time.Now(), crossBorderTestConfig(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query/pool deadline ignored: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("canceled read stayed queued")
	}
}

func TestCrossBorderNativeServiceKeepsMainDataIsolated(t *testing.T) {
	local := crossBorderFixture(t)
	var peer *DB
	if testBackend(t) == dbconfig.Postgres {
		peer, _ = crossBorderReadyPostgresFixture(t)
	} else {
		peer = crossBorderFixture(t)
	}
	now := time.Now().Unix()
	crossBorderExec(t, local, `INSERT INTO observers(rowid,id,iata) VALUES (1,'local','AAA'),(2,'mirrored','CCC')`)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,iata) VALUES (1,'peer','BBB'),(2,'mirrored','BBB')`)
	crossBorderExec(t, local, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type) VALUES (1,'local-only','1500ff',1,5)`)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,decoded_json,first_seen) VALUES (1,'peer-only','1500ff',1,5,'private-decrypted-sentinel',$1)`, crossBorderTime(now))
	crossBorderExec(t, local, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff')`, now)
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff'),(1,2,$1,'rx','1500ff')`, now)
	cfg := crossBorderTestConfig()
	srv := NewServer(local, &Config{CrossBorder: &cfg}, nil)
	srv.crossBorder.peer = peer
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border", nil))
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var response CrossBorderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Summary.PeerPackets != 1 || response.Summary.LocalReferencePackets != 0 || response.Summary.DuplicateObserverIDs != 1 {
		t.Fatalf("isolated comparison or global duplicate identities wrong: %+v", response.Summary)
	}
	if response.Sources.Local.LatestObservationAt == nil || response.Sources.Local.Status != "available" {
		t.Fatal("a fresh local source with no matching peer content was labelled empty/stale")
	}
	if srv.db != local || srv.store != nil {
		t.Fatal("peer reader replaced the main database/store")
	}
	var localCount, peerInLocal int
	if err := local.conn.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&localCount); err != nil {
		t.Fatal(err)
	}
	if err := local.conn.QueryRow(`SELECT COUNT(*) FROM transmissions WHERE hash='peer-only'`).Scan(&peerInLocal); err != nil {
		t.Fatal(err)
	}
	if localCount != 1 || peerInLocal != 0 {
		t.Fatal("peer telemetry entered local storage")
	}
	if strings.Contains(w.Body.String(), "private-decrypted-sentinel") || strings.Contains(w.Body.String(), "raw_hex") {
		t.Fatal("payload material leaked")
	}
}

func TestCrossBorderPeerPoolReadOnlyAndSeparateDatabase(t *testing.T) {
	postgresOnly(t)
	local := crossBorderFixture(t)
	ownerDB, ownerURL := crossBorderReadyPostgresFixture(t)
	owner := ownerDB.conn
	readerURL := pgtest.ReadOnly(t, ownerURL)
	cfg := crossBorderTestConfig()
	peer, err := openCrossBorderPeer(context.Background(), local, readerURL, cfg)
	if err != nil {
		t.Fatal("separate read-only peer rejected", err)
	}
	t.Cleanup(func() { peer.conn.Close() })
	if peer.conn.Stats().MaxOpenConnections != cfg.MaxConnections {
		t.Fatal("peer pool did not honor its own small connection bound")
	}
	var readOnly string
	if err := peer.conn.QueryRow(`SHOW default_transaction_read_only`).Scan(&readOnly); err != nil || readOnly != "on" {
		t.Fatalf("peer sessions not read-only: %q %v", readOnly, err)
	}
	if _, err := peer.conn.Exec(`CREATE TABLE forbidden_cross_border_write(id INTEGER)`); err == nil {
		t.Fatal("peer connection can write")
	}
	if db, err := openCrossBorderPeer(context.Background(), local, ownerURL, cfg); err == nil {
		db.conn.Close()
		t.Fatal("privileged owner accepted as public diagnostics reader")
	}
	same := &DB{conn: owner, backend: dbconfig.Postgres}
	if db, err := openCrossBorderPeer(context.Background(), same, readerURL, cfg); err == nil {
		db.conn.Close()
		t.Fatal("alternate reader URL to main database accepted as separate peer")
	}
}

func TestCrossBorderSQLPlanAndBoundedLargeWindow(t *testing.T) {
	db := crossBorderFixture(t)
	now := time.Unix(1800000000, 0).UTC()
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(1,'local','AAA'),(2,'other','CCC')`)
	const count = 30000
	copyTestRows(t, db.conn, "transmissions", []string{"id", "hash", "raw_hex", "route_type", "payload_type"}, count, func(i int) []any { return []any{i + 1, fmt.Sprintf("%016x", i+1), "1500ff", 1, 5} })
	copyTestRows(t, db.conn, "observations", []string{"transmission_id", "observer_idx", "timestamp", "direction", "raw_hex"}, count*2, func(i int) []any { return []any{i/2 + 1, 1, now.Unix() - int64(i%60), "rx", "1500ff"} })
	crossBorderExec(t, db, `ANALYZE`)
	hashes := make([]string, 20)
	for i := range hashes {
		hashes[i] = fmt.Sprintf("%016x", i+1)
	}
	cfg := crossBorderTestConfig()
	start := time.Now()
	got, err := db.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, cfg, hashes)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observations) != 40 || got.ObservationsTruncated {
		t.Fatalf("unrelated traffic consumed candidate window: rows=%d capped=%v", len(got.Observations), got.ObservationsTruncated)
	}
	t.Logf("local peer-candidate read: %d transmissions / %d observations, 20 hashes -> 40 rows in %s", count, count*2, time.Since(start))
	query, args := db.crossBorderObservationQuery([]string{"AAA"}, now.Add(-time.Hour), now, cfg, hashes)
	plan := []string{}
	if db.Backend() == dbconfig.Postgres {
		rows, err := db.conn.Query(`EXPLAIN (ANALYZE,BUFFERS,FORMAT TEXT) `+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	} else {
		rows, err := db.conn.Query(`EXPLAIN QUERY PLAN `+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	text := strings.Join(plan, "\n")
	t.Logf("candidate query plan:\n%s", text)
	if !strings.Contains(text, "cross_border_observation_tx") {
		t.Fatal("candidate query did not use the transmission/time index")
	}
}

func TestCrossBorderRepresentativeResponseSize(t *testing.T) {
	cfg := (&Config{CrossBorder: &CrossBorderConfig{Enabled: true, AllowedLocalIATA: []string{"AAA"}, DefaultLocalIATA: []string{"AAA"}, DefaultPeerIATA: []string{"BBB"}}}).GetCrossBorder()
	now := time.Unix(1800000000, 0).UTC()
	local, peer := crossBorderSnapshot{Available: true}, crossBorderSnapshot{Available: true}
	for i := 0; i < cfg.MetadataLimit; i++ {
		pk := fmt.Sprintf("%064x", i+1)
		peer.Observers = append(peer.Observers, CrossBorderObserver{ID: pk, Name: "Example observer", IATA: "BBB", LatestReportedRadioHardware: crossBorderString("SX1262")})
		local.Observers = append(local.Observers, CrossBorderObserver{ID: "reference-" + pk, IATA: "AAA"})
		peer.Nodes = append(peer.Nodes, crossBorderNode{PublicKey: pk, Name: "Example repeater", Role: "repeater", ConfiguredScope: crossBorderString("example,*")})
	}
	for i := 0; i < cfg.ObservationLimit; i++ {
		pk := fmt.Sprintf("%064x", i%cfg.MetadataLimit+1)
		hash := fmt.Sprintf("%016x", i+1)
		peer.Observations = append(peer.Observations, crossBorderObservation(hash, pk, "rx", now.Unix()-int64(i%60)))
		local.Observations = append(local.Observations, crossBorderObservation(hash, "reference-"+pk, "rx", now.Unix()-int64(i%60)))
	}
	start := time.Now()
	got, err := buildCrossBorder(context.Background(), cfg, crossBorderTestFilters(), local, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packets) != cfg.PacketLimit || len(got.PeerObservers) != cfg.MetadataLimit || len(got.PeerRepeaters) != cfg.MetadataLimit {
		t.Fatal("representative result did not exercise configured response caps")
	}
	if len(body) > 2*1024*1024 {
		t.Fatalf("representative serialized response unexpectedly exceeds 2 MiB: %d bytes", len(body))
	}
	t.Logf("5000 observations per source, 500 observers/repeaters: report+JSON=%s; JSON=%d bytes; cache4 serialized equivalent=%d bytes (not a heap estimate)", time.Since(start), len(body), 4*len(body))
}

func TestCrossBorderPrivacyGenerationInvalidatesCache(t *testing.T) {
	config := &Config{}
	svc := &crossBorderService{cfg: crossBorderTestConfig(), serverCfg: config}
	svc.load = func(ctx context.Context, f CrossBorderFilters) (CrossBorderResponse, error) {
		response := CrossBorderResponse{PeerObservers: []CrossBorderObserver{}}
		if !config.IsBlacklisted("peer") && !config.IsNameHidden("Hidden observer") {
			response.PeerObservers = append(response.PeerObservers, CrossBorderObserver{ID: "peer", Name: "Hidden observer"})
		}
		return response, nil
	}
	got, err := svc.response(context.Background(), crossBorderTestFilters())
	if err != nil || len(got.PeerObservers) != 1 {
		t.Fatal("initial fixture failed", err)
	}
	config.SetNodeBlacklist([]string{"peer"})
	got, err = svc.response(context.Background(), crossBorderTestFilters())
	if err != nil || len(got.PeerObservers) != 0 {
		t.Fatal("cached response still exposes newly blacklisted observer")
	}
	config.SetNodeBlacklist(nil)
	got, err = svc.response(context.Background(), crossBorderTestFilters())
	if err != nil || len(got.PeerObservers) != 1 {
		t.Fatal("blacklist generation was not observed")
	}
	config.SetHiddenNamePrefixes([]string{"Hidden"})
	got, err = svc.response(context.Background(), crossBorderTestFilters())
	if err != nil || len(got.PeerObservers) != 0 {
		t.Fatal("cached response still exposes newly hidden name")
	}
}

func TestCrossBorderUninitializedPeerIsRetryableUnavailable(t *testing.T) {
	postgresOnly(t)
	local := crossBorderFixture(t)
	url := pgtest.ReadOnly(t, pgtest.NewDatabase(t))
	db, err := openCrossBorderPeer(context.Background(), local, url, crossBorderTestConfig())
	if err == nil {
		db.conn.Close()
		t.Fatal("empty peer schema was latched as an initialized reader")
	}
}

func TestCrossBorderShutdownCancelsOnlyItsReports(t *testing.T) {
	started := make(chan struct{})
	svc := &crossBorderService{cfg: crossBorderTestConfig(), load: func(ctx context.Context, f CrossBorderFilters) (CrossBorderResponse, error) {
		close(started)
		<-ctx.Done()
		return CrossBorderResponse{}, ctx.Err()
	}}
	srv := &Server{crossBorder: svc}
	done := make(chan error, 1)
	go func() { _, err := svc.response(context.Background(), crossBorderTestFilters()); done <- err }()
	<-started
	srv.closeCrossBorder()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown did not cancel owned report: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown left a report running")
	}
	if _, err := svc.response(context.Background(), crossBorderTestFilters()); !errors.Is(err, errCrossBorderUnavailable) {
		t.Fatal("closed report service accepted new work")
	}
}

func TestCrossBorderOverlongScopeDoesNotInventTruncatedName(t *testing.T) {
	db := crossBorderFixture(t)
	now := time.Now().Unix()
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(1,'peer','BBB')`)
	crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,scope_name) VALUES(1,'scoped','140102000040ff',0,5,$1)`, strings.Repeat("a", 129))
	crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','140102000040ff')`, now)
	snapshot, err := db.readCrossBorder(context.Background(), []string{"BBB"}, time.Now().Add(-time.Hour), time.Now(), crossBorderTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Observations) != 1 || snapshot.Observations[0].ScopeName == nil || *snapshot.Observations[0].ScopeName != "" {
		t.Fatal("overlong scope became a different truncated name instead of unknown")
	}
}

func TestCrossBorderObserverIdentityPreservesOpaqueIDs(t *testing.T) {
	fullKey := strings.Repeat("A", 64)
	if got := crossBorderID(fullKey); got != strings.ToLower(fullKey) {
		t.Fatal("full public key not canonicalized")
	}
	for _, id := range []string{"opaque-A", " opaque-A "} {
		if got := crossBorderID(id); got != id {
			t.Fatalf("opaque observer identity changed: %q -> %q", id, got)
		}
	}
	db := crossBorderFixture(t)
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(1,$1,'AAA'),(2,'opaque-A','AAA')`, fullKey)
	ids, err := db.crossBorderSharedIDs(context.Background(), map[string]bool{strings.ToLower(fullKey): true, "opaque-a": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != strings.ToLower(fullKey) {
		t.Fatalf("SQL duplicate lookup conflated opaque IDs or missed key casing: %v", ids)
	}
}

func TestCrossBorderRegionOptionsSurviveNarrowSelection(t *testing.T) {
	db := crossBorderFixture(t)
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(1,'one','AAA'),(2,'two','CCC'),(3,'three','DDD')`)
	cfg := crossBorderTestConfig()
	cfg.AllowedLocalIATA = []string{"AAA", "CCC"}
	local, err := db.readCrossBorder(context.Background(), []string{"AAA"}, time.Now().Add(-time.Hour), time.Now(), cfg, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Observers) != 1 || len(local.Regions) != 2 || local.Regions[1].IATA != "CCC" {
		t.Fatalf("local options narrowed to selected metadata or escaped allowlist: %+v", local.Regions)
	}
	peer, err := db.readCrossBorder(context.Background(), []string{"AAA"}, time.Now().Add(-time.Hour), time.Now(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(peer.Observers) != 1 || len(peer.Regions) != 3 {
		t.Fatalf("peer options narrowed to selected metadata: %+v", peer.Regions)
	}
}

func TestCrossBorderUnknownIdentityIsNotKnownSharedObserver(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, missingID := range []bool{false, true} {
		id := "peer"
		if missingID {
			id = ""
		}
		peer := crossBorderSnapshot{Available: true, ObserversTruncated: !missingID, Observations: []crossBorderObservationRow{crossBorderObservation("content", id, "rx", now.Unix()-1)}}
		local := crossBorderSnapshot{Available: true, Observations: []crossBorderObservationRow{crossBorderObservation("content", "local", "rx", now.Unix()-1)}}
		got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Packets) != 1 || got.Packets[0].Overlap != "identity_unverified" || got.Summary.SharedObserverOnlyPackets != 0 || got.Summary.IndependentSharedPackets != 0 {
			t.Fatalf("unknown identity became a known overlap claim: %+v", got.Summary)
		}
	}
}

func TestCrossBorderPeerScopeRetainsDifferentLocalScopes(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	f := crossBorderTestFilters()
	f.Scope = "example"
	peer := crossBorderSnapshot{Available: true, Observations: []crossBorderObservationRow{{Hash: "content", ObserverID: "peer", Direction: "rx", Timestamp: now.Unix() - 1, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood, ScopeName: crossBorderString("#example"), RawHex: "140102000040ff", CanonicalRawHex: "140102000040ff"}}}
	local := crossBorderSnapshot{Available: true, Observations: []crossBorderObservationRow{
		{Hash: "content", ObserverID: "local-unknown", Direction: "rx", Timestamp: now.Unix() - 1, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood, ScopeName: crossBorderString(""), RawHex: "140304000040ff", CanonicalRawHex: "140304000040ff"},
		crossBorderObservation("content", "local-unscoped", "rx", now.Unix()-1)}}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), f, local, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packets) != 1 || got.Packets[0].Overlap != "independent" || got.Packets[0].LocalObservationCount != 2 {
		t.Fatalf("peer scope discarded matching local content with different/unresolved scope: %+v", got.Summary)
	}
	kinds := map[string]bool{}
	for _, o := range got.Packets[0].LocalObservations {
		kinds[o.Scope.Kind] = true
	}
	if !kinds["unknown"] || !kinds["unscoped"] || kinds["named"] {
		t.Fatalf("peer scope was transferred to local evidence: %v", kinds)
	}
}

func TestCrossBorderNativeDeclaredScopeReportsStayDatedAndPartial(t *testing.T) {
	db := crossBorderFixture(t)
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	oldAt, newAt := "2026-01-01T12:00:00Z", "2026-01-02T12:00:00Z"
	crossBorderExec(t, db, `INSERT INTO nodes(public_key,name,role,configured_scope,configured_scope_at,last_seen) VALUES($1,'Example one','repeater','#old',$2,$3),($4,'Example two','repeater','',NULL,$3)`, first, oldAt, newAt, second)
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(1,$1,'BBB'),(2,$2,'BBB')`, first, second)
	crossBorderExec(t, db, `CREATE TABLE node_declared_regions(target TEXT,observed_at TEXT,regions_csv TEXT,truncated INTEGER)`)
	crossBorderExec(t, db, `INSERT INTO node_declared_regions(target,observed_at,regions_csv,truncated) VALUES($1,$2,'obsolete',0),($3,$4,'#new,*',1)`, first, oldAt, strings.ToUpper(first), newAt)
	db.hasDeclaredRegionsTable = true
	peer, err := db.readCrossBorder(context.Background(), []string{"BBB"}, time.Now().Add(-time.Hour), time.Now(), crossBorderTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PeerRepeaters) != 2 {
		t.Fatalf("reported metadata rows missing: %d", len(got.PeerRepeaters))
	}
	for _, r := range got.PeerRepeaters {
		if r.PublicKey == first {
			if r.DeclaredScopes == nil || len(r.DeclaredScopes.Names) != 1 || r.DeclaredScopes.Names[0] != "new" || !r.DeclaredScopes.AllowsUnscoped || r.DeclaredScopes.ReportedAt == nil || *r.DeclaredScopes.ReportedAt != newAt || r.DeclaredScopes.Truncated == nil || !*r.DeclaredScopes.Truncated {
				t.Fatalf("newest partial report lost its evidence: %+v", r.DeclaredScopes)
			}
		}
		if r.PublicKey == second {
			if r.DeclaredScopes == nil || len(r.DeclaredScopes.Names) != 0 || r.DeclaredScopes.ReportedAt != nil || r.DeclaredScopes.Truncated != nil {
				t.Fatalf("empty report borrowed liveness time/completeness: %+v", r.DeclaredScopes)
			}
		}
	}
}
