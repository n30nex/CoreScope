package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

func querySelectionFixture(t *testing.T) (*DB, time.Time) {
	t.Helper()
	db := crossBorderFixture(t)
	now := time.Unix(1800000000, 0).UTC()
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,name,iata) VALUES(11,'first','First','AAA'),(22,'second','Second','AAA'),(33,'foreign','Foreign','BBB')`)
	for i, id := range []int{11, 22, 33} {
		crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,payload_type,route_type) VALUES($1,$2,'1500ff',5,1)`, i+1, fmt.Sprint(id))
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES($1,$2,$3,'rx','1500ff')`, i+1, id, now.Unix()-10)
	}
	return db, now
}

func TestCrossBorderQuerySelectionCapturesCompleteMetadataAndFallsBackOnCap(t *testing.T) {
	db, now := querySelectionFixture(t)
	cfg := crossBorderTestConfig()
	full, err := db.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !full.ObserverSelectionComplete || len(full.SelectedObserverRows) != 2 || full.SelectedObserverRows[0].RowID != 11 || full.SelectedObserverRows[1].RowID != 22 {
		t.Fatalf("complete private row IDs missing: %+v", full.SelectedObserverRows)
	}
	public, _ := json.Marshal(CrossBorderResponse{PeerObservers: full.Observers})
	if strings.Contains(strings.ToLower(string(public)), "rowid") || strings.Contains(string(public), "SelectedObserver") {
		t.Fatal("private query identity was serialized")
	}
	pg := *db
	pg.backend = dbconfig.Postgres
	selection := pg.crossBorderDashboardObserverSelection(full, true)
	if len(selection) != 2 {
		t.Fatal("complete PostgreSQL dashboard selection not eligible")
	}
	sqlite := *db
	sqlite.backend = dbconfig.SQLite
	if pg.crossBorderDashboardObserverSelection(full, false) != nil || sqlite.crossBorderDashboardObserverSelection(full, true) != nil {
		t.Fatal("optimization reached legacy view or SQLite")
	}
	cfg.MetadataLimit = 1
	limited, err := db.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if limited.ObserverSelectionComplete || len(limited.Observations) != 2 || pg.crossBorderDashboardObserverSelection(limited, true) != nil {
		t.Fatalf("capped metadata silently restricted observations: %+v", limited)
	}
}

func TestCrossBorderQuerySelectionBindingAndMembership(t *testing.T) {
	db, now := querySelectionFixture(t)
	pg := *db
	pg.backend = dbconfig.Postgres
	selection := []crossBorderSelectedObserver{{RowID: 11, ID: "first", IATA: "AAA"}, {RowID: 22, ID: "second", IATA: "AAA"}}
	query, args := pg.crossBorderObservationQuerySelected([]string{"AAA"}, now.Add(-time.Hour), now, crossBorderTestConfig(), nil, selection)
	if !strings.Contains(query, "o.observer_idx IN ($3,$4)") || strings.Contains(query, "UPPER(TRIM(COALESCE(ob.iata,''))) IN") || strings.Contains(query, "IN (11") {
		t.Fatalf("IDs were not bound as the measured predicate: %s", query)
	}
	if len(args) != 5 || args[2] != int64(11) || args[3] != int64(22) {
		t.Fatalf("unexpected typed arguments: %#v", args)
	}
	// The selected query uses common SQL. Execute it on the active backend's
	// real fixture to verify membership, not only the generated SQL spelling.
	rows, err := db.conn.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		seen[fmt.Sprint(values[1])] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !seen["first"] || !seen["second"] || seen["foreign"] || len(seen) != 2 {
		t.Fatalf("selected membership changed: %+v", seen)
	}
	fallback, _ := pg.crossBorderObservationQuerySelected([]string{"AAA"}, now.Add(-time.Hour), now, crossBorderTestConfig(), nil, nil)
	if !strings.Contains(fallback, "UPPER(TRIM(COALESCE(ob.iata,''))) IN") {
		t.Fatal("nil selection did not preserve IATA fallback")
	}
}

func TestCrossBorderQuerySelectionCohortSurvivesIATAChange(t *testing.T) {
	db, now := querySelectionFixture(t)
	initial, err := db.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, crossBorderTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pg := *db
	pg.backend = dbconfig.Postgres
	selection := pg.crossBorderDashboardObserverSelection(initial, true)
	if len(selection) != 2 {
		t.Fatal("selection was not captured before metadata change")
	}
	selectedIATA := crossBorderSelectionIATAs(selection)
	crossBorderExec(t, db, `UPDATE observers SET iata=CASE id WHEN 'first' THEN 'CCC' WHEN 'foreign' THEN 'AAA' ELSE iata END`)
	query, args := pg.crossBorderObservationQuerySelected([]string{"AAA"}, now.Add(-time.Hour), now, crossBorderTestConfig(), nil, selection)
	rows, err := db.conn.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	seen := map[string]bool{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprint(values[1])
		region, ok := crossBorderSelectedIATA(selectedIATA, id, fmt.Sprint(values[len(values)-1]))
		if !ok || region != "AAA" {
			t.Fatalf("selected record acquired a different cohort: id=%s region=%s", id, region)
		}
		seen[id] = true
	}
	if !seen["first"] || !seen["second"] || seen["foreign"] {
		t.Fatalf("IATA move changed the selected membership: %+v", seen)
	}
	if _, ok := crossBorderSelectedIATA(selectedIATA, "unexpected", "AAA"); ok {
		t.Fatal("unexpected identity was silently assigned to captured cohort")
	}
}

func TestCrossBorderQuerySelectionLimitsLegacyAndEmpty(t *testing.T) {
	pg := &DB{backend: dbconfig.Postgres, isV3: true}
	full := crossBorderSnapshot{ObserverSelectionComplete: true, SelectedObserverRows: []crossBorderSelectedObserver{}}
	if got := pg.crossBorderDashboardObserverSelection(full, true); got == nil || len(got) != 0 {
		t.Fatal("empty complete selection must differ from fallback")
	}
	query, args := pg.crossBorderObservationQuerySelected([]string{"AAA"}, time.Unix(1, 0), time.Unix(2, 0), crossBorderTestConfig(), nil, []crossBorderSelectedObserver{})
	if !strings.Contains(query, "1=0") || len(args) != 3 {
		t.Fatal("complete empty selection cannot scan regional observations")
	}
	for i := 0; i < 501; i++ {
		full.SelectedObserverRows = append(full.SelectedObserverRows, crossBorderSelectedObserver{RowID: int64(i + 1), ID: fmt.Sprint(i), IATA: "AAA"})
	}
	if pg.crossBorderDashboardObserverSelection(full, true) != nil {
		t.Fatal("observer argument bound exceeded500")
	}
	full.SelectedObserverRows = full.SelectedObserverRows[:1]
	pg.isV3 = false
	if pg.crossBorderDashboardObserverSelection(full, true) != nil {
		t.Fatal("legacy schema used numeric observation IDs")
	}
	pg.isV3 = true
	full.ObserverSelectionComplete = false
	if pg.crossBorderDashboardObserverSelection(full, true) != nil {
		t.Fatal("missing row IDs were treated as complete")
	}
}

func TestCrossBorderQuerySelectionImplicitRowIDRetainsIATAFallback(t *testing.T) {
	db, now := querySelectionFixture(t)
	if db.Backend() != dbconfig.SQLite {
		t.Skip("SQLite implicit rowid compatibility fixture")
	}
	// SQLite still exposes the INTEGER PRIMARY KEY through its implicit rowid,
	// but metadata does not advertise a portable explicit rowid column.
	crossBorderExec(t, db, `ALTER TABLE observers RENAME COLUMN rowid TO legacy_number`)
	got, err := db.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, crossBorderTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pg := *db
	pg.backend = dbconfig.Postgres
	if got.ObserverSelectionComplete || len(got.SelectedObserverRows) != 0 || len(got.Observations) != 2 || pg.crossBorderDashboardObserverSelection(got, true) != nil {
		t.Fatal("missing explicit row IDs lost legacy observations or enabled partial optimization")
	}
}

func TestCrossBorderQuerySelectionRadioAndCoverageUseCapturedCohort(t *testing.T) {
	db, now := querySelectionFixture(t)
	initial, err := db.readCrossBorder(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, crossBorderTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pg := *db
	pg.backend = dbconfig.Postgres
	selection := pg.crossBorderDashboardObserverSelection(initial, true)
	if len(selection) != 2 {
		t.Fatal("no captured cohort")
	}
	crossBorderExec(t, db, `UPDATE observers SET iata=CASE id WHEN 'first' THEN 'CCC' WHEN 'foreign' THEN 'AAA' ELSE iata END`)
	crossBorderExec(t, db, `UPDATE observations SET timestamp=CASE observer_idx WHEN 11 THEN $1 WHEN 33 THEN $2 ELSE timestamp END`, now.Unix()-300, now.Unix()-100)
	args := []any{}
	where := db.crossBorderObserverWhere([]string{"AAA"}, selection, "o.observer_idx", &args)
	var oldest int64
	if err := db.conn.QueryRowContext(context.Background(), `SELECT MIN(o.timestamp) FROM observations o `+db.crossBorderObserverJoin()+` WHERE `+where, args...).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != now.Unix()-300 {
		t.Fatal("freshness/coverage adopted a different IATA cohort")
	}
	crossBorderExec(t, db, `CREATE TABLE IF NOT EXISTS observer_metrics(observer_id TEXT,timestamp TEXT,tx_air_secs BIGINT,rx_air_secs BIGINT,recv_errors BIGINT,packets_sent BIGINT,packets_recv BIGINT)`)
	for i, id := range []string{"first", "second", "foreign"} {
		tx, rx := 30*(i+1), 60*(i+1)
		if id == "foreign" {
			tx, rx = 270, 270
		}
		crossBorderExec(t, db, `INSERT INTO observer_metrics(observer_id,timestamp,tx_air_secs,rx_air_secs) VALUES($1,$2,0,0),($1,$3,$4,$5)`, id, crossBorderTime(now.Unix()-1200), crossBorderTime(now.Unix()-900), tx, rx)
	}
	for _, hideFirst := range []bool{false, true} {
		snapshot := crossBorderSnapshot{DashboardObserverSelection: selection}
		hidden := map[string]bool{}
		if hideFirst {
			hidden["first"] = true
		}
		if err := db.readCrossBorderDashboardMetrics(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, hidden, &Config{}, &snapshot); err != nil {
			t.Fatal(err)
		}
		radio, err := buildCrossBorderDashboardRadio(context.Background(), snapshot, now.Add(-time.Hour).Unix(), now, 500)
		if err != nil {
			t.Fatal(err)
		}
		wantCount, wantTX, wantRX := 2, 15.0, 30.0
		if hideFirst {
			wantCount, wantTX, wantRX = 1, 20, 40
		}
		if radio.ReportingObservers != wantCount || radio.TxAirtimePct == nil || *radio.TxAirtimePct != wantTX || radio.RxAirtimePct == nil || *radio.RxAirtimePct != wantRX {
			t.Fatalf("radio cohort/privacy differs from captured packet cohort: %+v", radio)
		}
		for _, observer := range radio.Observers {
			if observer.ID == "foreign" || observer.IATA != "AAA" {
				t.Fatalf("radio adopted changed IATA metadata: %+v", observer)
			}
		}
	}
}
