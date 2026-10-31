package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkCrossBorderFlow50000PerSource(b *testing.B) {
	now := time.Unix(1800000000, 0).UTC()
	local, peer := dashboardSnapshot("local", "AAA"), dashboardSnapshot("peer", "BBB")
	start := crossBorderTime(now.Unix() - 3600)
	local.OldestStoredObservationAt = &start
	peer.OldestStoredObservationAt = &start
	for i := 0; i < 500; i++ {
		for source, s := range []*crossBorderSnapshot{&local, &peer} {
			key := fmt.Sprintf("%06x%058x", i*2+source+1, i)
			// Inland grids avoid an unintended Toronto shoreline point that
			// remained unclassified in the country geometry.
			lat, lon := 43.75+float64(i%10)*0.001, -79.45+float64(i/10)*0.001
			if source == 1 {
				lat, lon = 43.00+float64(i%10)*0.001, -78.80+float64(i/10)*0.001
			}
			s.Nodes = append(s.Nodes, crossBorderNode{PublicKey: key, Name: key[:6], Role: "repeater", Lat: &lat, Lon: &lon})
			s.DashboardNodes = append(s.DashboardNodes, crossBorderNodeIdentity{PublicKey: key, Role: "repeater"})
		}
	}
	for i := 0; i < 50000; i++ {
		for _, s := range []*crossBorderSnapshot{&local, &peer} {
			r := dashboardRecord(fmt.Sprint(i), s.Observers[0].ID, s.Observers[0].IATA, now.Unix()-int64(i%3500), PayloadGRP_TXT, RouteFlood, 3, local.Nodes[i%500].PublicKey[:6], peer.Nodes[i%500].PublicKey[:6])
			s.Observations = append(s.Observations, r)
		}
	}
	// Load immutable cartography separately from the warm report benchmark.
	if !crossBorderGeo().valid {
		b.Fatal("cartography unavailable")
	}
	for source, snapshot := range []crossBorderSnapshot{local, peer} {
		want := []string{"CA", "US"}[source]
		wrong := 0
		firstBad := ""
		for _, node := range snapshot.Nodes {
			got := crossBorderCountry(crossBorderLocation(node.Lat, node.Lon))
			if got != want {
				wrong++
				if firstBad == "" {
					firstBad = fmt.Sprintf("lat=%f lon=%f country=%q", *node.Lat, *node.Lon, got)
				}
			}
		}
		if wrong > 0 {
			b.Fatalf("fixture %s has%d misclassified/unlocated positions; first %s", want, wrong, firstBad)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
		if err != nil || got.Flow == nil || got.Flow.EligibleContents != 50000 || got.Flow.UnresolvedContents != 0 || got.Flow.NonCrossingContents != 0 || got.Flow.Directions[0].Contents != 50000 || got.Flow.Directions[1].Contents != 0 || len(got.Flow.Paths) != 100 || !got.Flow.PathsTruncated {
			if got != nil && got.Flow != nil {
				b.Fatalf("large-input evidence/cap mismatch: error=%v eligible=%d unresolved=%d noncrossing=%d ca_us=%d us_ca=%d paths=%d", err, got.Flow.EligibleContents, got.Flow.UnresolvedContents, got.Flow.NonCrossingContents, got.Flow.Directions[0].Contents, got.Flow.Directions[1].Contents, len(got.Flow.Paths))
			}
			b.Fatalf("large-input dashboard missing: %v", err)
		}
	}
}

func BenchmarkCrossBorderGeographyCold(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		g := parseCrossBorderGeography(crossBorderCountriesJSON, crossBorderBoundaryJSON)
		if !g.valid {
			b.Fatal("cartography unavailable")
		}
	}
}

func BenchmarkCrossBorderScopeMatchBound(b *testing.B) {
	names := []string{"onny"}
	for i := 0; i < 127; i++ {
		names = append(names, fmt.Sprintf("candidate-%d", i))
	}
	var rows []crossBorderObservationRow
	for i := 0; i < scopeVerifyMaxWindowPackets; i++ {
		payload := []byte(fmt.Sprintf("%0184d", i))
		raw := fmt.Sprintf("%02x", PayloadGRP_TXT<<2|RouteTransportFlood) + regionCode("onny", PayloadGRP_TXT, payload) + "000040" + fmt.Sprintf("%x", payload)
		rows = append(rows, crossBorderObservationRow{Hash: fmt.Sprint(i), RawHex: raw, RawHexLength: len(raw)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := crossBorderSnapshot{Observations: append([]crossBorderObservationRow{}, rows...)}
		stats, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, names)
		if err != nil || stats.Checked != scopeVerifyMaxWindowPackets || stats.Limited {
			b.Fatalf("scope budget mismatch: %+v %v", stats, err)
		}
	}
}
