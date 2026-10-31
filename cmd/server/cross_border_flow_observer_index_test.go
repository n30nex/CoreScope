package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCrossBorderFlowUnselectedObserverOnlyIdentityBlocksFalseUniqueHop(t *testing.T) {
	srv, local, peer := crossBorderReviewService(t, crossBorderTestConfig())
	a := "aa1101" + strings.Repeat("f", 58)
	b := "bb2202" + strings.Repeat("f", 58)
	collision := "bb2202" + strings.Repeat("e", 58)
	now := time.Now().Unix() - 10
	// This public observer is outside the selected regions and has no node
	// row anywhere. Its known full key must still challenge a shared prefix.
	crossBorderExec(t, local, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'local','Selected receiver','AAA'),(2,$1,'Unselected public observer','ZZZ')`, collision)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Selected receiver','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO nodes(public_key,name,role,lat,lon,last_seen) VALUES($1,'Canadian repeater','repeater',43.65,-79.38,$3),($2,'US repeater','repeater',42.88,-78.88,$3)`, a, b, crossBorderTime(now))
	raw := "1582aa1101bb2202ff"
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'observer-only-collision',$1,1,5,$2)`, raw, crossBorderTime(now))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx',$2)`, now, raw)
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var got CrossBorderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Dashboard == nil || got.Dashboard.Flow == nil {
		t.Fatalf("no dashboard: %d %v", w.Code, err)
	}
	if !got.Dashboard.Flow.IdentityComplete {
		t.Fatal("small complete identity set was needlessly discarded")
	}
	if got.Dashboard.Flow.Directions[0].Contents != 0 || len(got.Dashboard.Flow.Paths) != 0 {
		t.Fatal("unselected observer-only identity was omitted from collision checks")
	}
}

func TestCrossBorderObserverIdentityIndexPreservesKnownRolesAndCaps(t *testing.T) {
	local, peer, now := flowFixture()
	known := local.DashboardNodes[0].PublicKey
	if err := crossBorderAppendObserverIdentities(context.Background(), &peer, map[string]bool{strings.ToUpper(known): true}, true, nil); err != nil {
		t.Fatal(err)
	}
	peer.Observations = []crossBorderObservationRow{dashboardRecord("same-identity", "peer", "BBB", now.Unix()-10, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202")}
	got, _ := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if got.Flow.Directions[0].Contents != 1 {
		t.Fatal("observer-only record replaced a known repeater role")
	}
	if err := crossBorderAppendObserverIdentities(context.Background(), &local, map[string]bool{known: true}, true, nil); err != nil || len(local.DashboardNodes) != 1 || local.DashboardNodes[0].Role != "repeater" {
		t.Fatal("same-source observer replaced node role or duplicated identity")
	}
	limited := crossBorderSnapshot{DashboardNodeIndexComplete: true}
	for i := 0; i < crossBorderDashboardKeys; i++ {
		limited.DashboardNodes = append(limited.DashboardNodes, crossBorderNodeIdentity{PublicKey: fmt.Sprintf("%064x", i+1), Role: "repeater"})
	}
	if err := crossBorderAppendObserverIdentities(context.Background(), &limited, map[string]bool{strings.Repeat("f", 64): true}, true, nil); err != nil || limited.DashboardNodeIndexComplete || len(limited.DashboardNodes) != crossBorderDashboardKeys {
		t.Fatal("combined node/observer identity cap failed open")
	}
	partial := crossBorderSnapshot{DashboardNodeIndexComplete: true}
	if err := crossBorderAppendObserverIdentities(context.Background(), &partial, map[string]bool{known: true}, false, nil); err != nil || partial.DashboardNodeIndexComplete {
		t.Fatal("capped full observer query claimed complete collision evidence")
	}
}
