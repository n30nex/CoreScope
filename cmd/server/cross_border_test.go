package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func crossBorderTestConfig() CrossBorderConfig {
	return CrossBorderConfig{Enabled: true, LocalLabel: "Local reference", PeerLabel: "Peer telemetry",
		AllowedLocalIATA: []string{"AAA"}, DefaultLocalIATA: []string{"AAA"}, DefaultPeerIATA: []string{"BBB"},
		DefaultWindowMinutes: 60, MaxWindowMinutes: 360, ObservationLimit: 5000, MetadataLimit: 2000,
		PacketLimit: 100, ObservationsPerPacket: 8, QueryTimeoutSeconds: 3, CacheSeconds: 30,
		CacheEntries: 16, MaxConnections: 2, StaleAfterSeconds: 600}
}

func crossBorderTestFilters() CrossBorderFilters {
	return CrossBorderFilters{LocalIATA: []string{"AAA"}, PeerIATA: []string{"BBB"}, Scope: "all", WindowMinutes: 60}
}

func crossBorderString(v string) *string  { return &v }
func crossBorderFloat(v float64) *float64 { return &v }

func TestCrossBorderConfigOptInAndBounds(t *testing.T) {
	var absent *Config
	if got := absent.GetCrossBorder(); got.Enabled || got.ObservationLimit <= 0 || got.QueryTimeoutSeconds <= 0 || got.MaxConnections <= 0 {
		t.Fatalf("absent config must be disabled with bounded defaults; got %+v", got)
	}
	custom := crossBorderTestConfig()
	custom.ObservationLimit, custom.PacketLimit, custom.CacheEntries = 31, 7, 3
	got := (&Config{CrossBorder: &custom}).GetCrossBorder()
	if !got.Enabled || got.ObservationLimit != 31 || got.PacketLimit != 7 || got.CacheEntries != 3 {
		t.Fatalf("operator limits must survive configuration: %+v", got)
	}
}

func TestCrossBorderFiltersExplicitAllowedRegions(t *testing.T) {
	cfg := crossBorderTestConfig()
	for _, tc := range []struct {
		query     string
		wantError bool
	}{
		{"", false},
		{"localIata=aaa,AAA&peerIata=bbb&scope=%23example&windowMinutes=15", false},
		{"localIata=CCC", true},
		{"localIata=ALL", true},
		{"windowMinutes=1440", true},
		{"windowMinutes=abc", true},
		{"peerIata=%27%3BDROP+TABLE+observers", true},
	} {
		t.Run(tc.query, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseCrossBorderFilters(q, cfg)
			if tc.wantError {
				if err == nil {
					t.Fatalf("must reject unsupported or outside-allowlist selection %q", tc.query)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.LocalIATA, []string{"AAA"}) || !reflect.DeepEqual(got.PeerIATA, []string{"BBB"}) {
				t.Fatalf("must use explicit canonical defaults/selection; got %+v", got)
			}
			if tc.query != "" && (got.Scope != "example" || got.WindowMinutes != 15) {
				t.Fatalf("canonical scope/window: %+v", got)
			}
		})
	}
}

func crossBorderObservation(hash, observer, direction string, at int64) crossBorderObservationRow {
	return crossBorderObservationRow{Hash: hash, ObserverID: observer, Direction: direction,
		Timestamp: at, PayloadType: PayloadGRP_TXT, RouteType: RouteFlood, RawHex: "1500aabb", CanonicalRawHex: "1500aabb"}
}

func TestCrossBorderPeerPrimaryIndependentOverlapAndDirection(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	local := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{
		{ID: "local", IATA: "AAA"}, {ID: "shared", IATA: "AAA"}}, Observations: []crossBorderObservationRow{
		crossBorderObservation("shared-content", "local", "rx", now.Unix()-5),
		crossBorderObservation("duplicate-only", "shared", "rx", now.Unix()-5),
		crossBorderObservation("local-only", "local", "rx", now.Unix()-5),
		crossBorderObservation("peer-only", "local", " Tx ", now.Unix()-5)}}
	peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{
		{ID: "peer", IATA: "BBB"}, {ID: "shared", IATA: "BBB"}}, Observations: []crossBorderObservationRow{
		crossBorderObservation("shared-content", "peer", "RX", now.Unix()-10),
		crossBorderObservation("duplicate-only", "shared", "rx", now.Unix()-10),
		crossBorderObservation("peer-only", "peer", "", now.Unix()-10),
		crossBorderObservation("tx-only", "peer", "tX", now.Unix()-10)}}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	want := CrossBorderSummary{PeerPackets: 3, LocalReferencePackets: 2, IndependentSharedPackets: 1, SharedObserverOnlyPackets: 1, NotSeenLocallyPackets: 1, DuplicateObserverIDs: 1}
	if got.Summary != want {
		t.Fatalf("peer-first same-observation join: got %+v, want %+v", got.Summary, want)
	}
	if got.Sources.Peer.ExcludedTXObservations != 1 || got.Sources.Local.ExcludedTXObservations != 1 || got.Sources.Peer.UnknownDirectionObservations != 1 {
		t.Fatalf("TX and unknown direction must remain separate: %+v", got.Sources)
	}
	for _, p := range got.Packets {
		if p.Hash == "local-only" || p.Hash == "tx-only" {
			t.Errorf("non-peer or TX-only content leaked into peer rows: %s", p.Hash)
		}
		if p.Hash == "duplicate-only" && p.Overlap != "shared_observer_only" {
			t.Errorf("shared observer inflated independent evidence: %+v", p)
		}
		if p.Hash == "peer-only" && (p.Overlap != "not_seen_locally" || p.PeerObservations[0].Direction != "unknown") {
			t.Errorf("TX contaminated local receive evidence: %+v", p)
		}
	}
}

func TestCrossBorderMissingMetadataPrivacyAndScopeEvidence(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	rows := []crossBorderObservationRow{
		{Hash: "known", ObserverID: "peer", Timestamp: now.Unix() - 1, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood,
			RawHex: "140102000040aabb", CanonicalRawHex: "140102000040aabb", ScopeName: crossBorderString("#example")},
		{Hash: "changed-scope", ObserverID: "peer", Timestamp: now.Unix() - 2, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood,
			RawHex: "140304000080aabb", CanonicalRawHex: "140102000040aabb", ScopeName: crossBorderString("#example")},
		{Hash: "unscoped", ObserverID: "peer", Timestamp: now.Unix() - 3, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood,
			RawHex: "1500aabb", CanonicalRawHex: "140102000040aabb", ScopeName: crossBorderString("#example")},
	}
	peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB"}}, Observations: rows}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packets) != 3 || len(got.PeerObservers) != 1 {
		t.Fatalf("missing metadata must not discard observations: %+v", got.Summary)
	}
	states := map[string]string{}
	for _, p := range got.Packets {
		states[p.Hash] = p.Scope.Kind
		if len(p.PeerObservations) != 1 {
			t.Fatalf("missing observation evidence for %s", p.Hash)
		}
		if p.PeerObservations[0].SNR != nil || p.PeerObservations[0].RSSI != nil {
			t.Errorf("unknown RF measurements must stay null: %+v", p)
		}
	}
	if states["known"] != "named" || states["changed-scope"] != "unknown" || states["unscoped"] != "unscoped" {
		t.Fatalf("scope must be supported by same observation header: %v", states)
	}
	if got.PeerObservers[0].LatestReportedRadioHardware != nil || got.PeerObservers[0].ReportedCanRelay != nil || got.PeerObservers[0].Location != nil {
		t.Fatal("missing metadata became a setting/location")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"aabb", "rawHex", "decoded_json", "payloadText", "databaseURL"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("private/unneeded wire content leaked: %s", secret)
		}
	}
}

func TestCrossBorderRepeaterForwardingNeedsUnambiguousFloodEvidence(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	pk := strings.Repeat("a", 64)
	peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB"}},
		Nodes: []crossBorderNode{{PublicKey: pk, Name: "Example repeater", Role: "repeater", ConfiguredScope: crossBorderString("#example,*"), ScopeAt: crossBorderString(now.Add(-time.Hour).Format(time.RFC3339))}},
		Observations: []crossBorderObservationRow{
			{Hash: "forwarded", ObserverID: "peer", Direction: "rx", Timestamp: now.Unix() - 1, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood, ScopeName: crossBorderString("#example"), RawHex: "140102000041aaaaff", CanonicalRawHex: "140102000041aaaaff"},
			{Hash: "direct-not-forwarder", ObserverID: "peer", Direction: "rx", Timestamp: now.Unix() - 2, PayloadType: PayloadGRP_TXT, RouteType: RouteDirect, RawHex: "1641aaaaff", CanonicalRawHex: "1641aaaaff"},
			{Hash: "one-byte-ambiguous", ObserverID: "peer", Direction: "rx", Timestamp: now.Unix() - 3, PayloadType: PayloadGRP_TXT, RouteType: RouteFlood, RawHex: "1501aaff", CanonicalRawHex: "1501aaff"}}}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PeerRepeaters) != 1 {
		t.Fatalf("want one supported peer repeater row, got %d", len(got.PeerRepeaters))
	}
	r := got.PeerRepeaters[0]
	if r.SampleForwardedPackets != 1 || r.DeclaredScopes == nil || !r.DeclaredScopes.AllowsUnscoped || r.DeclaredScopes.Truncated != nil || !reflect.DeepEqual(r.DeclaredScopes.Names, []string{"example"}) {
		t.Fatalf("declared facts and observed forwarding must remain separate: %+v", r)
	}
	if len(r.ObservedScopes) != 1 || r.ObservedScopes[0].Name == nil || *r.ObservedScopes[0].Name != "example" {
		t.Fatalf("observed scope evidence: %+v", r)
	}
	peer.Nodes = append(peer.Nodes, crossBorderNode{PublicKey: "aaaa" + strings.Repeat("b", 60), Role: "repeater"})
	got, err = buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.PeerRepeaters {
		if r.SampleForwardedPackets != 0 {
			t.Fatalf("colliding two-byte prefixes must not be attributed: %+v", r)
		}
	}
}

func TestCrossBorderCapsEmptyUnavailableAndCancellation(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := crossBorderTestConfig()
	cfg.PacketLimit, cfg.ObservationsPerPacket = 1, 1
	peer := crossBorderSnapshot{Available: true, ObservationsTruncated: true,
		Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB"}}, Observations: []crossBorderObservationRow{
			crossBorderObservation("same", "peer", "rx", now.Unix()-1), crossBorderObservation("same", "peer", "rx", now.Unix()-2), crossBorderObservation("other", "peer", "rx", now.Unix()-3)}}
	got, err := buildCrossBorder(context.Background(), cfg, crossBorderTestFilters(), crossBorderSnapshot{}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "partial" || got.Completeness.Complete || !got.Completeness.PacketsTruncated || len(got.Packets) != 1 || len(got.Packets[0].PeerObservations) != 1 || !got.Packets[0].ObservationsTruncated {
		t.Fatalf("every truncation must be visible while rows stay bounded: %+v", got)
	}
	if got.Packets[0].Overlap != "local_unavailable" || got.Summary.NotSeenLocallyPackets != 0 {
		t.Fatalf("unavailable reference is not negative evidence: %+v", got.Summary)
	}
	empty, err := buildCrossBorder(context.Background(), cfg, crossBorderTestFilters(), crossBorderSnapshot{Available: true}, crossBorderSnapshot{Available: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Sources.Peer.Status != "empty" || empty.Packets == nil || empty.PeerObservers == nil || empty.PeerRepeaters == nil {
		t.Fatal("empty source must have typed empty collections and honest empty status")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildCrossBorder(ctx, cfg, crossBorderTestFilters(), crossBorderSnapshot{}, peer, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled build must stop: %v", err)
	}
}

func TestCrossBorderCommunityReferenceDoesNotBecomeDeviceSetting(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.PeerReferenceProfile = &CrossBorderReferenceProfile{Label: "Community recommendation", URL: "https://example.org/radio", FrequencyMHz: 910.525, BandwidthKHz: 62.5, SpreadingFactor: 7, CodingRate: 8, HopWidthBytes: 2}
	peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB", LatestReportedRadioHardware: crossBorderString("SX1262")}}}
	got, err := buildCrossBorder(context.Background(), cfg, crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources.Peer.ReferenceProfile == nil || got.Sources.Peer.ReferenceProfile.FrequencyMHz != 910.525 {
		t.Fatal("configured recommendation must remain separately labelled")
	}
	if len(got.PeerObservers) != 1 || got.PeerObservers[0].RadioHardwareReportedAt != nil || got.PeerObservers[0].CanRelayReportedAt != nil {
		t.Fatal("must not invent per-device report times")
	}
}

func TestCrossBorderPublicEndpointDisabledAndUnavailable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			t.Setenv("CORESCOPE_CROSS_BORDER_PEER_DATABASE_URL", "private-sentinel-invalid-url")
			cfg := crossBorderTestConfig()
			cfg.Enabled = enabled
			srv := NewServer(nil, &Config{CrossBorder: &cfg, APIKey: "private-api-sentinel"}, nil)
			router := mux.NewRouter()
			srv.RegisterRoutes(router)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/api/cross-border", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("public diagnostics must return safe typed state without authentication; status=%d", w.Code)
			}
			var got CrossBorderResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := "disabled"
			if enabled {
				want = "unavailable"
			}
			if got.Enabled != enabled || got.Status != want {
				t.Fatalf("optional/misconfigured peer must not break the app: %+v", got)
			}
			if strings.Contains(w.Body.String(), "private-") {
				t.Fatal("private configuration appeared in public error")
			}
			publicCfg := httptest.NewRecorder()
			srv.handleConfigClient(publicCfg, httptest.NewRequest("GET", "/api/config/client", nil))
			if strings.Contains(publicCfg.Body.String(), "private-") || strings.Contains(publicCfg.Body.String(), "DATABASE_URL") {
				t.Fatal("peer configuration leaked into client config")
			}
		})
	}
}

// Fixtures use the existing backend matrix: run once with SQLite and once
// with CORESCOPE_TEST_BACKEND=postgres against a disposable test database.
func crossBorderFixture(t *testing.T) *DB {
	t.Helper()
	conn, err := openFixtureSQL(postgresTestDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetMaxOpenConns(1)
	identity := testNativeSQL("INTEGER PRIMARY KEY AUTOINCREMENT", "BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY")
	stmts := []string{
		`CREATE TABLE observers (rowid ` + identity + `, id TEXT UNIQUE, name TEXT, iata TEXT, last_seen TEXT, first_seen TEXT, radio TEXT, can_relay INTEGER, can_relay_seen INTEGER, inactive INTEGER)`,
		`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, configured_scope TEXT, configured_scope_at TEXT)`,
		`CREATE TABLE transmissions (id ` + identity + `, hash TEXT UNIQUE, raw_hex TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER, scope_name TEXT, from_pubkey TEXT, decoded_json TEXT)`,
		`CREATE TABLE observations (id ` + identity + `, transmission_id BIGINT, observer_idx BIGINT, timestamp BIGINT, direction TEXT, snr REAL, rssi REAL, raw_hex TEXT)`,
		`CREATE INDEX cross_border_observation_time ON observations(timestamp)`,
		`CREATE INDEX cross_border_observation_tx ON observations(transmission_id, timestamp)`,
	}
	for _, stmt := range stmts {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return &DB{conn: conn, backend: testBackend(t), isV3: true, hasObsRawHex: true, hasScopeName: true, hasConfiguredScope: true}
}

func crossBorderExec(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	if _, err := db.conn.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestCrossBorderSQLBoundedSelectedSourcesAndPeerCandidates(t *testing.T) {
	local, peer := crossBorderFixture(t), crossBorderFixture(t)
	now := time.Unix(1800000000, 0).UTC()
	for _, db := range []*DB{local, peer} {
		crossBorderExec(t, db, `INSERT INTO observers(rowid,id,name,iata,radio,can_relay,can_relay_seen) VALUES (1,'selected','Selected','AAA','SX1262',0,0),(2,'foreign','Foreign','CCC',NULL,0,1)`)
		crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,scope_name,decoded_json) VALUES (1,'candidate','1500aabb',1,5,NULL,'private-message-sentinel'),(2,'unrelated','1500aabb',1,5,NULL,NULL)`)
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES (1,1,$1,'rx','1500aabb'),(1,2,$1,'rx','1500aabb'),(2,1,$1,'rx','1500aabb'),(1,1,$2,'rx','1500aabb')`, now.Unix()-1, now.Unix()-7200)
	}
	cfg := crossBorderTestConfig()
	before, err := local.conn.Query(`SELECT hash FROM transmissions ORDER BY hash`)
	if err != nil {
		t.Fatal(err)
	}
	before.Close()
	got, err := local.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, cfg, []string{"candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || len(got.Observations) != 1 || got.Observations[0].Hash != "candidate" || got.Observations[0].ObserverID != "selected" {
		t.Fatalf("local reference must use selected IATA, peer hashes, and window in one bounded read: %+v", got)
	}
	if len(got.Observers) != 1 || got.Observers[0].ReportedCanRelay != nil || got.Observers[0].RadioHardwareReportedAt != nil {
		t.Fatalf("source metadata selection/null presence: %+v", got.Observers)
	}
	var localRows, peerRows int
	if err := local.conn.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&localRows); err != nil {
		t.Fatal(err)
	}
	if err := peer.conn.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&peerRows); err != nil {
		t.Fatal(err)
	}
	if localRows != 4 || peerRows != 4 {
		t.Fatal("diagnostics mutated a source database")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := local.readCrossBorder(ctx, []string{"AAA"}, now.Add(-time.Hour), now, cfg, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("SQL read ignored cancellation: %v", err)
	}
}

func TestCrossBorderSQLCapsAndNulls(t *testing.T) {
	db := crossBorderFixture(t)
	now := time.Unix(1800000000, 0).UTC()
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES (1,'one','BBB'),(2,'two','BBB')`)
	crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type) VALUES (1,'a','1500aabb',1,5)`)
	for i := 0; i < 3; i++ {
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp) VALUES (1,1,$1)`, now.Unix()-int64(i+1))
	}
	cfg := crossBorderTestConfig()
	cfg.ObservationLimit, cfg.MetadataLimit = 2, 1
	got, err := db.readCrossBorder(context.Background(), []string{"BBB"}, now.Add(-time.Hour), now, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observations) != 2 || !got.ObservationsTruncated || len(got.Observers) != 1 || !got.ObserversTruncated {
		t.Fatalf("cap-plus-one must expose truncated rows and bound memory: %+v", got)
	}
	if got.Observations[0].SNR != nil || got.Observations[0].Direction != "" {
		t.Fatal("SQL NULL measurement/direction was invented")
	}
}

func TestCrossBorderHeaderBoundariesAndScopeKinds(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, tc := range []struct {
		name, raw, kind string
		width           int
		invalid         bool
	}{
		{"named-zero-hop-two-byte", "140102000040ff", "named", 2, false},
		{"transport-zero", "140000000000ff", "unscoped", 1, false},
		{"reserved-code", "14ffff000000ff", "unknown", 1, false},
		{"plain-flood", "1500ff", "unscoped", 1, false},
		{"three-byte", "1581aaaaaaff", "unscoped", 3, false},
		{"direct-zero-hop", "1600ff", "unscoped", 0, false},
		{"trace-not-hops", "2541aaaaff", "unscoped", 0, false},
		{"reserved-width", "15c0ff", "", 0, true},
		{"truncated-path", "1542aaaaff", "", 0, true},
		{"malformed-hex", "15zzff", "", 0, true},
		{"missing-observation-wire", "", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB"}}, Observations: []crossBorderObservationRow{{Hash: tc.name, ObserverID: "peer", Timestamp: now.Unix() - 1,
				PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood, ScopeName: crossBorderString("#example"), RawHex: tc.raw, CanonicalRawHex: tc.raw}}}
			got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalid {
				if got.Summary.PeerPackets != 0 || got.Sources.Peer.ExcludedInvalidObservations != 1 {
					t.Fatalf("malformed wire cannot become verified overlap: %+v", got.Sources.Peer)
				}
				return
			}
			if len(got.Packets) != 1 || len(got.Packets[0].PeerObservations) != 1 {
				t.Fatalf("valid observed header was discarded: %+v", got.Summary)
			}
			o := got.Packets[0].PeerObservations[0]
			if o.Scope.Kind != tc.kind {
				t.Errorf("scope=%+v, want kind=%s", o.Scope, tc.kind)
			}
			if tc.width == 0 && o.HopWidth != nil {
				t.Errorf("hop width unsupported, got %v", *o.HopWidth)
			}
			if tc.width > 0 && (o.HopWidth == nil || *o.HopWidth != tc.width) {
				t.Errorf("observed hop width=%v, want %d", o.HopWidth, tc.width)
			}
		})
	}
}

func TestCrossBorderTransportZeroDoesNotEstablishWildcardEvidence(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "peer", IATA: "BBB"}},
		Nodes: []crossBorderNode{{PublicKey: strings.Repeat("a", 64), Role: "repeater", ConfiguredScope: crossBorderString("")}},
		Observations: []crossBorderObservationRow{{Hash: "transport-zero", ObserverID: "peer", Direction: "rx", Timestamp: now.Unix() - 1, PayloadType: PayloadGRP_TXT, RouteType: RouteTransportFlood,
			RawHex: "140000000041aaaaff", CanonicalRawHex: "140000000041aaaaff"}}}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{Available: true}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PeerRepeaters) != 1 {
		t.Fatal("expected supported flood-family forwarding row")
	}
	r := got.PeerRepeaters[0]
	if r.ObservedUnscoped || r.DeclaredScopes == nil || len(r.DeclaredScopes.Names) != 0 || r.DeclaredScopes.AllowsUnscoped {
		t.Fatalf("transport-zero does not contradict a reported empty wildcard policy: %+v", r)
	}
}

func TestCrossBorderCacheCoalescesAndBoundsKeys(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.CacheEntries = 3
	var loads atomic.Int64
	svc := &crossBorderService{cfg: cfg, load: func(ctx context.Context, filters CrossBorderFilters) (CrossBorderResponse, error) {
		loads.Add(1)
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return CrossBorderResponse{}, ctx.Err()
		}
		return CrossBorderResponse{Status: "ready", Filters: filters}, nil
	}}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := svc.response(context.Background(), crossBorderTestFilters())
			if err != nil || got.Status != "ready" {
				t.Errorf("shared result status=%s err=%v", got.Status, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("12 concurrent same-query requests must run one bounded build, got %d", got)
	}
	for i := 0; i < 8; i++ {
		filters := crossBorderTestFilters()
		filters.Scope = fmt.Sprintf("scope-%d", i)
		got, err := svc.response(context.Background(), filters)
		if err != nil || got.Filters.Scope != filters.Scope {
			t.Fatalf("wrong cached selection: %+v, %v", got.Filters, err)
		}
	}
	if len(svc.cache) > cfg.CacheEntries {
		t.Fatalf("unbounded cache: %d entries, cap %d", len(svc.cache), cfg.CacheEntries)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.response(ctx, crossBorderTestFilters()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter must leave immediately: %v", err)
	}
}

func TestCrossBorderConcurrentDistinctBuildsAreBounded(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.MaxConcurrentBuilds = 2
	started := make(chan string, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var active, loads atomic.Int64
	svc := &crossBorderService{cfg: cfg, load: func(ctx context.Context, f CrossBorderFilters) (CrossBorderResponse, error) {
		loads.Add(1)
		if n := active.Add(1); n > 2 {
			t.Errorf("active distinct reports=%d, cap=2", n)
		}
		defer active.Add(-1)
		started <- f.Scope
		select {
		case <-release:
			return CrossBorderResponse{Status: "ready", Filters: f}, nil
		case <-ctx.Done():
			return CrossBorderResponse{}, ctx.Err()
		}
	}}
	results := make(chan error, 2)
	for _, scope := range []string{"first", "second"} {
		f := crossBorderTestFilters()
		f.Scope = scope
		go func(f CrossBorderFilters) { _, err := svc.response(context.Background(), f); results <- err }(f)
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("expected one admitted report build; none started")
		}
	}
	third := crossBorderTestFilters()
	third.Scope = "third"
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := svc.response(ctx, third); !errors.Is(err, errCrossBorderBusy) {
		t.Fatalf("distinct request over cap must return busy without queueing: %v", err)
	}
	first := crossBorderTestFilters()
	first.Scope = "first"
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() { _, err := svc.response(waitCtx, first); waitResult <- err }()
	cancelWait()
	select {
	case err := <-waitResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("same-key waiter cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter remained queued")
	}
	if active.Load() != 2 || loads.Load() != 2 {
		t.Fatalf("same-key waiter canceled or duplicated the shared report: active=%d loads=%d", active.Load(), loads.Load())
	}
	unblock()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got, err := svc.response(context.Background(), third); err != nil || got.Filters.Scope != "third" {
		t.Fatalf("released capacity not reusable: %+v %v", got.Filters, err)
	}
}
