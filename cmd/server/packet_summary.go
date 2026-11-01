package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
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
	// Reuse observer-ever membership. Time evidence can come from a different
	// observer: combining both predicates on one observation would change it.
	where, args := db.buildTransmissionWhere(PacketQuery{Observer: observer})
	where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM observations ot
		WHERE ot.transmission_id = t.id AND ot.timestamp >= %s LIMIT 1 OFFSET 0)`, db.parameter(len(args)+1)))
	args = append(args, since.Unix())
	query := fmt.Sprintf(`SELECT t.hash, t.payload_type, t.route_type, t.first_seen
		FROM transmissions t WHERE %s ORDER BY t.id DESC LIMIT %s`, strings.Join(where, " AND "), db.parameter(len(args)+1))
	return query, append(args, limit+1)
}

func (db *DB) QueryPacketSummary(ctx context.Context, observer string, since time.Time, limit int) (*PacketSummaryResult, error) {
	if limit < 1 || limit > packetSummaryMax {
		return nil, fmt.Errorf("invalid packet summary limit")
	}
	query, args := db.packetSummarySQL(observer, since, limit)
	rows, err := db.conn.QueryContext(ctx, query, args...)
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
