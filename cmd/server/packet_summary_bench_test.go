package main

import (
	"context"
	"fmt"
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
