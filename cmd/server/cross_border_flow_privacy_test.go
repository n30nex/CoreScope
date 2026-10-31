package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCrossBorderFlowHiddenObserverOnlyIdentityCannotReappearFromPeerNode(t *testing.T) {
	cfg := crossBorderTestConfig()
	srv, local, peer := crossBorderReviewService(t, cfg)
	srv.cfg.SetHiddenNamePrefixes([]string{"Hidden"})
	a := "aa1101" + strings.Repeat("f", 58)
	b := "bb2202" + strings.Repeat("f", 58)
	now := time.Now().Unix() - 10
	// The hidden identity is outside the selected local region and has no
	// local node row. The complete hidden-observer query is its only guard.
	crossBorderExec(t, local, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'local','Visible receiver','AAA'),(2,$1,'Hidden observer only','ZZZ')`, a)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Visible receiver','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO nodes(public_key,name,role,lat,lon,last_seen,configured_scope) VALUES($1,'Public alias of hidden observer','repeater',43.65,-79.38,$2,'sensitive-scope'),($3,'Visible US repeater','repeater',42.88,-78.88,$2,NULL)`, a, crossBorderTime(now), b)
	crossBorderRequireFixtureNodes(t, peer, a, b)
	raw := "1582aa1101bb2202ff"
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'private-alias-path',$1,1,5,$2)`, raw, crossBorderTime(now))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx',$2)`, now, raw)
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var got CrossBorderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Dashboard == nil || got.Dashboard.Flow == nil {
		t.Fatalf("no dashboard: status=%d error=%v", w.Code, err)
	}
	for _, private := range []string{a, "Hidden observer only", "Public alias of hidden observer", "sensitive-scope"} {
		if strings.Contains(w.Body.String(), private) {
			t.Errorf("cross-source private identity/metadata disclosed: %q", private)
		}
	}
	if got.Dashboard.Flow.Directions[0].Contents != 0 || len(got.Dashboard.Flow.Paths) != 0 {
		t.Fatal("hidden observer alias manufactured crossing attribution")
	}
}

func TestCrossBorderFlowHiddenObserverOnlyPrefixPreventsFalseUniqueHop(t *testing.T) {
	local, peer, now := flowFixture()
	hidden := "bb2202" + strings.Repeat("e", 58)
	local.DashboardHiddenIDs = map[string]bool{hidden: true}
	peer.Observations = []crossBorderObservationRow{dashboardRecord("observer-collision", "peer", "BBB", now.Unix()-10, PayloadGRP_TXT, RouteFlood, 3, "aa1101", "bb2202")}
	got, body := readFlow(t, crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if got.Flow.Directions[0].Contents != 0 || len(got.Flow.Paths) != 0 {
		t.Fatal("observer-only hidden collision was ignored")
	}
	if strings.Contains(body, hidden) {
		t.Fatal("hidden observer collision candidate disclosed")
	}
}

func TestCrossBorderFlowMissingPrivacySourceDoesNotPublishPeerAliases(t *testing.T) {
	srv, _, peer := crossBorderReviewService(t, crossBorderTestConfig())
	srv.cfg.SetHiddenNamePrefixes([]string{"Hidden"})
	now := time.Now().Unix() - 10
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Peer alias requiring local check','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'content','1500ff',1,5,$1)`, crossBorderTime(now))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff')`, now)
	srv.crossBorder.local = nil
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var got CrossBorderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "unavailable" || got.Dashboard != nil || strings.Contains(w.Body.String(), "Peer alias requiring local check") {
		t.Fatal("unavailable privacy source published unchecked peer aliases")
	}
}
