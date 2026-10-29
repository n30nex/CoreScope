package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (cfg *Config) GetCrossBorder() CrossBorderConfig {
	c := CrossBorderConfig{}
	if cfg != nil && cfg.CrossBorder != nil {
		c = *cfg.CrossBorder
	}
	positive := func(v *int, fallback int) {
		if *v <= 0 {
			*v = fallback
		}
	}
	positive(&c.DefaultWindowMinutes, 60)
	positive(&c.MaxWindowMinutes, 360)
	if c.MaxWindowMinutes > 360 {
		c.MaxWindowMinutes = 360
	}
	if c.MaxWindowMinutes < 15 {
		c.MaxWindowMinutes = 15
	}
	positive(&c.ObservationLimit, 5000)
	positive(&c.MetadataLimit, 500)
	positive(&c.PacketLimit, 100)
	positive(&c.ObservationsPerPacket, 8)
	positive(&c.QueryTimeoutSeconds, 3)
	positive(&c.CacheSeconds, 30)
	positive(&c.CacheEntries, 4)
	positive(&c.MaxConnections, 2)
	positive(&c.MaxConcurrentBuilds, 2)
	positive(&c.MaxSelectedIATA, 16)
	if c.MaxSelectedIATA > 128 {
		c.MaxSelectedIATA = 128
	}
	positive(&c.StaleAfterSeconds, 600)
	if strings.TrimSpace(c.LocalLabel) == "" {
		c.LocalLabel = "Local reference"
	}
	if strings.TrimSpace(c.PeerLabel) == "" {
		c.PeerLabel = "Peer telemetry"
	}
	c.AllowedLocalIATA = canonicalCrossBorderIATA(c.AllowedLocalIATA, 4096)
	c.DefaultLocalIATA = canonicalCrossBorderIATA(c.DefaultLocalIATA, c.MaxSelectedIATA)
	c.DefaultPeerIATA = canonicalCrossBorderIATA(c.DefaultPeerIATA, c.MaxSelectedIATA)
	allowedDefaults := []string{}
	for _, iata := range c.DefaultLocalIATA {
		if crossBorderContains(c.AllowedLocalIATA, iata) {
			allowedDefaults = append(allowedDefaults, iata)
		}
	}
	c.DefaultLocalIATA = allowedDefaults
	if c.DefaultWindowMinutes > c.MaxWindowMinutes {
		c.DefaultWindowMinutes = c.MaxWindowMinutes
	}
	requestedWindow := c.DefaultWindowMinutes
	c.DefaultWindowMinutes = 15
	for _, minutes := range []int{15, 60, 180, 360} {
		if minutes <= requestedWindow {
			c.DefaultWindowMinutes = minutes
		}
	}
	if p := c.PeerReferenceProfile; p != nil {
		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			c.PeerReferenceProfile = nil
		}
	}
	return c
}

func validCrossBorderIATA(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, b := range []byte(s) {
		if !(b >= 'A' && b <= 'Z') && !(b >= '0' && b <= '9') {
			return false
		}
	}
	return true
}

func canonicalCrossBorderIATA(values []string, limit int) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range values {
		s = strings.ToUpper(strings.TrimSpace(s))
		if validCrossBorderIATA(s) && !seen[s] {
			if len(out) >= limit {
				break
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func crossBorderContains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func parseCrossBorderFilters(q url.Values, cfg CrossBorderConfig) (CrossBorderFilters, error) {
	maxSelected := cfg.MaxSelectedIATA
	if maxSelected <= 0 {
		maxSelected = 16
	}
	parse := func(key string, defaults []string) ([]string, error) {
		if len(q[key]) > 1 {
			return nil, errors.New("duplicate filter")
		}
		s := strings.TrimSpace(q.Get(key))
		if s == "" {
			return append([]string{}, defaults...), nil
		}
		if len(s) > maxSelected*4 {
			return nil, errors.New("too many region selections")
		}
		parts := strings.Split(s, ",")
		if len(parts) > maxSelected {
			return nil, errors.New("too many region selections")
		}
		for _, p := range parts {
			if !validCrossBorderIATA(strings.ToUpper(strings.TrimSpace(p))) {
				return nil, errors.New("invalid IATA selection")
			}
		}
		return canonicalCrossBorderIATA(parts, maxSelected), nil
	}
	f := CrossBorderFilters{Scope: "all", WindowMinutes: cfg.DefaultWindowMinutes}
	f.View = strings.TrimSpace(q.Get("view"))
	if len(q["view"]) > 1 || (f.View != "" && f.View != "dashboard") {
		return f, errors.New("unsupported view")
	}
	var err error
	if f.LocalIATA, err = parse("localIata", cfg.DefaultLocalIATA); err != nil {
		return f, err
	}
	if f.PeerIATA, err = parse("peerIata", cfg.DefaultPeerIATA); err != nil {
		return f, err
	}
	for _, iata := range f.LocalIATA {
		if !crossBorderContains(cfg.AllowedLocalIATA, iata) {
			return f, errors.New("local region is outside the operator allowlist")
		}
	}
	if s := q.Get("windowMinutes"); s != "" {
		f.WindowMinutes, err = strconv.Atoi(s)
		if err != nil {
			return f, errors.New("invalid time window")
		}
	}
	if len(q["windowMinutes"]) > 1 || (f.WindowMinutes != 15 && f.WindowMinutes != 60 && f.WindowMinutes != 180 && f.WindowMinutes != 360) || f.WindowMinutes > cfg.MaxWindowMinutes {
		return f, errors.New("unsupported time window")
	}
	if s := strings.TrimSpace(q.Get("scope")); s != "" {
		f.Scope = normScope(s)
	}
	if len(q["scope"]) > 1 || len(f.Scope) > 128 || f.Scope == "" || isScopeWildcard(f.Scope) {
		return f, errors.New("invalid scope")
	}
	for _, c := range f.Scope {
		if c < 32 || c == 127 {
			return f, errors.New("invalid scope")
		}
	}
	return f, nil
}

type crossBorderCacheEntry struct {
	Response CrossBorderResponse
	At       time.Time
}
type crossBorderFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	response CrossBorderResponse
	err      error
}
type crossBorderService struct {
	cfg         CrossBorderConfig
	load        func(context.Context, CrossBorderFilters) (CrossBorderResponse, error)
	mu          sync.Mutex
	cache       map[string]crossBorderCacheEntry
	flights     map[string]*crossBorderFlight
	closed      bool
	local       *DB
	serverCfg   *Config
	peerURL     string
	peerMu      sync.Mutex
	peer        *DB
	peerOpening chan struct{}
	peerAttempt time.Time
	peerError   error
}

var errCrossBorderBusy = errors.New("cross-border report capacity reached")
var errCrossBorderUnavailable = errors.New("cross-border source unavailable")

func (svc *crossBorderService) response(ctx context.Context, filters CrossBorderFilters) (CrossBorderResponse, error) {
	if err := ctx.Err(); err != nil {
		return CrossBorderResponse{}, err
	}
	b, _ := json.Marshal(filters)
	// Privacy changes invalidate prior snapshots immediately, like the other
	// node-response caches; a configured TTL never delays a blacklist update.
	key := strconv.FormatUint(svc.serverCfg.BlacklistGeneration(), 10) + ":" + strconv.FormatUint(svc.serverCfg.HiddenNamePrefixesGeneration(), 10) + ":" + string(b)
	svc.mu.Lock()
	if svc.closed {
		svc.mu.Unlock()
		return CrossBorderResponse{}, errCrossBorderUnavailable
	}
	if cached, ok := svc.cache[key]; ok && time.Since(cached.At) < time.Duration(svc.cfg.CacheSeconds)*time.Second {
		svc.mu.Unlock()
		return cached.Response, nil
	}
	flight := svc.flights[key]
	if flight == nil {
		cap := svc.cfg.MaxConcurrentBuilds
		if cap <= 0 {
			cap = 2
		}
		if len(svc.flights) >= cap {
			svc.mu.Unlock()
			return CrossBorderResponse{}, errCrossBorderBusy
		}
		if svc.flights == nil {
			svc.flights = make(map[string]*crossBorderFlight)
		}
		timeout := svc.cfg.QueryTimeoutSeconds
		if timeout <= 0 {
			timeout = 3
		}
		buildCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		flight = &crossBorderFlight{done: make(chan struct{}), cancel: cancel}
		svc.flights[key] = flight
		go func(f *crossBorderFlight) {
			defer cancel()
			response, err := svc.load(buildCtx, filters)
			svc.mu.Lock()
			defer svc.mu.Unlock()
			if err == nil && !svc.closed {
				if svc.cache == nil {
					svc.cache = make(map[string]crossBorderCacheEntry)
				}
				cap := svc.cfg.CacheEntries
				if cap <= 0 {
					cap = 4
				}
				if _, exists := svc.cache[key]; !exists && len(svc.cache) >= cap {
					var oldestKey string
					var oldest time.Time
					for k, v := range svc.cache {
						if oldest.IsZero() || v.At.Before(oldest) {
							oldestKey = k
							oldest = v.At
						}
					}
					delete(svc.cache, oldestKey)
				}
				svc.cache[key] = crossBorderCacheEntry{Response: response, At: time.Now()}
			}
			f.response, f.err = response, err
			delete(svc.flights, key)
			close(f.done)
		}(flight)
	}
	svc.mu.Unlock()
	select {
	case <-ctx.Done():
		return CrossBorderResponse{}, ctx.Err()
	case <-flight.done:
		return flight.response, flight.err
	}
}

func newCrossBorderService(s *Server) *crossBorderService {
	svc := &crossBorderService{cfg: s.cfg.GetCrossBorder(), local: s.db, serverCfg: s.cfg, peerURL: os.Getenv("CORESCOPE_CROSS_BORDER_PEER_DATABASE_URL")}
	svc.load = svc.loadReport
	return svc
}

func (s *Server) closeCrossBorder() {
	if s == nil || s.crossBorder == nil {
		return
	}
	svc := s.crossBorder
	svc.mu.Lock()
	svc.closed = true
	for _, f := range svc.flights {
		f.cancel()
	}
	svc.cache = nil
	svc.mu.Unlock()
	svc.peerMu.Lock()
	peer := svc.peer
	svc.peer = nil
	svc.peerMu.Unlock()
	if peer != nil {
		peer.conn.Close()
	}
}

func (s *Server) handleCrossBorder(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.GetCrossBorder()
	if !cfg.Enabled {
		writeJSON(w, crossBorderBase(cfg, CrossBorderFilters{LocalIATA: []string{}, PeerIATA: []string{}, Scope: "all", WindowMinutes: cfg.DefaultWindowMinutes}, time.Now()))
		return
	}
	f, err := parseCrossBorderFilters(r.URL.Query(), cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid cross-network filter")
		return
	}
	base := crossBorderBase(cfg, f, time.Now())
	if len(f.LocalIATA) == 0 || len(f.PeerIATA) == 0 {
		base.Status = "selection_required"
		if len(f.LocalIATA) == 0 {
			base.Sources.Local.Status = "selection_required"
		}
		if len(f.PeerIATA) == 0 {
			base.Sources.Peer.Status = "selection_required"
		}
		base.Findings = append(base.Findings, CrossBorderFinding{Code: "selection_required", Severity: "info", Message: "Explicit local and peer observer-region selections are required."})
		writeJSON(w, base)
		return
	}
	if s.crossBorder == nil {
		writeJSON(w, base)
		return
	}
	result, err := s.crossBorder.response(r.Context(), f)
	if err != nil {
		code, message := "source_unavailable", "A source could not be read. Its connection details are private to the operator."
		if errors.Is(err, errCrossBorderBusy) {
			code = "busy"
			message = "Report capacity is busy. Retry shortly; no new report was queued."
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = "query_timeout"
			message = "The bounded report deadline expired. Retry with a shorter window."
		}
		base.Findings = append(base.Findings, CrossBorderFinding{Code: code, Severity: "warning", Message: message})
		writeJSON(w, base)
		return
	}
	writeJSON(w, result)
}

func (svc *crossBorderService) loadReport(ctx context.Context, f CrossBorderFilters) (CrossBorderResponse, error) {
	if f.View == "dashboard" {
		return svc.loadDashboard(ctx, f)
	}
	now := time.Now().UTC()
	since := now.Add(-time.Duration(f.WindowMinutes) * time.Minute)
	peerDB, err := svc.peerReader(ctx)
	if err != nil {
		return CrossBorderResponse{}, err
	}
	peer, err := peerDB.readCrossBorder(ctx, f.PeerIATA, since, now, svc.cfg, nil)
	if err != nil {
		return CrossBorderResponse{}, err
	}
	if err := applyCrossBorderPrivacy(ctx, peerDB, &peer, svc.serverCfg); err != nil {
		return CrossBorderResponse{}, err
	}
	hashSet := map[string]bool{}
	for _, row := range peer.Observations {
		if strings.EqualFold(strings.TrimSpace(row.Direction), "tx") {
			continue
		}
		wire, valid := crossBorderReadWire(row)
		if valid && crossBorderScopeMatches(wire.scope, f.Scope) {
			hashSet[row.Hash] = true
		}
	}
	hashes := make([]string, 0, len(hashSet))
	for hash := range hashSet {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	local, err := svc.local.readCrossBorder(ctx, f.LocalIATA, since, now, svc.cfg, hashes)
	if err != nil {
		if ctx.Err() != nil {
			return CrossBorderResponse{}, ctx.Err()
		}
		local = crossBorderSnapshot{}
	}
	if local.Available {
		if err := applyCrossBorderPrivacy(ctx, svc.local, &local, svc.serverCfg); err != nil {
			return CrossBorderResponse{}, err
		}
		// Display metadata is capped and region-selected. Check the bounded
		// candidate IDs against each opposite source's full observer metadata.
		local.SharedIdentityChecked = true
		peerIDs, peerErr := svc.local.crossBorderSharedIDs(ctx, crossBorderSnapshotIDs(peer))
		localIDs, localErr := peerDB.crossBorderSharedIDs(ctx, crossBorderSnapshotIDs(local))
		if ctx.Err() != nil {
			return CrossBorderResponse{}, ctx.Err()
		}
		local.SharedObserverIDs = append(peerIDs, localIDs...)
		local.SharedIdentityComplete = peerErr == nil && localErr == nil
	}
	return buildCrossBorder(ctx, svc.cfg, f, local, peer, now)
}

func crossBorderSnapshotIDs(snapshot crossBorderSnapshot) map[string]bool {
	ids := map[string]bool{}
	for _, o := range snapshot.Observers {
		ids[crossBorderID(o.ID)] = true
	}
	for _, o := range snapshot.Observations {
		ids[crossBorderID(o.ObserverID)] = true
	}
	return ids
}

func applyCrossBorderPrivacy(ctx context.Context, db *DB, snapshot *crossBorderSnapshot, cfg *Config) error {
	if cfg == nil {
		return nil
	}
	ids := crossBorderSnapshotIDs(*snapshot)
	for _, n := range snapshot.Nodes {
		ids[crossBorderID(n.PublicKey)] = true
	}
	hidden, err := db.crossBorderHiddenIDs(ctx, ids, cfg)
	if err != nil {
		return err
	}
	for i := range snapshot.Nodes {
		n := &snapshot.Nodes[i]
		n.Hidden = cfg.IsBlacklisted(n.PublicKey) || cfg.IsNameHidden(n.Name) || hidden[crossBorderID(n.PublicKey)]
		if n.Hidden {
			hidden[crossBorderID(n.PublicKey)] = true
		}
	}
	observers := snapshot.Observers[:0]
	for _, o := range snapshot.Observers {
		if cfg.IsBlacklisted(o.ID) || cfg.IsNameHidden(o.Name) || hidden[crossBorderID(o.ID)] {
			hidden[crossBorderID(o.ID)] = true
			continue
		}
		observers = append(observers, o)
	}
	snapshot.Observers = observers
	rows := snapshot.Observations[:0]
	for _, row := range snapshot.Observations {
		if !hidden[crossBorderID(row.ObserverID)] && !cfg.IsBlacklisted(row.ObserverID) {
			rows = append(rows, row)
		}
	}
	snapshot.Observations = rows
	return nil
}
