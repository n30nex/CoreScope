package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPacketSummaryRegisteredProjection(t *testing.T) {
	_, router := setupTestServer(t)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/packets/summary?observer=obs1&since=2000-01-01T00:00:00Z&limit=1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("summary route: status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Packets   []map[string]any `json:"packets"`
		Limit     int              `json:"limit"`
		Truncated *bool            `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Limit != 1 || len(response.Packets) != 1 || response.Truncated == nil || !*response.Truncated {
		t.Fatalf("cap metadata: %+v", response)
	}
	for _, packet := range response.Packets {
		if len(packet) != 4 {
			t.Fatalf("unexpected summary fields: %v", packet)
		}
		for _, key := range []string{"hash", "payload_type", "route_type", "timestamp"} {
			if _, ok := packet[key]; !ok {
				t.Fatalf("missing summary field %s", key)
			}
		}
	}
}

func TestPacketSummaryOpenAPI(t *testing.T) {
	meta, ok := routeDescriptions()["GET /api/packets/summary"]
	if !ok || len(meta.QueryParams) != 3 || meta.Description == "" {
		t.Fatalf("summary API documentation is missing: %+v", meta)
	}
}

func TestPacketSummaryMatchesLegacyMembershipAndTimestamp(t *testing.T) {
	db := setupTestDB(t)
	for _, query := range []string{
		`INSERT INTO observers(rowid,id,name) VALUES(1,'OBSERVER-A','A'),(2,'observer-b','B')`,
		`INSERT INTO transmissions(id,hash,raw_hex,first_seen,payload_type,route_type) VALUES
		 (1,'old-a-recent-b','AA','2020-01-01T00:00:00Z',5,1),
		 (2,'selected-old-only','AA','2020-01-02T00:00:00Z',5,1),
		 (3,'other-observer-only','AA','2020-01-03T00:00:00Z',5,1),
		 (4,'inclusive-cutoff','AA','2020-01-04T00:00:00Z',5,1),
		 (5,'duplicate-observations','AA','2020-01-05T00:00:00Z',NULL,NULL)`,
		`INSERT INTO observations(transmission_id,observer_idx,timestamp,path_json,direction) VALUES
		 (1,1,1700000000,'[]','tx'),(1,2,1700000200,'["AA"]','rx'),
		 (2,1,1700000000,'[]','rx'),(3,2,1700000200,'[]','rx'),
		 (4,1,1700000150,'[]','rx'),(5,1,1700000200,'[]','rx'),(5,1,1700000201,'["BB"]','rx')`,
	} {
		if _, err := db.conn.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Unix(1700000150, 0).UTC()
	for _, observer := range []string{"OBSERVER-A", "observer-b", "observer-a", "missing"} {
		for _, limit := range []int{1, 3, 10} {
			t.Run(fmt.Sprintf("%s/%d", observer, limit), func(t *testing.T) {
				legacy, err := db.QueryPackets(PacketQuery{Observer: observer, Since: since.Format(time.RFC3339), Limit: limit, Order: "DESC"})
				if err != nil {
					t.Fatal(err)
				}
				summary, err := db.QueryPacketSummary(context.Background(), observer, since, limit)
				if err != nil || summary == nil {
					t.Fatal(err)
				}
				if summary.Packets == nil || summary.Limit != limit || summary.Truncated != (legacy.Total > limit) || len(summary.Packets) != len(legacy.Packets) {
					t.Fatalf("summary=%+v legacy count=%d rows=%d", summary, legacy.Total, len(legacy.Packets))
				}
				for i, packet := range summary.Packets {
					before := legacy.Packets[i]
					if packet.Hash != before["hash"] || packet.Timestamp != before["timestamp"] {
						t.Fatalf("membership/order/first_seen changed: %+v versus %v", packet, before)
					}
					for name, value := range map[string]*int{"payload_type": packet.PayloadType, "route_type": packet.RouteType} {
						if (before[name] == nil) != (value == nil) || (value != nil && *value != before[name].(int)) {
							t.Fatalf("nullable %s changed", name)
						}
					}
				}
			})
		}
	}
}

func TestPacketSummaryValidationAndEmptyMetadata(t *testing.T) {
	srv, router := setupTestServer(t)
	valid := "/api/packets/summary?observer=obs1&since=2000-01-01T00:00:00Z"
	for _, path := range []string{
		"/api/packets/summary", strings.Replace(valid, "obs1", "", 1), strings.Replace(valid, "obs1", "a,b", 1),
		strings.Replace(valid, "obs1", strings.Repeat("x", 257), 1), strings.Replace(valid, "obs1", "%0A", 1),
		strings.Replace(valid, "2000-01-01T00:00:00Z", "yesterday", 1), valid + "&observer=obs2", valid + "&since=2001-01-01T00:00:00Z",
		valid + "&limit=0", valid + "&limit=-1", valid + "&limit=10001", valid + "&limit=abc", valid + "&limit=", valid + "&limit=1&limit=2", valid + "&until=2026-01-01T00:00:00Z",
		valid + "&broken=%zz", valid + "&broken=a;b",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid summary query returned %d: %s", w.Code, path)
		}
	}
	srv.cfg.ListLimits.PacketsMax = 2
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", strings.Replace(valid, "obs1", "missing", 1)+"&limit=10000", nil))
	var response struct {
		Packets   []PacketSummary `json:"packets"`
		Limit     int             `json:"limit"`
		Truncated *bool           `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.Limit != 2 || response.Packets == nil || len(response.Packets) != 0 || response.Truncated == nil || *response.Truncated {
		t.Fatalf("empty response or operator limit changed: %s", w.Body.String())
	}
}

func TestPacketSummaryCancellationAndDeadline(t *testing.T) {
	db := setupTestDB(t)
	// Occupy the fixture's only connection: cancellation must also bound pool
	// acquisition, without starting a long-running query or changing DB state.
	connection, err := db.conn.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = db.QueryPacketSummary(ctx, "observer", time.Now(), 10)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("summary did not honor request deadline: %v", err)
	}
	srv := NewServer(db, &Config{}, NewHub())
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	srv.handlePacketSummary(w, httptest.NewRequest("GET", "/api/packets/summary?observer=observer&since=2000-01-01T00:00:00Z", nil).WithContext(ctx))
	if w.Code != http.StatusGatewayTimeout || strings.Contains(w.Body.String(), "postgres") {
		t.Fatalf("cancelled request was not reported safely: %d %s", w.Code, w.Body.String())
	}
}

func TestPacketSummaryBoundedPlan(t *testing.T) {
	postgresOnly(t)
	db, q := packetSummaryFixture(t, 8000)
	since, _ := time.Parse(time.RFC3339, q.Since)
	query, args := db.packetSummarySQL(q.Observer, since, 7)
	var raw string
	if err := db.conn.QueryRow("EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan map[string]any }
	if err := json.Unmarshal([]byte(raw), &plans); err != nil {
		t.Fatal(err)
	}
	if rows := plans[0].Plan["Actual Rows"].(float64); rows != 8 {
		t.Fatalf("fixture must exercise exactly cap+1 rows, got %v", rows)
	}
	found := false
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		if node["Relation Name"] == "observations" {
			index, _ := node["Index Cond"].(string)
			filter, _ := node["Filter"].(string)
			if node["Node Type"] == "Seq Scan" {
				t.Error("summary scans the global observation table")
			}
			if strings.Contains(index+filter, "timestamp") {
				found = true
				if !strings.Contains(index, "transmission_id") || node["Actual Rows"].(float64) > 1 {
					t.Errorf("time lookup is not a per-transmission existence seek: %v", node)
				}
			}
		}
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			walk(child.(map[string]any))
		}
	}
	walk(plans[0].Plan)
	if !found {
		t.Fatalf("fixture did not exercise its time-existence index lookup: %s", raw)
	}
}
