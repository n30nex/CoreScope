package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func dashboardRecord(hash, observer, iata string, at int64, payload, route, width int, hops ...string) crossBorderObservationRow {
	raw := fmt.Sprintf("%02x", payload<<2|route)
	if route == RouteTransportFlood || route == RouteTransportDirect {
		raw += "01020000"
	}
	raw += fmt.Sprintf("%02x", (width-1)<<6|len(hops)) + strings.Join(hops, "") + "ff"
	return crossBorderObservationRow{Hash: hash, ObserverID: observer, ObserverIATA: iata, Direction: "rx", Timestamp: at,
		PayloadType: payload, RouteType: route, RawHex: raw, CanonicalRawHex: raw}
}

func dashboardSnapshot(observer, iata string) crossBorderSnapshot {
	return crossBorderSnapshot{Available: true, SharedIdentityChecked: true, SharedIdentityComplete: true, DashboardNodeIndexComplete: true,
		Observers: []CrossBorderObserver{{ID: observer, Name: observer, IATA: iata}}}
}

func TestCrossBorderDashboardViewIsExplicitAndCacheSeparated(t *testing.T) {
	f, err := parseCrossBorderFilters(url.Values{"view": {"dashboard"}}, crossBorderTestConfig())
	if err != nil || f.View != "dashboard" {
		t.Fatalf("dashboard view not retained: %+v %v", f, err)
	}
	for _, v := range [][]string{{"unknown"}, {"dashboard", "dashboard"}} {
		if _, err := parseCrossBorderFilters(url.Values{"view": v}, crossBorderTestConfig()); err == nil {
			t.Fatal("unsupported/duplicate dashboard view accepted")
		}
	}
	legacy, _ := parseCrossBorderFilters(url.Values{}, crossBorderTestConfig())
	a, _ := json.Marshal(legacy)
	b, _ := json.Marshal(f)
	if string(a) == string(b) || strings.Contains(string(a), "view") {
		t.Fatal("new view must separate cache keys without changing legacy filter JSON")
	}
}

func TestCrossBorderDashboardIndependentSourcesAndScopedComparison(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	local, peer := dashboardSnapshot("local", "AAA"), dashboardSnapshot("peer", "BBB")
	local.Observations = []crossBorderObservationRow{
		dashboardRecord("shared", "local", "AAA", now.Unix()-20, PayloadGRP_TXT, RouteFlood, 2),
		dashboardRecord("local-only", "local", "AAA", now.Unix()-10, PayloadGRP_TXT, RouteFlood, 1),
	}
	peer.Observations = []crossBorderObservationRow{dashboardRecord("shared", "peer", "BBB", now.Unix()-25, PayloadGRP_TXT, RouteFlood, 3)}
	got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Local.Contents != 2 || got.Peer.Contents != 1 || got.SharedContents != 1 || got.IndependentSharedContents != 1 {
		t.Fatalf("independent local population or shared identity was lost: %+v", got)
	}
	if len(got.RegionPairs) != 1 || got.RegionPairs[0] != (CrossBorderRegionPair{LocalIATA: "AAA", PeerIATA: "BBB", Contents: 1}) {
		t.Fatalf("wrong region overlap: %+v", got.RegionPairs)
	}
	for _, s := range []*crossBorderSnapshot{&local, &peer} {
		row := dashboardRecord("named", s.Observers[0].ID, s.Observers[0].IATA, now.Unix()-1, PayloadGRP_TXT, RouteTransportFlood, 2)
		row.ScopeName = crossBorderString("example")
		s.Observations = append(s.Observations, row)
	}
	f := crossBorderTestFilters()
	f.Scope = "example"
	got, err = buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), f, local, peer, now)
	if err != nil || got.Local.Records != 1 || got.Peer.Records != 1 || got.SharedContents != 1 {
		t.Fatalf("scope must select both independent dashboard populations: %+v %v", got, err)
	}
}

func TestCrossBorderDashboardOwnAdvertAndAllHopCarryingAreSeparate(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	a, b := "aabb11"+strings.Repeat("a", 58), "bbcc22"+strings.Repeat("b", 58)
	collision := "aabb33" + strings.Repeat("c", 58)
	peer := dashboardSnapshot("peer", "BBB")
	peer.DashboardNodes = []crossBorderNodeIdentity{{PublicKey: a, Role: "repeater"}, {PublicKey: b, Role: "repeater"}, {PublicKey: collision, Role: "client", Hidden: true}}
	peer.Nodes = []crossBorderNode{{PublicKey: a, Name: "Alpha", Role: "repeater"}, {PublicKey: b, Name: "Beta", Role: "repeater"}}
	advert := dashboardRecord("advert", "peer", "BBB", now.Unix()-10, PayloadADVERT, RouteFlood, 2)
	advert.FromPubkey = a
	peer.Observations = []crossBorderObservationRow{advert,
		dashboardRecord("three", "peer", "BBB", now.Unix()-8, PayloadGRP_TXT, RouteFlood, 3, "aabb11", "bbcc22"),
		dashboardRecord("three", "peer", "BBB", now.Unix()-7, PayloadGRP_TXT, RouteFlood, 3, "aabb11", "bbcc22"),
		dashboardRecord("collision", "peer", "BBB", now.Unix()-6, PayloadGRP_TXT, RouteFlood, 2, "aabb"),
		dashboardRecord("one", "peer", "BBB", now.Unix()-5, PayloadGRP_TXT, RouteFlood, 1, "aa"),
		dashboardRecord("direct", "peer", "BBB", now.Unix()-4, PayloadGRP_TXT, RouteDirect, 3, "bbcc22"),
		dashboardRecord("trace", "peer", "BBB", now.Unix()-3, PayloadTRACE, RouteFlood, 1, "aa"),
	}
	got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), dashboardSnapshot("local", "AAA"), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.FloodRecords != 5 || len(got.Peer.Widths) != 3 || got.Peer.Widths[0].Records != 1 || got.Peer.Widths[1].Records != 2 || got.Peer.Widths[2].Records != 2 || got.Peer.Widths[1].Advertisers != 1 {
		t.Fatalf("invalid width population/adoption: %+v", got.Peer)
	}
	byKey := map[string]CrossBorderDashboardRepeater{}
	for _, repeater := range got.Peer.Repeaters {
		byKey[repeater.PublicKey] = repeater
	}
	if len(byKey) != 2 || byKey[a].OwnAdvertWidth == nil || *byKey[a].OwnAdvertWidth != 2 || byKey[a].SampleForwardedPackets != 1 || byKey[b].SampleForwardedPackets != 1 || !byKey[a].CarryingAmbiguous {
		t.Fatalf("earlier-hop evidence, collision privacy, dedup, or own advert attribution failed: %+v", byKey)
	}
	if len(byKey[a].HopWidths) != 1 || byKey[a].HopWidths[0] != 3 {
		t.Fatalf("colliding/one-byte path fabricated carrying: %+v", byKey[a])
	}
	peer.DashboardNodeIndexComplete = false
	got, err = buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), dashboardSnapshot("local", "AAA"), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, repeater := range got.Peer.Repeaters {
		if repeater.SampleForwardedPackets != 0 {
			t.Fatal("capped full index claimed carrying")
		}
	}
}

func TestCrossBorderDashboardCapsAndMissingSourcesLeaveChartGaps(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	peer := dashboardSnapshot("peer", "BBB")
	peer.ObservationsTruncated = true
	peer.Observations = []crossBorderObservationRow{dashboardRecord("recent", "peer", "BBB", now.Unix()-60, PayloadGRP_TXT, RouteFlood, 1)}
	got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Peer.Truncated || got.Peer.CoveredFromAt == nil || len(got.Peer.Timeline) < 2 || got.Peer.Timeline[0].Complete || got.Peer.Timeline[0].Records != nil {
		t.Fatalf("cap fabricated old zero buckets: %+v", got.Peer)
	}
	if got.Local.Status != "unavailable" || len(got.Local.Timeline) == 0 || got.Local.Timeline[0].Records != nil {
		t.Fatalf("missing source became measured zero: %+v", got.Local)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = buildCrossBorderDashboard(ctx, crossBorderTestConfig(), crossBorderTestFilters(), peer, peer, now); err == nil {
		t.Fatal("canceled analysis continued")
	}
}

func TestCrossBorderDashboardRadioUsesCompletedValidCounterIntervals(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	peer := dashboardSnapshot("peer", "BBB")
	peer.DashboardMetricsAvailable = true
	integer := func(v int) *int { return &v }
	add := func(id string, ago, tx, rx int) {
		peer.DashboardMetrics = append(peer.DashboardMetrics, crossBorderMetricRow{Observer: CrossBorderObserver{ID: id, Name: id, IATA: "BBB"}, Sample: rawMetricsSample{Timestamp: crossBorderTime(now.Unix() - int64(ago)), TxAirSecs: integer(tx), RxAirSecs: integer(rx)}})
	}
	add("good", 1200, 100, 200)
	add("good", 900, 130, 260)
	add("good", 600, 145, 290)
	add("reset", 900, 100, 100)
	add("reset", 600, 1, 1)
	add("impossible", 900, 0, 0)
	add("impossible", 600, 400, 400)
	add("gap", 1800, 100, 100)
	add("gap", 600, 110, 110)
	add("good", 0, 900, 900) // nearest-rounded current bucket is not complete
	add("good", -300, 1200, 1200)
	got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), dashboardSnapshot("local", "AAA"), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	r := got.Peer.Radio
	if r.Status != "available" || r.ReportingObservers != 1 || r.TxAirtimePct == nil || math.Abs(*r.TxAirtimePct-7.5) > 0.001 || r.RxAirtimePct == nil || math.Abs(*r.RxAirtimePct-15) > 0.001 || r.ExcludedIntervals < 3 {
		t.Fatalf("bad counter weighting/quality: %+v", r)
	}
	if got.Local.Radio.TxAirtimePct != nil || got.Local.Radio.RxAirtimePct != nil {
		t.Fatal("no metrics became zero airtime")
	}
}

func TestCrossBorderDashboardRealHandlerIncludesLocalOnlyAndHidesMetadata(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.MetadataLimit = 1
	srv, local, peer := crossBorderReviewService(t, cfg)
	srv.cfg.SetHiddenNamePrefixes([]string{"Private"})
	now := time.Now().Unix() - 10
	for i, db := range []*DB{local, peer} {
		region, id := "AAA", "local"
		if i == 1 {
			region, id = "BBB", "peer"
		}
		crossBorderExec(t, db, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,$1,'Visible',$2),(2,$3,'Private observer',$2)`, id, region, "hidden-"+id)
		crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,$1,'1500ff',1,5,$2),(2,'private-content','1500ff',1,5,$2)`, id+"-only", crossBorderTime(now))
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff'),(2,2,$1,'rx','1500ff')`, now)
		crossBorderExec(t, db, `CREATE TABLE IF NOT EXISTS observer_metrics(observer_id TEXT,timestamp TEXT,tx_air_secs BIGINT,rx_air_secs BIGINT,recv_errors BIGINT,packets_sent BIGINT,packets_recv BIGINT)`)
		for _, stamp := range []int64{now - 900, now - 600} {
			crossBorderExec(t, db, `INSERT INTO observer_metrics(observer_id,timestamp,tx_air_secs,rx_air_secs) VALUES($1,$2,100,200)`, "hidden-"+id, crossBorderTime(stamp))
		}
		crossBorderExec(t, db, `INSERT INTO observer_metrics(observer_id,timestamp,tx_air_secs,rx_air_secs) VALUES($1,$2,100,200),($1,$3,130,260),($1,$4,9999,9999)`, id, crossBorderTime(now-1200), crossBorderTime(now-900), crossBorderTime(now+300))
	}
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var got CrossBorderResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Dashboard == nil {
		t.Fatalf("dashboard missing from real handler: status=%d body=%s", w.Code, w.Body.String())
	}
	if got.Dashboard.Local.Contents != 1 || got.Dashboard.Peer.Contents != 1 || got.Dashboard.Local.Records != 1 || got.Dashboard.Peer.Records != 1 {
		t.Fatalf("local-only population or privacy lost: %+v", got.Dashboard)
	}
	if strings.Contains(w.Body.String(), "Private") || strings.Contains(w.Body.String(), "hidden-") || strings.Contains(w.Body.String(), "private-content") {
		t.Fatal("private observer metadata, metrics or content escaped cap-independent filtering")
	}
	for _, source := range []CrossBorderDashboardSource{got.Dashboard.Local, got.Dashboard.Peer} {
		if source.Radio.ReportingObservers != 1 || source.Radio.TxAirtimePct == nil || *source.Radio.TxAirtimePct != 10 || source.Radio.RxAirtimePct == nil || *source.Radio.RxAirtimePct != 20 {
			t.Fatalf("actual SQL metrics leaked hidden/current data or lost valid rows: %+v", source.Radio)
		}
	}
}

func TestCrossBorderDashboardOverviewScopeKeepsOtherScopePacketDetails(t *testing.T) {
	srv, local, peer := crossBorderReviewService(t, crossBorderTestConfig())
	now := time.Now().Unix() - 10
	for side, db := range []*DB{local, peer} {
		id, region, raw := "local", "AAA", "1500ff"
		var scope *string
		if side == 1 {
			id, region, raw = "peer", "BBB", "170102000040ff"
			scope = crossBorderString("example")
		}
		crossBorderExec(t, db, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,$1,'Visible',$2)`, id, region)
		crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,scope_name) VALUES(1,'shared',$1,1,5,$2)`, raw, scope)
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx',$2)`, now, raw)
	}
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard&scope=example", nil))
	var got CrossBorderResponse
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Dashboard == nil {
		t.Fatalf("missing scoped dashboard: %s", w.Body.String())
	}
	if got.Dashboard.Local.Records != 0 || got.Dashboard.Peer.Records != 1 || got.Dashboard.SharedContents != 0 {
		t.Fatalf("overview populations are not both scope-selected: %+v", got.Dashboard)
	}
	if len(got.Packets) != 1 || len(got.Packets[0].LocalObservations) != 1 || got.Packets[0].LocalObservations[0].Scope.Kind != "unscoped" {
		t.Fatalf("inspect evidence lost the differently scoped Canadian copy: %+v", got.Packets)
	}
}

func TestCrossBorderDashboardCappedRowsReleaseBackingArrays(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	peer := dashboardSnapshot("peer", "BBB")
	for i := 1; i <= 3; i++ {
		key := fmt.Sprintf("%04x%060x", i, i)
		peer.DashboardNodes = append(peer.DashboardNodes, crossBorderNodeIdentity{PublicKey: key, Role: "repeater"})
		row := dashboardRecord(fmt.Sprint(i), "peer", "BBB", now.Unix()-1, PayloadADVERT, RouteFlood, 2)
		row.FromPubkey = key
		peer.Observations = append(peer.Observations, row)
	}
	cfg := crossBorderTestConfig()
	cfg.MetadataLimit = 1
	got, err := buildCrossBorderDashboard(context.Background(), cfg, crossBorderTestFilters(), dashboardSnapshot("local", "AAA"), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Peer.Repeaters) != 1 || cap(got.Peer.Repeaters) != 1 || !got.Peer.RepeatersTruncated {
		t.Fatal("cached repeater slice retains discarded rows")
	}
}

func TestCrossBorderDashboardTransportWildcardIsNotOrdinaryUnscoped(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	key := "aabb" + strings.Repeat("a", 60)
	peer := dashboardSnapshot("peer", "BBB")
	peer.DashboardNodes = []crossBorderNodeIdentity{{PublicKey: key, Role: "repeater"}}
	row := dashboardRecord("wildcard", "peer", "BBB", now.Unix()-1, PayloadGRP_TXT, RouteTransportFlood, 2, "aabb")
	row.RawHex = row.RawHex[:2] + "00000000" + row.RawHex[10:]
	row.CanonicalRawHex = row.RawHex
	peer.Observations = []crossBorderObservationRow{row}
	got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), dashboardSnapshot("local", "AAA"), peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Peer.Scopes) != 1 || got.Peer.Scopes[0].Key != "transport-zero" || len(got.Peer.Repeaters) != 1 || !got.Peer.Repeaters[0].ObservedTransportWildcard || got.Peer.Repeaters[0].ObservedUnscoped || len(got.Peer.Repeaters[0].ObservedScopes) != 0 {
		t.Fatalf("transport zero invented wildcard policy evidence: %+v", got.Peer)
	}
}

func TestCrossBorderDashboardSharedIdentityAndBoundedRegionPairs(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	local, peer := dashboardSnapshot("shared", "AAA"), dashboardSnapshot("shared", "BBB")
	local.Observations = []crossBorderObservationRow{dashboardRecord("same", "shared", "AAA", now.Unix()-1, PayloadGRP_TXT, RouteFlood, 2)}
	peer.Observations = []crossBorderObservationRow{dashboardRecord("same", "shared", "BBB", now.Unix()-1, PayloadGRP_TXT, RouteFlood, 2)}
	got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if err != nil || got.SharedContents != 1 || got.IndependentSharedContents != 0 || len(got.RegionPairs) != 0 {
		t.Fatalf("same observer became independent regional link: %+v %v", got, err)
	}
	f := crossBorderTestFilters()
	for c := 'B'; c <= 'R'; c++ {
		f.LocalIATA = append(f.LocalIATA, "AA"+string(c))
		f.PeerIATA = append(f.PeerIATA, "BB"+string(c))
	}
	got, err = buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), f, local, peer, now)
	if err != nil || !got.RegionPairsTruncated || len(got.RegionPairs) > 256 {
		t.Fatal("large selected-region input expanded the matrix without a visible cap")
	}
}

func TestCrossBorderDashboardRealCollisionIndexIncludesHiddenNonDisplayedNodes(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.MetadataLimit = 1
	srv, _, peer := crossBorderReviewService(t, cfg)
	srv.cfg.SetHiddenNamePrefixes([]string{"Private"})
	a := "aabb11" + strings.Repeat("a", 58)
	hidden := "aabb22" + strings.Repeat("b", 58)
	now := time.Now().Unix() - 10
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Visible','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO nodes(public_key,name,role,last_seen) VALUES($1,'Visible repeater','repeater',$2),($3,'Private other candidate','client',$4)`, a, crossBorderTime(now), hidden, crossBorderTime(now-1000))
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,from_pubkey) VALUES(1,'advert','1140ff',1,4,$1),(2,'collision','1541aabbff',1,5,NULL)`, a)
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1140ff'),(2,1,$1,'rx','1541aabbff')`, now)
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var got CrossBorderResponse
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Dashboard == nil {
		t.Fatalf("no dashboard: %s", w.Body.String())
	}
	repeaters := got.Dashboard.Peer.Repeaters
	if len(repeaters) != 1 || repeaters[0].SampleForwardedPackets != 0 || !repeaters[0].CarryingAmbiguous || repeaters[0].OwnAdvertWidth == nil || *repeaters[0].OwnAdvertWidth != 2 {
		t.Fatalf("metadata cap/hidden client manufactured uniqueness: %+v", repeaters)
	}
	if strings.Contains(w.Body.String(), hidden) || strings.Contains(w.Body.String(), "Private other") {
		t.Fatal("hidden collision candidate was disclosed")
	}
	for _, repeater := range got.PeerRepeaters {
		if repeater.SampleForwardedPackets != 0 {
			t.Fatal("packet-detail envelope reintroduced metadata-only false attribution")
		}
	}
}

func TestCrossBorderDashboardSQLNodeIndexCapFailsClosed(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.MetadataLimit = 1
	srv, _, peer := crossBorderReviewService(t, cfg)
	now := time.Now().Unix() - 10
	a := "aabb" + strings.Repeat("a", 60)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,iata) VALUES(1,'peer','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO nodes(public_key,name,role) VALUES($1,'Visible','repeater')`, a)
	fill := testNativeSQL(`WITH RECURSIVE n(i) AS(VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<50000) INSERT INTO nodes(public_key,role) SELECT 'bbbb'||printf('%060x',i),'client' FROM n`, `INSERT INTO nodes(public_key,role) SELECT 'bbbb'||lpad(to_hex(i::bigint),60,'0'),'client' FROM generate_series(1,50000) i`)
	crossBorderExec(t, peer, fill)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,from_pubkey) VALUES(1,'advert','1140ff',1,4,$1),(2,'forward','1541aabbff',1,5,NULL)`, a)
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1140ff'),(2,1,$1,'rx','1541aabbff')`, now)
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var got CrossBorderResponse
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Dashboard == nil {
		t.Fatalf("missing bounded index report: %s", w.Body.String())
	}
	if got.Dashboard.Peer.NodeIndexComplete || len(got.Dashboard.Peer.Repeaters) != 1 || got.Dashboard.Peer.Repeaters[0].SampleForwardedPackets != 0 || got.Dashboard.Peer.Repeaters[0].OwnAdvertWidth == nil || *got.Dashboard.Peer.Repeaters[0].OwnAdvertWidth != 2 {
		t.Fatalf("index cap was not separated from full-origin advert evidence: %+v", got.Dashboard.Peer)
	}
}

func BenchmarkCrossBorderDashboard50000PerSource(b *testing.B) {
	now := time.Unix(1800000000, 0).UTC()
	local, peer := dashboardSnapshot("local", "AAA"), dashboardSnapshot("peer", "BBB")
	for _, snapshot := range []*crossBorderSnapshot{&local, &peer} {
		start := crossBorderTime(now.Unix() - 3600)
		snapshot.OldestStoredObservationAt = &start
		for i := 1; i <= 64; i++ {
			key := fmt.Sprintf("%04x%060x", i, i)
			snapshot.DashboardNodes = append(snapshot.DashboardNodes, crossBorderNodeIdentity{PublicKey: key, Role: "repeater"})
		}
		id, region := snapshot.Observers[0].ID, snapshot.Observers[0].IATA
		for i := 0; i < 50000; i++ {
			snapshot.Observations = append(snapshot.Observations, dashboardRecord(fmt.Sprint(i), id, region, now.Unix()-int64(i%3590), PayloadGRP_TXT, RouteFlood, 2, fmt.Sprintf("%04x", i%64+1), fmt.Sprintf("%04x", (i+1)%64+1)))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
		if err != nil || got.Local.Records != 50000 || got.Peer.Records != 50000 || got.IndependentSharedContents != 50000 {
			b.Fatalf("bounded large input failed: %+v %v", got, err)
		}
	}
}
