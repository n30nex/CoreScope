package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
)

func historyBoundsFixture(t *testing.T) (*DB, time.Time, []crossBorderSelectedObserver) {
	t.Helper()
	db := crossBorderFixture(t)
	now := time.Unix(1800000000, 0).UTC()
	crossBorderExec(t, db, `CREATE INDEX history_bounds_observer_time ON observations(observer_idx,timestamp)`)
	crossBorderExec(t, db, `INSERT INTO observers(rowid,id,iata) VALUES(11,'first','AAA'),(22,'second','AAA'),(33,'foreign','BBB'),(44,'empty','AAA'),(55,'future','AAA')`)
	crossBorderExec(t, db, `INSERT INTO transmissions(id,hash,raw_hex,first_seen) VALUES(1,'content','1500ff',$1)`, crossBorderTime(now.Unix()-100000))
	for _, row := range [][2]int64{{11, -2000}, {11, -10}, {11, -10}, {11, 50}, {22, -1000}, {22, 0}, {22, 100}, {33, -100000}, {33, 100000}, {55, 200}} {
		crossBorderExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,timestamp,direction,raw_hex) VALUES(1,$1,$2,'rx','1500ff')`, row[0], now.Unix()+row[1])
	}
	return db, now, []crossBorderSelectedObserver{{RowID: 11, ID: "first", IATA: "AAA"}, {RowID: 22, ID: "second", IATA: "AAA"}, {RowID: 44, ID: "empty", IATA: "AAA"}}
}

func historyBoundValue(t *testing.T, db *DB, query string, args []any) sql.NullInt64 {
	t.Helper()
	var result sql.NullInt64
	if query == "" {
		return result
	}
	err := db.conn.QueryRowContext(context.Background(), query, db.planWithValues(args)...).Scan(&result)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return result
}

func TestCrossBorderHistoryBoundsSelectionCutoffAndNulls(t *testing.T) {
	db, now, selected := historyBoundsFixture(t)
	for _, tc := range []struct {
		bound crossBorderHistoryBound
		want  int64
	}{{crossBorderHistoryLatest, 0}, {crossBorderHistoryAfter, 100}} {
		q, args := db.crossBorderHistoryQuery([]string{"AAA"}, selected, now, tc.bound)
		got := historyBoundValue(t, db, q, args)
		if !got.Valid || got.Int64 != now.Unix()+tc.want {
			t.Fatalf("selected bound=%d: got%+v", tc.bound, got)
		}
		if len(args) != 4 || args[0] != int64(11) || args[1] != int64(22) || args[2] != int64(44) || args[3] != now.Unix() {
			t.Fatalf("unbounded/interpolated selection arguments: %#v", args)
		}
	}
	oldestQuery, oldestArgs := db.crossBorderHistoryQuery([]string{"AAA"}, selected, now, crossBorderHistoryOldest)
	if oldestQuery != "" || len(oldestArgs) != 0 {
		t.Fatal("optimized dashboard still queries optional full-history oldest")
	}
	for _, selection := range [][]crossBorderSelectedObserver{{}, {{RowID: 44, ID: "empty", IATA: "AAA"}}} {
		for _, bound := range []crossBorderHistoryBound{crossBorderHistoryOldest, crossBorderHistoryLatest, crossBorderHistoryAfter} {
			q, args := db.crossBorderHistoryQuery([]string{"AAA"}, selection, now, bound)
			if got := historyBoundValue(t, db, q, args); got.Valid {
				t.Fatalf("empty history became a timestamp: %+v", got)
			}
		}
	}
	future := []crossBorderSelectedObserver{{RowID: 55, ID: "future", IATA: "AAA"}}
	for _, bound := range []crossBorderHistoryBound{crossBorderHistoryOldest, crossBorderHistoryLatest} {
		q, args := db.crossBorderHistoryQuery([]string{"AAA"}, future, now, bound)
		if got := historyBoundValue(t, db, q, args); got.Valid {
			t.Fatal("future-only observer became in-snapshot history")
		}
	}
	q, args := db.crossBorderHistoryQuery([]string{"AAA"}, future, now, crossBorderHistoryAfter)
	if got := historyBoundValue(t, db, q, args); !got.Valid || got.Int64 != now.Unix()+200 {
		t.Fatal("future-only timestamp lost")
	}
	crossBorderExec(t, db, `UPDATE observers SET iata='BBB' WHERE rowid IN(11,22)`)
	q, args = db.crossBorderHistoryQuery([]string{"AAA"}, selected, now, crossBorderHistoryLatest)
	if got := historyBoundValue(t, db, q, args); !got.Valid || got.Int64 != now.Unix() {
		t.Fatal("metadata change replaced the captured observer cohort")
	}
	q, args = db.crossBorderHistoryQuery([]string{"AAA"}, nil, now, crossBorderHistoryOldest)
	if got := historyBoundValue(t, db, q, args); got.Valid || !strings.Contains(q, "UPPER(TRIM(COALESCE(ob.iata,''))) IN") {
		t.Fatal("fallback no longer follows the existing IATA semantics")
	}
}

func TestCrossBorderHistoryBoundsUsePerObserverSeeks(t *testing.T) {
	db := &DB{backend: dbconfig.Postgres, isV3: true}
	selection := []crossBorderSelectedObserver{{RowID: 11, ID: "first", IATA: "AAA"}, {RowID: 22, ID: "second", IATA: "AAA"}}
	for _, tc := range []struct {
		bound                            crossBorderHistoryBound
		aggregate, comparison, direction string
	}{{crossBorderHistoryLatest, "MAX", "<=", "DESC"}, {crossBorderHistoryAfter, "MAX", ">", "DESC"}} {
		query, args := db.crossBorderHistoryQuery([]string{"AAA"}, selection, time.Unix(99, 0), tc.bound)
		if !strings.Contains(query, "SELECT "+tc.aggregate+"((SELECT o.timestamp") || !strings.Contains(query, "o.observer_idx=ob.rowid") || !strings.Contains(query, "o.timestamp "+tc.comparison+" $3 ORDER BY o.timestamp "+tc.direction+" LIMIT 1))") || !strings.Contains(query, "FROM observers ob WHERE ob.rowid IN ($1,$2)") {
			t.Fatalf("bound%d can scan global history: %s", tc.bound, query)
		}
		if len(args) != 3 || args[0] != int64(11) || args[1] != int64(22) || args[2] != int64(99) {
			t.Fatal("unexpected seek parameter contract")
		}
		fallback, _ := db.crossBorderHistoryQuery([]string{"AAA"}, nil, time.Unix(99, 0), tc.bound)
		if strings.Contains(fallback, "((SELECT") || !strings.Contains(fallback, "o.timestamp "+tc.comparison+" $2") || !strings.Contains(fallback, "ORDER BY o.timestamp "+tc.direction+" LIMIT 1") {
			t.Fatalf("legacy fallback changed: %s", fallback)
		}
	}
	q, args := db.crossBorderHistoryQuery([]string{"AAA"}, selection, time.Unix(99, 0), crossBorderHistoryOldest)
	if q != "" || len(args) != 0 {
		t.Fatal("selected history oldest should remain unestablished")
	}
	fallback, _ := db.crossBorderHistoryQuery([]string{"AAA"}, nil, time.Unix(99, 0), crossBorderHistoryOldest)
	if !strings.Contains(fallback, "ORDER BY o.timestamp ASC LIMIT 1") {
		t.Fatal("legacy oldest lookup was removed")
	}
}

func TestCrossBorderHistoryBoundsNativeSeekPlan(t *testing.T) {
	db, now, selected := historyBoundsFixture(t)
	fill := testNativeSQL(`WITH RECURSIVE n(i) AS(VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000) INSERT INTO observations(transmission_id,observer_idx,timestamp) SELECT 1,33,$1-i FROM n`, `INSERT INTO observations(transmission_id,observer_idx,timestamp) SELECT 1,33,$1-i FROM generate_series(1,10000) i`)
	crossBorderExec(t, db, fill, now.Unix()-1000)
	crossBorderExec(t, db, `ANALYZE observations`)
	for _, bound := range []crossBorderHistoryBound{crossBorderHistoryLatest, crossBorderHistoryAfter} {
		query, args := db.crossBorderHistoryQuery([]string{"AAA"}, selected, now, bound)
		if db.Backend() == dbconfig.SQLite {
			rows, err := db.conn.Query(`EXPLAIN QUERY PLAN `+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			details := []string{}
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			plan := strings.Join(details, "\n")
			t.Logf("bound%d:\n%s", bound, plan)
			if !strings.Contains(plan, "CORRELATED SCALAR SUBQUERY") || !strings.Contains(plan, "history_bounds_observer_time (observer_idx=?") || strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("missing bounded composite-index seek:\n%s", plan)
			}
		} else {
			type planNode struct {
				NodeType           string     `json:"Node Type"`
				IndexCond          string     `json:"Index Cond"`
				Filter             string     `json:"Filter"`
				ParentRelationship string     `json:"Parent Relationship"`
				PlanRows           int        `json:"Plan Rows"`
				Plans              []planNode `json:"Plans"`
			}
			var raw string
			if err := db.conn.QueryRow(`EXPLAIN (FORMAT JSON) `+query, db.planWithValues(args)...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plan []struct {
				Plan planNode `json:"Plan"`
			}
			if err := json.Unmarshal([]byte(raw), &plan); err != nil {
				t.Fatal(err)
			}
			limited, correlated := false, false
			var visit func(planNode)
			visit = func(node planNode) {
				if node.NodeType == "Limit" && node.ParentRelationship == "SubPlan" && node.PlanRows == 1 {
					limited = true
				}
				if strings.Contains(node.IndexCond+node.Filter, "observer_idx = ob.rowid") {
					correlated = true
				}
				for _, child := range node.Plans {
					visit(child)
				}
			}
			for _, root := range plan {
				visit(root.Plan)
			}
			t.Logf("bound%d: %s", bound, raw)
			if !limited || !correlated {
				t.Fatal("PostgreSQL lost the per-observer correlated one-result bound")
			}
		}
	}
}

func TestCrossBorderHistoryBoundsDashboardLeavesOldestUnestablished(t *testing.T) {
	db, now, _ := historyBoundsFixture(t)
	if db.Backend() != dbconfig.Postgres {
		t.Skip("numeric selected-cohort optimization is PostgreSQL-only")
	}
	cfg := crossBorderTestConfig()
	snapshot, err := db.readCrossBorderDataSelected(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, cfg, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.OldestStoredObservationAt != nil || snapshot.LatestStoredObservationAt == nil || *snapshot.LatestStoredObservationAt != crossBorderTime(now.Unix()) || snapshot.LatestAfterSnapshotAt == nil || *snapshot.LatestAfterSnapshotAt != crossBorderTime(now.Unix()+200) || len(snapshot.Observations) == 0 {
		t.Fatalf("unknown oldest became empty database or lost freshness: %+v", snapshot)
	}
	got, err := buildCrossBorderDashboard(context.Background(), cfg, crossBorderTestFilters(), snapshot, crossBorderSnapshot{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Local.CoveredFromAt == nil || got.Local.Timeline[0].Complete || got.Local.Timeline[0].Records != nil {
		t.Fatal("unqueried oldest fabricated complete history before the sample")
	}
	cfg.ObservationLimit = 1
	capped, err := db.readCrossBorderDataSelected(context.Background(), []string{"AAA"}, now.Add(-time.Hour), now, cfg, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	limited, err := buildCrossBorderDashboard(context.Background(), cfg, crossBorderTestFilters(), capped, crossBorderSnapshot{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if capped.OldestStoredObservationAt != nil || !limited.Local.Truncated || limited.Local.CoveredFromAt != nil {
		t.Fatal("capped newest-only sample invented historical coverage")
	}
}
