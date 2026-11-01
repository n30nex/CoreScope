package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/meshcore-analyzer/dbconfig"
)

const packetSummaryMax = 10000
const packetSummaryTimeout = 15 * time.Second

type PacketSummary struct {
	Hash        string `json:"hash"`
	PayloadType *int   `json:"payload_type"`
	RouteType   *int   `json:"route_type"`
	Timestamp   string `json:"timestamp"`
}

type PacketSummaryResult struct {
	Packets   []PacketSummary `json:"packets"`
	Limit     int             `json:"limit"`
	Truncated bool            `json:"truncated"`
}

func (db *DB) packetSummarySQL(observer string, since time.Time, limit int) (string, []any) {
	observerMatch := "oi.observer_id = " + db.parameter(1)
	if db.isV3 {
		observerMatch = "oi.observer_idx = (SELECT rowid FROM observers WHERE id = " + db.parameter(1) + ")"
	}
	// Keep observer-ever membership and recent evidence independent. Materialize
	// IDs through time qualification and paging before loading packet fields;
	// otherwise the planner can fetch every historical packet heap row first.
	// Validate IDs before the cap so orphan observations cannot consume a slot.
	query := fmt.Sprintf(`WITH observer_ids AS MATERIALIZED (
		SELECT DISTINCT oi.transmission_id FROM observations oi WHERE %s
	), recent_ids AS MATERIALIZED (
		SELECT p.transmission_id FROM observer_ids p WHERE EXISTS (
			SELECT 1 FROM observations ot WHERE ot.transmission_id = p.transmission_id
			AND ot.timestamp >= %s ORDER BY ot.timestamp DESC LIMIT 1 OFFSET 0
		)
	), page_ids AS MATERIALIZED (
		SELECT p.transmission_id FROM recent_ids p WHERE EXISTS (
			SELECT 1 FROM transmissions valid WHERE valid.id = p.transmission_id LIMIT 1 OFFSET 0
		) ORDER BY p.transmission_id DESC LIMIT %s
	)
	SELECT t.hash, t.payload_type, t.route_type, t.first_seen
	FROM page_ids p JOIN transmissions t ON t.id = p.transmission_id
	ORDER BY p.transmission_id DESC`, observerMatch, db.parameter(2), db.parameter(3))
	return query, []any{strings.TrimSpace(observer), since.Unix(), limit + 1}
}

func (db *DB) QueryPacketSummary(ctx context.Context, observer string, since time.Time, limit int) (*PacketSummaryResult, error) {
	if limit < 1 || limit > packetSummaryMax {
		return nil, fmt.Errorf("invalid packet summary limit")
	}
	query, args := db.packetSummarySQL(observer, since, limit)
	var tx *sql.Tx
	var rows *sql.Rows
	var err error
	if db.Backend() == dbconfig.Postgres {
		// JIT setup can exceed this bounded lookup's latency budget. Scope the
		// setting to a read-only transaction; other requests retain their policy.
		tx, err = db.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "SET LOCAL jit = off"); err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, query, args...)
	} else {
		rows, err = db.conn.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &PacketSummaryResult{Packets: make([]PacketSummary, 0, limit+1), Limit: limit}
	for rows.Next() {
		var packet PacketSummary
		// Legacy packet responses use first_seen for timestamp, not the time
		// of the observation that satisfied the requested window.
		if err := rows.Scan(&packet.Hash, &packet.PayloadType, &packet.RouteType, &packet.Timestamp); err != nil {
			return nil, err
		}
		result.Packets = append(result.Packets, packet)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	if len(result.Packets) > limit {
		result.Packets = result.Packets[:limit]
		result.Truncated = true
	}
	return result, nil
}

func (s *Server) handlePacketSummary(w http.ResponseWriter, r *http.Request) {
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid summary query")
		return
	}
	for key, values := range params {
		if (key != "observer" && key != "since" && key != "limit") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "unsupported or repeated summary parameter")
			return
		}
	}
	observer := strings.TrimSpace(params.Get("observer"))
	if observer == "" || len(observer) > 256 || strings.Contains(observer, ",") || strings.IndexFunc(observer, unicode.IsControl) >= 0 {
		writeError(w, http.StatusBadRequest, "one observer identifier is required")
		return
	}
	since, err := time.Parse(time.RFC3339Nano, params.Get("since"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
		return
	}
	limit := packetSummaryMax
	if raw, ok := params["limit"]; ok {
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > packetSummaryMax {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 10000")
			return
		}
	}
	if s.cfg != nil && s.cfg.ListLimits != nil && s.cfg.ListLimits.PacketsMax > 0 {
		limit = min(limit, s.cfg.ListLimits.PacketsMax)
	}
	ctx, cancel := context.WithTimeout(r.Context(), packetSummaryTimeout)
	defer cancel()
	result, err := s.db.QueryPacketSummary(ctx, observer, since, limit)
	if err != nil {
		if ctx.Err() != nil {
			writeError(w, http.StatusGatewayTimeout, "packet comparison timed out")
		} else {
			writeError(w, http.StatusInternalServerError, "packet comparison is unavailable")
		}
		return
	}
	writeJSON(w, result)
}
