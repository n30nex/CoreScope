package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// SQLite binds named $N parameters by first appearance, while PostgreSQL
// interprets N as an argument position. Verify both intended nodes exist so a
// missing control cannot make a privacy or collision regression pass vacuously.
func crossBorderRequireFixtureNodes(t *testing.T, db *DB, a, b string) {
	t.Helper()
	var count int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM nodes WHERE public_key IN ($1,$2)`, a, b).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected both fixture identities: count=%d error=%v", count, err)
	}
}

func TestCrossBorderLegacyHiddenObserverAliasIsPrivateAcrossSources(t *testing.T) {
	srv, local, peer := crossBorderReviewService(t, crossBorderTestConfig())
	srv.cfg.SetHiddenNamePrefixes([]string{"Hidden"})
	a := "aa1101" + strings.Repeat("f", 58)
	b := "bb2202" + strings.Repeat("f", 58)
	now := time.Now().Unix() - 10
	crossBorderExec(t, local, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'local','Visible receiver','AAA'),(2,$1,'Hidden observer only','ZZZ')`, a)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Visible receiver','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO nodes(public_key,name,role,lat,lon,last_seen,configured_scope) VALUES($1,'Peer alias of hidden observer','repeater',43.65,-79.38,$2,'sensitive-scope'),($3,'Visible US repeater','repeater',42.88,-78.88,$2,NULL)`, a, crossBorderTime(now), b)
	crossBorderRequireFixtureNodes(t, peer, a, b)
	// The legacy report exposes only the last observed relay. Put the
	// protected alias last; a separate packet keeps the visible control.
	raw := "1582bb2202aa1101ff"
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'private-alias-path',$1,1,5,$2),(2,'visible-control','1581bb2202ff',1,5,$2)`, raw, crossBorderTime(now))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx',$2),(2,1,$1,'rx','1581bb2202ff')`, now, raw)
	got, body := crossBorderReviewResponse(t, srv) // GET /api/cross-border, without dashboard view
	for _, private := range []string{a, "Hidden observer only", "Peer alias of hidden observer", "sensitive-scope"} {
		if strings.Contains(body, private) {
			t.Errorf("legacy report disclosed cross-source protected metadata: %q", private)
		}
	}
	if len(got.PeerRepeaters) != 1 || got.PeerRepeaters[0].PublicKey != b {
		t.Fatalf("visible metadata lost or hidden alias retained: %+v", got.PeerRepeaters)
	}
}

func TestCrossBorderLegacyMissingPrivacySourceDoesNotPublishPeerAliases(t *testing.T) {
	srv, _, peer := crossBorderReviewService(t, crossBorderTestConfig())
	srv.cfg.SetHiddenNamePrefixes([]string{"Hidden"})
	now := time.Now().Unix() - 10
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Peer alias requiring local check','BBB')`)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,first_seen) VALUES(1,'content','1500ff',1,5,$1)`, crossBorderTime(now))
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1500ff')`, now)
	srv.crossBorder.local = nil
	got, body := crossBorderReviewResponse(t, srv)
	if got.Status != "unavailable" || len(got.PeerObservers) != 0 || strings.Contains(body, "Peer alias requiring local check") {
		t.Fatal("legacy report published aliases without checking the other privacy source")
	}
}

func TestCrossBorderLegacyJointPrivacySkipsDBWithoutNamePolicy(t *testing.T) {
	local, peer := crossBorderSnapshot{}, crossBorderSnapshot{Nodes: []crossBorderNode{{PublicKey: strings.Repeat("a", 64), Role: "repeater"}}}
	if err := applyCrossBorderJointPrivacy(context.Background(), nil, nil, &local, &peer, &Config{}); err != nil {
		t.Fatalf("no-name-policy path attempted database work: %v", err)
	}
}
