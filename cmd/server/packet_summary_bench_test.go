package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Production-shaped synthetic data: 128 observers and 16 observations per
// transmission. Neither source identities nor private payloads are required.
func packetSummaryFixture(t testing.TB, transmissions int) (*DB, PacketQuery) {
	t.Helper()
	db := setupTestDB(t)
	copyTestRows(t, db.conn, "observers", []string{"rowid", "id", "name", "iata"}, 128, func(i int) []any {
		return []any{i + 1, fmt.Sprintf("observer-%03d", i+1), "Synthetic observer", "AAA"}
	})
	copyTestRows(t, db.conn, "transmissions", []string{"id", "hash", "raw_hex", "first_seen", "route_type", "payload_type", "decoded_json"}, transmissions, func(i int) []any {
		return []any{i + 1, fmt.Sprintf("window-%d", i+1), "AA", "2023-11-14T00:00:00Z", 1, 5, `{}`}
	})
	const epoch = 1700000000
	copyTestRows(t, db.conn, "observations", []string{"transmission_id", "observer_idx", "timestamp", "path_json", "direction"}, transmissions*16, func(i int) []any {
		tx := i/16 + 1
		return []any{tx, i%128 + 1, epoch + (tx%3)*100, `[` + strings.Repeat(`"AA",`, i%16) + `"BB"]`, "rx"}
	})
	for _, query := range []string{
		`CREATE INDEX window_observer ON observations(observer_idx)`,
		`CREATE INDEX window_observer_time ON observations(observer_idx,timestamp)`,
		`ANALYZE observations`, `ANALYZE transmissions`, `ANALYZE observers`,
	} {
		if _, err := db.conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db, PacketQuery{Observer: "observer-001", Since: time.Unix(epoch+100, 0).UTC().Format(time.RFC3339), Limit: 10000, Order: "DESC"}
}

// Frozen 6d4 query/scan path, for a direct before/after summary comparison.
func priorPacketSummary(b *testing.B, db *DB, observer string, since time.Time, limit int) *PacketSummaryResult {
	b.Helper()
	where, args := db.buildTransmissionWhere(PacketQuery{Observer: observer})
	where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM observations ot WHERE ot.transmission_id=t.id AND ot.timestamp >= %s LIMIT 1 OFFSET 0)`, db.parameter(len(args)+1)))
	args = append(args, since.Unix(), limit+1)
	query := fmt.Sprintf(`SELECT t.hash,t.payload_type,t.route_type,t.first_seen FROM transmissions t WHERE %s ORDER BY t.id DESC LIMIT %s`, strings.Join(where, " AND "), db.parameter(len(args)))
	rows, err := db.conn.QueryContext(context.Background(), query, args...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	result := &PacketSummaryResult{Packets: make([]PacketSummary, 0, limit+1), Limit: limit}
	for rows.Next() {
		var packet PacketSummary
		if err := rows.Scan(&packet.Hash, &packet.PayloadType, &packet.RouteType, &packet.Timestamp); err != nil {
			b.Fatal(err)
		}
		result.Packets = append(result.Packets, packet)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	if len(result.Packets) > limit {
		result.Packets, result.Truncated = result.Packets[:limit], true
	}
	return result
}

func BenchmarkPacketSummaryNativePlan(b *testing.B) {
	db, q := packetSummaryFixture(b, 128000)
	// About one third of the observer's historical transmissions have current
	// evidence, matching the native gate's 4,091 of 13,736 qualifying IDs.
	since := time.Unix(1700000200, 0).UTC()
	before := priorPacketSummary(b, db, q.Observer, since, q.Limit)
	after, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
	if err != nil || !reflect.DeepEqual(before, after) {
		b.Fatalf("summary fields, membership, order or cap changed: %v", err)
	}
	b.Logf("128000 transmissions, 2048000 observations, 128 observers; qualifying=%d truncated=%v", len(after.Packets), after.Truncated)
	b.Run("6d4-summary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := priorPacketSummary(b, db, q.Observer, since, q.Limit); len(got.Packets) != len(after.Packets) || got.Truncated != after.Truncated {
				b.Fatal("baseline summary changed")
			}
		}
	})
	b.Run("ids-before-fields", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
			if err != nil || len(got.Packets) != len(after.Packets) || got.Truncated != after.Truncated {
				b.Fatalf("candidate summary changed: %v", err)
			}
		}
	})
}

// Compare the old comparison workflow with the compact route on the same
// synthetic database. Setup and both warm-up calls are outside timing.
func BenchmarkPacketSummaryComparison(b *testing.B) {
	db, q := packetSummaryFixture(b, 128000)
	since, _ := time.Parse(time.RFC3339, q.Since)
	legacy, err := db.QueryPackets(q)
	if err != nil {
		b.Fatal(err)
	}
	summary, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
	if err != nil {
		b.Fatal(err)
	}
	if len(legacy.Packets) != len(summary.Packets) || summary.Truncated != (legacy.Total > q.Limit) {
		b.Fatal("comparison coverage changed")
	}
	for i, packet := range summary.Packets {
		if packet.Hash != legacy.Packets[i]["hash"] || packet.Timestamp != legacy.Packets[i]["timestamp"] {
			b.Fatal("comparison order or timestamp changed")
		}
	}
	b.Logf("128000 transmissions, 2048000 observations, 128 observers; legacy total=%d page=%d truncated=%v", legacy.Total, len(summary.Packets), summary.Truncated)
	b.Run("legacy-packets", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result, err := db.QueryPackets(q)
			if err != nil || len(result.Packets) != len(summary.Packets) {
				b.Fatalf("legacy: %v", err)
			}
		}
	})
	b.Run("summary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			result, err := db.QueryPacketSummary(context.Background(), q.Observer, since, q.Limit)
			if err != nil || len(result.Packets) != len(summary.Packets) || !result.Truncated {
				b.Fatalf("summary: %v", err)
			}
		}
	})
}
