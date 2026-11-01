package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

func scopeMatchRecord(t *testing.T, hash, name string, payload []byte, hops ...string) crossBorderObservationRow {
	t.Helper()
	r := dashboardRecord(hash, "peer", "BBB", time.Now().Unix(), PayloadGRP_TXT, RouteTransportFlood, 3, hops...)
	r.RawHex = fmt.Sprintf("%02x", PayloadGRP_TXT<<2|RouteTransportFlood) + strings.ToLower(regionCode(name, PayloadGRP_TXT, payload)) + "0000" + fmt.Sprintf("%02x", 128|len(hops)) + strings.Join(hops, "") + hex.EncodeToString(payload)
	r.RawHexLength = len(r.RawHex)
	r.CanonicalRawHex = ""
	return r
}

func TestCrossBorderScopeMatchUsesFullObservationAndPreservesCase(t *testing.T) {
	payload := []byte(strings.Repeat("a", 130))
	a := scopeMatchRecord(t, "same-content", "onny", payload, strings.Repeat("aa", 3), strings.Repeat("bb", 3))
	b := scopeMatchRecord(t, "same-content", "ykf", payload, "aaaaaa", "bbbbbb")
	c := scopeMatchRecord(t, "case-sensitive", "YKF", payload, "aaaaaa", "bbbbbb")
	s := crossBorderSnapshot{Observations: []crossBorderObservationRow{a, b, c, a}}
	stats, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, []string{"onny", "ykf", "onny"})
	if err != nil || stats.Checked != 3 || stats.Matched != 2 || stats.Limited {
		t.Fatalf("wrong bounded resolution stats: %+v %v", stats, err)
	}
	for i, want := range []string{"onny", "ykf", "", "onny"} {
		wire, ok := crossBorderReadWire(s.Observations[i])
		if !ok {
			t.Fatal("fixture invalid")
		}
		if want == "" {
			if wire.scope.Kind != "unknown" {
				t.Fatal("case-insensitive scope alias")
			}
			continue
		}
		if wire.scope.Kind != "named" || wire.scope.Name == nil || *wire.scope.Name != want || wire.scope.Resolution != "catalog_match" {
			t.Fatalf("historical observation scope not recovered: %+v", wire.scope)
		}
		if s.Observations[i].ScopeName != nil || s.Observations[i].CanonicalRawHex != "" {
			t.Fatal("resolver falsified canonical transmission")
		}
	}
}

func TestCrossBorderScopeMatchCollisionsReservedAndTruncatedStayUnknown(t *testing.T) {
	payload := []byte("collision fixture")
	first := map[string]string{}
	var a, b string
	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("scope-%d", i)
		code := regionCode(name, PayloadGRP_TXT, payload)
		if old := first[code]; old != "" {
			a, b = old, name
			break
		}
		first[code] = name
	}
	if a == "" {
		t.Fatal("collision fixture did not find a pair")
	}
	row := scopeMatchRecord(t, "collision", a, payload, "aaaaaa", "bbbbbb")
	zero := row
	zero.Hash = "zero"
	zero.RawHex = zero.RawHex[:2] + "0000" + zero.RawHex[6:]
	reserved := row
	reserved.Hash = "reserved"
	reserved.RawHex = reserved.RawHex[:2] + "ffff" + reserved.RawHex[6:]
	truncated := row
	truncated.Hash = "truncated"
	truncated.RawHex = truncated.RawHex[:len(truncated.RawHex)-2]
	s := crossBorderSnapshot{Observations: []crossBorderObservationRow{row, zero, reserved, truncated}}
	_, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range s.Observations {
		if r.ObservedScope != nil {
			t.Fatalf("ambiguous/reserved/truncated record %d was named", i)
		}
	}
}

func TestCrossBorderScopeMatchBudgetMemoizationAndCancellation(t *testing.T) {
	var s crossBorderSnapshot
	for i := 0; i < 4100; i++ {
		r := scopeMatchRecord(t, fmt.Sprint(i), "onny", []byte(fmt.Sprint(i)), "aaaaaa", "bbbbbb")
		s.Observations = append(s.Observations, r, r)
	}
	stats, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, []string{"onny"})
	if err != nil || stats.Checked != 4096 || stats.Matched != 4096 || !stats.Limited {
		t.Fatalf("candidate bound/memoization failed: %+v %v", stats, err)
	}
	if s.Observations[0].ObservedScope == nil || s.Observations[1].ObservedScope == nil || s.Observations[len(s.Observations)-1].ObservedScope != nil {
		t.Fatal("scope budget incorrectly applied to repeated observations")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolveCrossBorderScopes(ctx, []*crossBorderSnapshot{&s}, []string{"onny"}); err == nil {
		t.Fatal("cancellation ignored")
	}
	tooMany := make([]string, 129)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("candidate-%d", i)
	}
	stats, err = resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, tooMany)
	if err != nil || !stats.Limited {
		t.Fatal("catalog truncation was hidden")
	}
}

func TestCrossBorderScopeMatchReservedNamedFiltersStayDistinct(t *testing.T) {
	for _, name := range []string{"all", "unknown", "unscoped"} {
		scope := CrossBorderScope{Kind: "named", Name: &name}
		if !crossBorderScopeMatches(scope, "named:"+name) || crossBorderScopeMatches(CrossBorderScope{Kind: "unscoped"}, "named:"+name) {
			t.Fatalf("literal scope %q collides with builtin filter", name)
		}
	}
}

func TestCrossBorderScopeMatchCatalogLimitCannotHideCollision(t *testing.T) {
	payload := []byte("collision beyond catalog boundary")
	seen := map[string]string{}
	var first, second string
	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("collision-%d", i)
		code := regionCode(name, PayloadGRP_TXT, payload)
		if old := seen[code]; old != "" {
			first, second = old, name
			break
		}
		seen[code] = name
	}
	if first == "" {
		t.Fatal("fixture did not find collision")
	}
	names := []string{first}
	for i := 0; i < 127; i++ {
		names = append(names, fmt.Sprintf("filler-%d", i))
	}
	names = append(names, second)
	s := crossBorderSnapshot{Observations: []crossBorderObservationRow{scopeMatchRecord(t, "budget-collision", first, payload, "aaaaaa", "bbbbbb")}}
	stats, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, names)
	if err != nil || !stats.Limited || stats.Checked != 0 || s.Observations[0].ObservedScope != nil {
		t.Fatalf("omitted colliding candidate manufactured a scope name: %+v %v", stats, err)
	}
}

func TestCrossBorderScopeMatchMaximumFirmwareFrame(t *testing.T) {
	hops := make([]string, 21)
	for i := range hops {
		hops[i] = "aabbcc"
	}
	row := scopeMatchRecord(t, "maximum", "onny", []byte(strings.Repeat("x", maxPacketPayload)), hops...)
	if len(row.RawHex) != 506 {
		t.Fatalf("fixture size changed: %d", len(row.RawHex))
	}
	s := crossBorderSnapshot{Observations: []crossBorderObservationRow{row}}
	stats, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&s}, []string{"onny"})
	if err != nil || stats.Matched != 1 || s.Observations[0].ObservedScope == nil {
		t.Fatalf("valid maximum3byteframe did not match: %+v %v", stats, err)
	}
}

func TestCrossBorderScopeReadRetainsWholeValidPacket(t *testing.T) {
	cfg := crossBorderTestConfig()
	_, _, peer := crossBorderReviewService(t, cfg)
	now := time.Now().UTC()
	hops := make([]string, 21)
	for i := range hops {
		hops[i] = "aabbcc"
	}
	row := scopeMatchRecord(t, "sql-full-payload", "onny", []byte(strings.Repeat("x", maxPacketPayload)), hops...)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Receiver','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,$1,$2,0,5,$3)`, row.Hash, row.RawHex, crossBorderTime(now.Unix()-10))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx',$2)`, now.Unix()-10, row.RawHex)
	snapshot, err := peer.readCrossBorder(context.Background(), []string{"BBB"}, now.Add(-time.Hour), now, cfg, nil)
	if err != nil || len(snapshot.Observations) != 1 {
		t.Fatalf("source read: %v rows=%d", err, len(snapshot.Observations))
	}
	if snapshot.Observations[0].RawHex != row.RawHex || snapshot.Observations[0].RawHexLength != 506 {
		t.Fatal("SQL cut valid payload before scope verification")
	}
	stats, err := resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&snapshot}, []string{"onny"})
	if err != nil || stats.Matched != 1 {
		t.Fatalf("full stored scope not recognized: %+v %v", stats, err)
	}
}

func TestCrossBorderScopeMatchAllocationBudget(t *testing.T) {
	const packetCount = 64
	names := []string{"onny"}
	for i := 0; i < 31; i++ {
		names = append(names, fmt.Sprintf("candidate-%d", i))
	}
	rows := make([]crossBorderObservationRow, 0, packetCount)
	for i := 0; i < packetCount; i++ {
		rows = append(rows, scopeMatchRecord(t, fmt.Sprint(i), "onny", []byte(fmt.Sprintf("payload-%d", i)), "aaaaaa", "bbbbbb"))
	}
	var stats CrossBorderScopeResolution
	var err error
	var last crossBorderSnapshot
	allocations := testing.AllocsPerRun(3, func() {
		last = crossBorderSnapshot{Observations: append([]crossBorderObservationRow{}, rows...)}
		stats, err = resolveCrossBorderScopes(context.Background(), []*crossBorderSnapshot{&last}, names)
	})
	if err != nil || stats.Checked != packetCount || stats.Limited || last.Observations[0].ObservedScope == nil {
		t.Fatalf("allocation fixture did not exercise scope matching: %+v %v", stats, err)
	}
	// This modest real report used over20k allocations when constructing a
	// fresh keyed MAC for every candidate/packet. Keep that regression bounded.
	if allocations > 12000 {
		t.Fatalf("scope matching allocated %.0f objects for64 packets/32names; budget12000", allocations)
	}
	t.Logf("scope matching allocations=%.0f for64 packets/32names", allocations)
}
