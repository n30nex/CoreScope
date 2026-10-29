package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

func dashboardReviewGet(t *testing.T, srv *Server) (CrossBorderResponse, string) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.handleCrossBorder(w, httptest.NewRequest("GET", "/api/cross-border?view=dashboard", nil))
	var result CrossBorderResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("dashboard handler failed: status=%d", w.Code)
	}
	return result, w.Body.String()
}

func TestCrossBorderDashboardReviewPrivacyCannotEraseCappedCoverageBoundary(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.ObservationLimit = 1
	srv, _, peer := crossBorderReviewService(t, cfg)
	srv.cfg.SetHiddenNamePrefixes([]string{"Private"})
	now := time.Now().Unix()
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'visible','Visible','BBB'),(2,'hidden','Private observer','BBB')`)
	for i := 1; i <= 12; i++ {
		stamp, observer := now-int64(i)-10, 2
		if i == 11 {
			stamp, observer = now-1800, 1
		}
		if i == 12 {
			stamp, observer = now-7200, 1
		}
		crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type) VALUES($1,$2,'1500ff',1,5)`, i, fmt.Sprint(i))
		crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES($1,$2,$3,'rx','1500ff')`, i, observer, stamp)
	}
	got, _ := dashboardReviewGet(t, srv)
	if got.Dashboard == nil {
		t.Fatalf("no dashboard: status=%s", got.Status)
	}
	source := got.Dashboard.Peer
	if !source.Truncated || source.Records != 0 {
		t.Fatalf("wrong private capped sample: %+v", source)
	}
	for _, point := range source.Timeline {
		if point.Complete || point.Records != nil {
			t.Fatalf("hidden newest sample fabricated complete zero history: %+v", point)
		}
	}
	if source.CoveredFromAt != nil {
		at, err := time.Parse(time.RFC3339, *source.CoveredFromAt)
		if err != nil || at.After(time.Now()) {
			t.Fatalf("future/invalid coverage claim: %v", source.CoveredFromAt)
		}
	}
}

func TestCrossBorderDashboardReviewTiedSQLCutoffLeavesBoundaryBucketUnknown(t *testing.T) {
	db := crossBorderFixture(t)
	now := time.Unix(1800000000, 0).UTC()
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'peer','Visible','BBB')`)
	for i := 1; i <= 13; i++ {
		stamp := now.Unix() - 600
		if i <= 6 {
			stamp = now.Unix() - 100
		}
		if i == 13 {
			stamp = now.Unix() - 7200
		}
		crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type) VALUES($1,$2,'1500ff',1,5)`, i, fmt.Sprint(i))
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES($1,1,$2,'rx','1500ff')`, i, stamp)
	}
	cfg := crossBorderTestConfig()
	cfg.ObservationLimit = 1
	scanCfg := cfg
	scanCfg.ObservationLimit = crossBorderDashboardLimit(cfg)
	peer, _, _, err := db.readCrossBorderDashboard(context.Background(), []string{"BBB"}, crossBorderTestFilters(), scanCfg, &Config{}, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := buildCrossBorderDashboard(context.Background(), cfg, crossBorderTestFilters(), crossBorderSnapshot{}, peer, now)
	if err != nil {
		t.Fatal(err)
	}
	points := got.Peer.Timeline
	boundary, newer := points[len(points)-2], points[len(points)-1]
	if !got.Peer.Truncated || boundary.Complete || boundary.Records != nil {
		t.Fatalf("cap split timestamp ties but marked the boundary bucket complete: %+v", boundary)
	}
	if !newer.Complete || newer.Records == nil || *newer.Records != 6 {
		t.Fatalf("known newer bucket lost valid coverage: %+v", newer)
	}
}

func TestCrossBorderDashboardReviewMissingOrFutureCutoffNeverClaimsCoverage(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	old := crossBorderTime(now.Add(-2 * time.Hour).Unix())
	future := now.Unix() + 300
	for _, cutoff := range []*int64{nil, &future} {
		peer := dashboardSnapshot("peer", "BBB")
		peer.ObservationsTruncated = true
		peer.OldestStoredObservationAt = &old
		peer.DashboardScanCutoff = cutoff
		got, err := buildCrossBorderDashboard(context.Background(), crossBorderTestConfig(), crossBorderTestFilters(), crossBorderSnapshot{}, peer, now)
		if err != nil {
			t.Fatal(err)
		}
		if got.Peer.CoveredFromAt != nil {
			t.Fatalf("unrecoverable capped coverage was invented: %v", got.Peer.CoveredFromAt)
		}
		for _, point := range got.Peer.Timeline {
			if point.Complete || point.Records != nil {
				t.Fatal("unrecoverable capped history became zero")
			}
		}
	}
}

func TestCrossBorderDashboardReviewInactiveCollisionAndPrivacy(t *testing.T) {
	cfg := crossBorderTestConfig()
	cfg.MetadataLimit = 1
	srv, _, peer := crossBorderReviewService(t, cfg)
	srv.cfg.SetHiddenNamePrefixes([]string{"Private"})
	a := "aabb11" + strings.Repeat("a", 58)
	hidden := "aabb22" + strings.Repeat("b", 58)
	client := "ccdd11" + strings.Repeat("c", 58)
	now := time.Now().Unix() - 10
	crossBorderExec(t, peer, `CREATE TABLE IF NOT EXISTS inactive_nodes(public_key TEXT PRIMARY KEY,name TEXT,role TEXT)`)
	crossBorderExec(t, peer, `CREATE TABLE IF NOT EXISTS observer_metrics(observer_id TEXT,timestamp TEXT,tx_air_secs BIGINT,rx_air_secs BIGINT,recv_errors BIGINT,packets_sent BIGINT,packets_recv BIGINT)`)
	crossBorderExec(t, peer, `INSERT INTO nodes(public_key,name,role,last_seen) VALUES($1,'Visible repeater','repeater',$2),($3,'Current client','client',$2)`, a, crossBorderTime(now), client)
	crossBorderExec(t, peer, `INSERT INTO inactive_nodes(public_key,name,role) VALUES($1,'Private retained identity','client'),($2,'Old repeater role','repeater')`, hidden, client)
	crossBorderExec(t, peer, `INSERT INTO observers(rowid,id,name,iata) VALUES(1,'reader','Visible radio','BBB'),(2,$1,'Ordinary name','BBB')`, hidden)
	crossBorderExec(t, peer, `INSERT INTO transmissions(id,hash,raw_hex,route_type,payload_type,from_pubkey) VALUES(1,'advert','1140ff',1,4,$1),(2,'collision','1541aabbff',1,5,NULL),(3,'client-advert','1180ff',1,4,$2),(4,'private-content','1500ff',1,5,NULL)`, a, client)
	crossBorderExec(t, peer, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,1,$1,'rx','1140ff'),(2,1,$1,'rx','1541aabbff'),(3,1,$1,'rx','1180ff'),(4,2,$1,'rx','1500ff')`, now)
	crossBorderExec(t, peer, `INSERT INTO observer_metrics(observer_id,timestamp,tx_air_secs,rx_air_secs) VALUES('reader',$1,100,200),('reader',$2,130,260),($3,$1,0,0),($3,$2,270,270)`, crossBorderTime(now-1200), crossBorderTime(now-900), hidden)
	got, body := dashboardReviewGet(t, srv)
	if got.Dashboard == nil {
		t.Fatalf("no dashboard: status=%s", got.Status)
	}
	source := got.Dashboard.Peer
	if source.Records != 3 || source.Radio.ReportingObservers != 1 || source.Radio.TxAirtimePct == nil || *source.Radio.TxAirtimePct != 10 || source.Radio.RxAirtimePct == nil || *source.Radio.RxAirtimePct != 20 {
		t.Errorf("retained hidden identity affected visible records or radio metrics: %+v", source)
	}
	if len(source.Repeaters) != 1 || source.Repeaters[0].PublicKey != a || source.Repeaters[0].SampleForwardedPackets != 0 || !source.Repeaters[0].CarryingAmbiguous || source.Repeaters[0].OwnAdvertWidth == nil || *source.Repeaters[0].OwnAdvertWidth != 2 {
		t.Errorf("inactive collision or current role precedence failed: %+v", source.Repeaters)
	}
	if source.Widths[2].Advertisers != 0 {
		t.Error("retained repeater role overrode current client role")
	}
	for _, secret := range []string{hidden, "Private retained identity", "Ordinary name", "private-content"} {
		if strings.Contains(body, secret) {
			t.Errorf("retained privacy leaked %q", secret)
		}
	}
	for _, repeater := range got.PeerRepeaters {
		if repeater.SampleForwardedPackets != 0 {
			t.Error("legacy detail envelope claimed carrying despite retained collider")
		}
	}
}

func TestCrossBorderDashboardReviewUnreadableRetainedSetFailsClosed(t *testing.T) {
	srv, _, peer := crossBorderReviewService(t, crossBorderTestConfig())
	if peer.Backend() != dbconfig.SQLite {
		t.Skip("SQLite broken-view fixture exercises retained-table read failure")
	}
	crossBorderExec(t, peer, `CREATE VIEW inactive_nodes AS SELECT public_key,name,role FROM missing_retained_source`)
	got, _ := dashboardReviewGet(t, srv)
	if got.Status != "unavailable" || got.Dashboard != nil {
		t.Fatalf("known retained set read failure was treated as absence: %+v", got)
	}
}
