package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
)

// This synthetic fixture has a selective observer and mostly historical cohort.
// PostgreSQL VACUUM establishes fixture visibility before either timing phase.
// No live data is used.
func BenchmarkObserverTransmissionCoveringIndex(b *testing.B) {
	db, q := packetSummaryFixture(b, 128000)
	since := time.Unix(1700000200, 0).UTC()
	if db.Backend() == dbconfig.Postgres {
		for _, table := range []string{"observations", "transmissions"} {
			if _, err := db.conn.Exec("VACUUM ANALYZE " + table); err != nil {
				b.Fatal(err)
			}
		}
	}
	phaseSQL := `SELECT DISTINCT oi.transmission_id FROM observations oi WHERE oi.observer_idx=(SELECT rowid FROM observers WHERE id=` + db.parameter(1) + `)`
	phase := func(b *testing.B) int {
		b.Helper()
		rows, err := db.conn.Query(phaseSQL, q.Observer)
		if err != nil {
			b.Fatal(err)
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				b.Fatal(err)
			}
			count++
		}
		if err := rows.Err(); err != nil {
			b.Fatal(err)
		}
		return count
	}
	wantIDs := phase(b)
	want, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
	if err != nil || wantIDs == 0 || len(want.Packets) == 0 {
		b.Fatal("invalid benchmark fixture", err)
	}
	for _, stage := range []string{"uncovered", "covered"} {
		if stage == "covered" {
			// This minimal, disposable performance fixture is separate from the
			// canonical-schema owner/CLI upgrade tests.
			if _, err := db.conn.Exec(`CREATE INDEX ` + dbschema.ObserverTransmissionIndex + ` ON observations(observer_idx,transmission_id)`); err != nil {
				b.Fatal(err)
			}
			if db.Backend() == dbconfig.Postgres {
				var indexBytes int64
				if err := db.conn.QueryRow(`SELECT pg_relation_size($1::regclass)`, dbschema.ObserverTransmissionIndex).Scan(&indexBytes); err != nil {
					b.Fatal(err)
				}
				b.Logf("additional covering B-tree size: %d bytes; inserts/deletes must maintain this index", indexBytes)
				if present, err := dbschema.HasPostgresObserverTransmissionIndex(context.Background(), db.conn); err != nil || !present {
					b.Fatal(present, err)
				}
				var raw string
				if err := db.conn.QueryRow("EXPLAIN (ANALYZE, FORMAT JSON) "+phaseSQL, q.Observer).Scan(&raw); err != nil {
					b.Fatal(err)
				}
				var plans []struct{ Plan map[string]any }
				if err := json.Unmarshal([]byte(raw), &plans); err != nil {
					b.Fatal(err)
				}
				found := false
				var walk func(map[string]any)
				walk = func(node map[string]any) {
					if node["Index Name"] == dbschema.ObserverTransmissionIndex {
						found = true
						if node["Node Type"] != "Index Only Scan" || node["Heap Fetches"] != float64(0) {
							b.Fatalf("covered phase still fetches heap rows: %v", node)
						}
					}
					children, _ := node["Plans"].([]any)
					for _, child := range children {
						walk(child.(map[string]any))
					}
				}
				walk(plans[0].Plan)
				if !found {
					b.Fatal("planner did not use the covering index")
				}
				b.Log("verified covering Index Only Scan with zero heap fetches")
			} else {
				if present, err := dbschema.HasSQLiteObserverTransmissionIndex(context.Background(), db.conn); err != nil || !present {
					b.Fatal(present, err)
				}
			}
		}
		got, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
		if err != nil || !reflect.DeepEqual(got, want) || phase(b) != wantIDs {
			b.Fatal("index changed results", err)
		}
		b.Run(stage+"/observer-ids", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if phase(b) != wantIDs {
					b.Fatal("observer cohort changed")
				}
			}
		})
		b.Run(stage+"/summary", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				got, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
				if err != nil || len(got.Packets) != len(want.Packets) || got.Truncated != want.Truncated {
					b.Fatal("summary changed", err)
				}
			}
		})
	}
	b.Log(fmt.Sprintf("128000 transmissions, 2048000 observations, 128 observers; cohort=%d returned=%d", wantIDs, len(want.Packets)))
}
