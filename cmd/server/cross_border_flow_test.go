package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Decode the actual dashboard wire response so the initial regression fails
// on missing behavior, rather than needing an as-yet-unimplemented Go type.
type flowWireTest struct {
	Flow *struct {
		Status              string `json:"status"`
		IdentityComplete    bool   `json:"identityComplete"`
		EligibleContents    int    `json:"eligibleContents"`
		UnresolvedContents  int    `json:"unresolvedContents"`
		NonCrossingContents int    `json:"nonCrossingContents"`
		PathsTruncated      bool   `json:"pathsTruncated"`
		Directions          []struct {
			Key               string   `json:"key"`
			Contents          int      `json:"contents"`
			Scoped            int      `json:"scoped"`
			Unscoped          int      `json:"unscoped"`
			TransportWildcard int      `json:"transportWildcard"`
			Mixed             int      `json:"mixed"`
			UnknownScopeName  int      `json:"unknownScopeName"`
			FurthestKm        *float64 `json:"furthestKm"`
			Timeline          []struct {
				At       string `json:"at"`
				Contents *int   `json:"contents"`
				Complete bool   `json:"complete"`
			} `json:"timeline"`
		} `json:"directions"`
		Paths []struct {
			ID              string   `json:"id"`
			Hash            string   `json:"hash"`
			Directions      []string `json:"directions"`
			FirstRecordedAt string   `json:"firstRecordedAt"`
			ScopeKey        string   `json:"scopeKey"`
			AirtimeMs       *float64 `json:"airtimeMs"`
			Hops            []struct {
				Index     int    `json:"index"`
				PublicKey string `json:"publicKey"`
				Country   string `json:"country"`
				Reason    string `json:"reason"`
				Kind      string `json:"kind"`
			} `json:"hops"`
			Crossings []struct {
				FromIndex int    `json:"fromIndex"`
				ToIndex   int    `json:"toIndex"`
				Direction string `json:"direction"`
			} `json:"crossings"`
		} `json:"paths"`
		Repeaters []struct {
			PublicKey string `json:"publicKey"`
			Contents  int    `json:"contents"`
		} `json:"repeaters"`
	} `json:"flow"`
}

func flowFixture() (crossBorderSnapshot, crossBorderSnapshot, time.Time) {
	now := time.Unix(1800000000, 0).UTC()
	local, peer := dashboardSnapshot("local", "AAA"), dashboardSnapshot("peer", "BBB")
	start := crossBorderTime(now.Unix() - 3600)
	local.OldestStoredObservationAt, peer.OldestStoredObservationAt = &start, &start
	for i, p := range []struct {
		prefix   string
		lat, lon float64
	}{
		{"aa1101", 43.65, -79.38}, // Toronto, Canada
		{"bb2202", 42.88, -78.88}, // Buffalo, United States
		{"cc3303", 43.15, -77.61}, // Rochester, United States
	} {
		key := p.prefix + strings.Repeat("f", 58)
		n := crossBorderNode{PublicKey: key, Name: p.prefix, Role: "repeater", Lat: &p.lat, Lon: &p.lon}
		id := crossBorderNodeIdentity{PublicKey: key, Role: "repeater"}
		if i == 0 {
			local.Nodes = append(local.Nodes, n)
			local.DashboardNodes = append(local.DashboardNodes, id)
		} else {
			peer.Nodes = append(peer.Nodes, n)
			peer.DashboardNodes = append(peer.DashboardNodes, id)
		}
	}
	return local, peer, now
}

func readFlow(t *testing.T, cfg CrossBorderConfig, f CrossBorderFilters, local, peer crossBorderSnapshot, now time.Time) (flowWireTest, string) {
	t.Helper()
	d, err := buildCrossBorderDashboard(context.Background(), cfg, f, local, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var got flowWireTest
	if err = json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Flow == nil {
		t.Fatal("dashboard omits geographic crossing evidence")
	}
	if len(got.Flow.Directions) != 2 || got.Flow.Directions[0].Key != "ca_us" || got.Flow.Directions[1].Key != "us_ca" {
		t.Fatalf("wrong direction contract: %+v", got.Flow)
	}
	return got, string(b)
}

func TestCrossBorderFlowDirectionsDeduplicateFeedsAndKeepScopeDenominator(t *testing.T) {
	local, peer, now := flowFixture()
	scoped := dashboardRecord("out-scoped", "peer", "BBB", now.Unix()-30, PayloadGRP_TXT, RouteTransportFlood, 3, "aa1101", "bb2202", "cc3303")
	scoped.ScopeName = crossBorderString("onny")
	unknown := dashboardRecord("out-unknown", "peer", "BBB", now.Unix()-20, PayloadGRP_TXT, RouteTransportFlood, 3, "aa1101", "bb2202")
	unscoped := dashboardRecord("out-unscoped", "peer", "BBB", now.Unix()-10, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202")
	returning := dashboardRecord("returning", "peer", "BBB", now.Unix()-5, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202", "aa1101")
	peer.Observations = []crossBorderObservationRow{scoped, unknown, unscoped, returning, scoped}
	duplicate := scoped
	duplicate.ObserverID = "local"
	duplicate.ObserverIATA = "AAA"
	local.Observations = []crossBorderObservationRow{duplicate}
	got, _ := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	out, in := got.Flow.Directions[0], got.Flow.Directions[1]
	if out.Contents != 4 || out.Scoped != 2 || out.Unscoped != 2 || out.UnknownScopeName != 1 || out.Mixed != 0 || in.Contents != 1 || in.Unscoped != 1 {
		t.Fatalf("wrong dedup/scoping/direction cohorts: %+v", got.Flow.Directions)
	}
	if got.Flow.EligibleContents != 4 || got.Flow.UnresolvedContents != 0 || len(got.Flow.Repeaters) != 3 {
		t.Fatalf("wrong cohort/repeater evidence: %+v", got.Flow)
	}
	if out.FurthestKm == nil || *out.FurthestKm <= 0 {
		t.Fatal("located crossing has no border reach")
	}
	for _, d := range got.Flow.Directions {
		if d.Scoped+d.Unscoped+d.TransportWildcard+d.Mixed != d.Contents {
			t.Fatal("scope categories do not reconcile")
		}
	}
	for _, p := range got.Flow.Paths {
		if p.AirtimeMs != nil {
			t.Fatal("unknown measured radio settings manufactured airtime")
		}
	}
}

func TestCrossBorderFlowMixedScopeContentDoesNotDoubleCount(t *testing.T) {
	local, peer, now := flowFixture()
	a := dashboardRecord("same-content", "peer", "BBB", now.Unix()-25, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202")
	b := dashboardRecord("same-content", "peer", "BBB", now.Unix()-20, PayloadGRP_TXT, RouteTransportFlood, 3, "aa1101", "bb2202")
	c := dashboardRecord("wildcard", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteTransportFlood, 3, "aa1101", "bb2202")
	c.RawHex = c.RawHex[:2] + "00000000" + c.RawHex[10:]
	c.CanonicalRawHex = c.RawHex
	peer.Observations = []crossBorderObservationRow{a, b, c}
	got, _ := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	d := got.Flow.Directions[0]
	if d.Contents != 2 || d.Mixed != 1 || d.TransportWildcard != 1 || d.Scoped != 0 || d.Unscoped != 0 {
		t.Fatalf("mixed scope evidence fabricated a percentage: %+v", d)
	}
}

func TestCrossBorderFlowUnionCollisionsHiddenNodesAndGaps(t *testing.T) {
	for _, kind := range []string{"collision", "hidden", "incomplete", "one-byte", "gap", "direct", "tx", "unknown-rx", "conflicting-position"} {
		t.Run(kind, func(t *testing.T) {
			local, peer, now := flowFixture()
			row := dashboardRecord("candidate", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202")
			switch kind {
			case "collision":
				local.DashboardNodes = append(local.DashboardNodes, crossBorderNodeIdentity{PublicKey: "bb2202" + strings.Repeat("e", 58), Role: "client", Hidden: true})
			case "hidden":
				local.DashboardNodes[0].Hidden = true
			case "incomplete":
				local.DashboardNodeIndexComplete = false
			case "one-byte":
				row = dashboardRecord("candidate", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteFlood, 1, "aa", "bb")
			case "gap":
				row = dashboardRecord("candidate", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "ffffff", "bb2202")
			case "direct":
				row = dashboardRecord("candidate", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteDirect, 3, "aa1101", "bb2202")
			case "tx":
				row.Direction = "tx"
			case "unknown-rx":
				row.Direction = ""
			case "conflicting-position":
				n := local.Nodes[0]
				n.Lat = crossBorderFloat(42.88)
				n.Lon = crossBorderFloat(-78.88)
				peer.Nodes = append(peer.Nodes, n)
			}
			peer.Observations = []crossBorderObservationRow{row}
			got, body := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
			if got.Flow.Directions[0].Contents != 0 || got.Flow.Directions[1].Contents != 0 || len(got.Flow.Paths) != 0 {
				t.Fatalf("%s manufactured geographic crossing: %+v", kind, got.Flow)
			}
			if kind == "hidden" && strings.Contains(body, local.DashboardNodes[0].PublicKey) {
				t.Fatal("hidden identity leaked")
			}
		})
	}
}

func TestCrossBorderFlowOneKnownLegDoesNotJoinAcrossUnknownHop(t *testing.T) {
	local, peer, now := flowFixture()
	peer.Observations = []crossBorderObservationRow{dashboardRecord("partial", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202", "ffffff", "aa1101")}
	got, _ := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if got.Flow.Directions[0].Contents != 1 || got.Flow.Directions[1].Contents != 0 || len(got.Flow.Paths) != 1 {
		t.Fatalf("gap contaminated known leg: %+v", got.Flow)
	}
	p := got.Flow.Paths[0]
	if len(p.Hops) < 4 || p.Hops[2].PublicKey != "" || p.Hops[2].Reason == "" || len(p.Crossings) != 1 || p.Crossings[0].FromIndex != 0 || p.Crossings[0].ToIndex != 1 {
		t.Fatalf("unresolved gap was removed/spliced: %+v", p)
	}
}

func TestCrossBorderFlowRXReceiverCanCompleteOneHopCrossing(t *testing.T) {
	local, peer, now := flowFixture()
	key := peer.DashboardNodes[0].PublicKey
	peer.Observers[0].ID = key
	peer.Observers[0].Location = crossBorderLocation(peer.Nodes[0].Lat, peer.Nodes[0].Lon)
	peer.Observations = []crossBorderObservationRow{dashboardRecord("last-leg", key, "BBB", now.Unix()-15, PayloadGRP_TXT, RouteFlood, 3, "aa1101")}
	got, _ := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if got.Flow.Directions[0].Contents != 1 || len(got.Flow.Paths) != 1 || len(got.Flow.Paths[0].Hops) != 2 || got.Flow.Paths[0].Hops[1].Kind != "receiver" {
		t.Fatalf("explicit RX endpoint lost: %+v", got.Flow)
	}
	if len(got.Flow.Repeaters) != 1 || got.Flow.Repeaters[0].PublicKey != local.DashboardNodes[0].PublicKey {
		t.Fatal("receiver was claimed to have forwarded")
	}
}

func TestCrossBorderFlowBoundsFiltersAndChartCoverage(t *testing.T) {
	local, peer, now := flowFixture()
	peer.ObservationsTruncated = true
	cutoff := now.Unix() - 30
	peer.DashboardScanCutoff = &cutoff
	for i := 0; i < 8; i++ {
		row := dashboardRecord(strings.Repeat("x", i+1), "peer", "BBB", cutoff+int64(i), PayloadGRP_TXT, RouteTransportFlood, 3, "aa1101", "bb2202")
		row.ScopeName = crossBorderString("onny")
		peer.Observations = append(peer.Observations, row)
	}
	cfg := crossBorderTestConfig()
	cfg.PacketLimit = 3
	got, _ := readFlow(t, cfg, crossBorderTestFilters(), local, peer, now)
	if got.Flow.Directions[0].Contents != 8 || len(got.Flow.Paths) != 3 || !got.Flow.PathsTruncated || got.Flow.Status != "partial" {
		t.Fatalf("display cap changed summary: %+v", got.Flow)
	}
	if got.Flow.Directions[0].Timeline[0].Contents != nil {
		t.Fatal("capped history became zero")
	}
	f := crossBorderTestFilters()
	f.Scope = "unscoped"
	got, _ = readFlow(t, cfg, f, local, peer, now)
	if got.Flow.Directions[0].Contents != 0 || len(got.Flow.Paths) != 0 {
		t.Fatal("flow ignores scope filter")
	}
}

func TestCrossBorderFlowSameCountryPathsAreNotMissingGeography(t *testing.T) {
	local, peer, now := flowFixture()
	peer.Observations = []crossBorderObservationRow{
		dashboardRecord("same-country", "peer", "BBB", now.Unix()-20, PayloadGRP_TXT, RouteFlood, 3, "bb2202", "cc3303"),
		dashboardRecord("missing-hop", "peer", "BBB", now.Unix()-15, PayloadGRP_TXT, RouteFlood, 3, "bb2202", "ffffff"),
	}
	got, _ := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if got.Flow.EligibleContents != 2 || got.Flow.NonCrossingContents != 1 || got.Flow.UnresolvedContents != 1 {
		t.Fatalf("domestic observed path treated as missing geography: %+v", got.Flow)
	}
}
