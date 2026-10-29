package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

func crossBorderReviewService(t *testing.T, cfg CrossBorderConfig) (*Server, *DB, *DB) {
	t.Helper()
	local := crossBorderFixture(t)
	peer := (*DB)(nil)
	if testBackend(t) == dbconfig.Postgres {
		peer, _ = crossBorderReadyPostgresFixture(t)
	} else {
		peer = crossBorderFixture(t)
	}
	srv := NewServer(local, &Config{CrossBorder: &cfg}, nil)
	srv.crossBorder.peer = peer
	t.Cleanup(srv.closeCrossBorder)
	return srv, local, peer
}

func crossBorderReviewResponse(t *testing.T, srv *Server) (CrossBorderResponse, string) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border", nil))
	if w.Code != 200 {
		t.Fatalf("handler status=%d", w.Code)
	}
	var got CrossBorderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got, w.Body.String()
}

func TestCrossBorderReviewPrivacyUsesFullIdentityEvidence(t *testing.T) {
	for _, cap := range []int{1, 2, 20} {
		t.Run(map[int]string{1: "observer-capped", 2: "node-capped", 20: "uncapped"}[cap], func(t *testing.T) {
			cfg := crossBorderTestConfig()
			cfg.MetadataLimit = cap
			srv, local, peer := crossBorderReviewService(t, cfg)
			srv.cfg.SetHiddenNamePrefixes([]string{"Hidden"})
			srv.cfg.SetNodeBlacklist([]string{"z-blocked-local", "z-blocked-peer"})
			now := time.Now().Unix() - 10
			privateIDs := []string{}
			for side, db := range []*DB{local, peer} {
				region, suffix, visible, hiddenNode := "AAA", "local", strings.Repeat("b", 64), strings.Repeat("e", 64)
				if side == 1 {
					region, suffix, visible, hiddenNode = "BBB", "peer", strings.Repeat("a", 64), strings.Repeat("f", 64)
				}
				hiddenObserver, blocked := "z-hidden-"+suffix, "z-blocked-"+suffix
				privateIDs = append(privateIDs, hiddenNode, hiddenObserver, blocked)
				for i, item := range []struct{ id, name, radio string }{
					{visible, "Visible observer", "visible-radio"}, {hiddenObserver, "Hidden observer", "private-radio"},
					{hiddenNode, "Ordinary observer", "private-radio"}, {blocked, "Blocked observer", "private-radio"},
				} {
					crossBorderExec(t, db, `INSERT INTO observers(rowid,id,name,iata,radio) VALUES($1,$2,$3,$4,$5)`, i+1, item.id, item.name, region, item.radio)
				}
				// The hidden node loses the newest-node cap despite its observer's ordinary name.
				crossBorderExec(t, db, `INSERT INTO nodes(public_key,name,role,lat,lon,last_seen,configured_scope) VALUES($1,'Visible repeater','repeater',12.5,34.5,$2,'visible-scope')`, visible, crossBorderTime(now-1))
				crossBorderExec(t, db, `INSERT INTO nodes(public_key,name,role,lat,lon,last_seen,configured_scope) VALUES($1,'Hidden repeater','repeater',66.125,77.25,$2,'private-scope')`, hiddenNode, crossBorderTime(now-100))
				crossBorderExec(t, db, `INSERT INTO nodes(public_key,name,role,last_seen) VALUES($1,'Newest unrelated node','client',$2)`, strings.Repeat("0", 64), crossBorderTime(now))
				crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'content','1500ff',1,5,$1)`, crossBorderTime(now))
				crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES
 (1,1,$1,'rx','1500ff'),(1,2,$1,'rx','1500ff'),(1,3,$1,'rx','1500ff'),(1,4,$1,'rx','1500ff')`, now)
			}
			got, body := crossBorderReviewResponse(t, srv)
			for _, secret := range append(privateIDs, "Hidden observer", "Hidden repeater", "private-radio", "private-scope", "66.125", "77.25") {
				if strings.Contains(body, secret) {
					t.Errorf("real handler leaked privacy-protected identity/metadata %q", secret)
				}
			}
			if len(got.Packets) != 1 || got.Packets[0].PeerObservationCount != 1 || got.Packets[0].LocalObservationCount != 1 || got.Packets[0].Overlap != "independent" {
				t.Fatalf("visible evidence was lost or hidden observations survived: %+v", got.Packets)
			}
			if len(got.PeerObservers) != 1 || got.PeerObservers[0].Name != "Visible observer" || got.PeerObservers[0].Location == nil {
				t.Fatalf("visible observer/location missing: %+v", got.PeerObservers)
			}
			if cap == 20 && (len(got.PeerRepeaters) != 1 || got.PeerRepeaters[0].Name != "Visible repeater") {
				t.Fatalf("visible repeater missing or hidden repeater survived: %+v", got.PeerRepeaters)
			}
		})
	}
}

func TestCrossBorderReviewSharedIdentityChecksBothSources(t *testing.T) {
	for _, otherRegion := range []string{"BBB", "CCC"} {
		t.Run(otherRegion, func(t *testing.T) {
			cfg := crossBorderTestConfig()
			if otherRegion == "BBB" {
				cfg.MetadataLimit = 1
			}
			srv, local, peer := crossBorderReviewService(t, cfg)
			crossBorderExec(t, local, `INSERT INTO observers(rowid,id,iata) VALUES(1,'z-local','AAA')`)
			crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,iata) VALUES(1,'a-peer','BBB'),(2,'z-local',$1)`, otherRegion)
			for _, db := range []*DB{local, peer} {
				crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'content','1500ff',1,5,$1)`, crossBorderTime(time.Now().Unix()-5))
				crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff')`, time.Now().Unix()-5)
			}
			got, _ := crossBorderReviewResponse(t, srv)
			if got.Summary.DuplicateObserverIDs != 1 || got.Summary.IndependentSharedPackets != 0 || len(got.Packets) != 1 || got.Packets[0].Overlap != "shared_feed_identity" {
				t.Fatalf("reverse-side full metadata was not checked: summary=%+v packets=%+v", got.Summary, got.Packets)
			}
		})
	}
}

func TestCrossBorderReviewDisjointPacketObserversAreNotSameObserver(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	local := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "A", IATA: "AAA"}, {ID: "B", IATA: "AAA"}},
		Observations: []crossBorderObservationRow{crossBorderObservation("content", "B", "rx", now.Unix()-1)}}
	peer := crossBorderSnapshot{Available: true, Observers: []CrossBorderObserver{{ID: "A", IATA: "BBB"}},
		Observations: []crossBorderObservationRow{crossBorderObservation("content", "A", "rx", now.Unix()-1)}}
	got, err := buildCrossBorder(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), local, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packets) != 1 || got.Packets[0].Overlap != "shared_feed_identity" || got.Summary.SharedObserverOnlyPackets != 0 || got.Summary.IndependentSharedPackets != 0 {
		t.Fatalf("disjoint per-packet IDs became a same-observer or independent match: %+v", got)
	}
}

func TestCrossBorderReviewFutureOnlyIsNotFreshAvailableEvidence(t *testing.T) {
	srv, _, peer := crossBorderReviewService(t, crossBorderTestConfig())
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,iata) VALUES(1,'peer','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'future','1500ff',1,5,$1)`, crossBorderTime(time.Now().Unix()+3600))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff')`, time.Now().Unix()+3600)
	got, _ := crossBorderReviewResponse(t, srv)
	if got.Status != "partial" || got.Completeness.Complete || got.Sources.Peer.Status != "after_snapshot" || got.Sources.Peer.AgeSeconds != nil || got.Sources.Peer.LatestObservationAt != nil || len(got.Packets) != 0 {
		t.Fatalf("future-only feed became fresh/complete evidence: status=%s complete=%v source=%+v", got.Status, got.Completeness.Complete, got.Sources.Peer)
	}
	for _, finding := range got.Findings {
		if finding.Code == "after_snapshot_observations" {
			return
		}
	}
	t.Fatal("after-snapshot exclusion is not explained")
}

func TestCrossBorderReviewFutureRowsCannotConsumeObservationCap(t *testing.T) {
	db := crossBorderFixture(t)
	now := time.Now().UTC()
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(1,'peer','BBB')`)
	crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type) VALUES(1,'valid','1500ff',1,5),(2,'future','1500ff',1,5)`)
	crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff'),(2,1,$2,'rx','1500ff'),(2,1,$3,'rx','1500ff')`, now.Unix()-10, now.Unix()+3600, now.Unix()+7200)
	cfg := crossBorderTestConfig()
	cfg.ObservationLimit = 1
	for _, hashes := range [][]string{nil, {"valid", "future"}} {
		got, err := db.readCrossBorder(context.Background(), []string{"BBB"}, now.Add(-time.Hour), now, cfg, hashes)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Observations) != 1 || got.Observations[0].Hash != "valid" || got.ObservationsTruncated || got.LatestStoredObservationAt == nil || *got.LatestStoredObservationAt != crossBorderTime(now.Unix()-10) {
			t.Errorf("future timestamps displaced valid bounded evidence: %+v", got)
		}
	}
}
