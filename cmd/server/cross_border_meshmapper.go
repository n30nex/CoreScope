package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

// MeshMapper is external metadata. It never substitutes for locally observed
// packet forwarding, and these lists must pass local privacy filters before use.
type CrossBorderMeshMapperSnapshot struct {
	Status    string                          `json:"status"`
	FetchedAt *string                         `json:"fetchedAt"`
	CheckedAt *string                         `json:"checkedAt"`
	Partial   bool                            `json:"partial"`
	Regions   []CrossBorderMeshMapperRegion   `json:"regions"`
	Repeaters []CrossBorderMeshMapperRepeater `json:"repeaters"`
	Scopes    []CrossBorderMeshMapperScope    `json:"scopes"`
	Revision  uint64                          `json:"-"`
}

type CrossBorderMeshMapperRegion struct {
	Code               string  `json:"code"`
	Status             string  `json:"status"`
	ErrorCode          string  `json:"errorCode,omitempty"`
	RepeatersFetchedAt *string `json:"repeatersFetchedAt"`
	ScopesFetchedAt    *string `json:"scopesFetchedAt"`
}

type CrossBorderMeshMapperScope struct {
	Name               string `json:"name"`
	Region             string `json:"region"`
	Repeaters          int    `json:"repeaters"`
	DefaultAdvertisers int    `json:"defaultAdvertisers"`
	Monitored          bool   `json:"monitored"`
	Wardriving         bool   `json:"wardriving"`
}

type CrossBorderMeshMapperScopeEvidence struct {
	Name            string   `json:"name"`
	ObservedAt      *string  `json:"observedAt"`
	ObservedPackets int      `json:"observedPackets"`
	Proof           string   `json:"proof,omitempty"`
	ReportedAt      *string  `json:"reportedAt"`
	UploadedAt      *string  `json:"uploadedAt"`
	SourceRegions   []string `json:"sourceRegions"`
}

type CrossBorderMeshMapperRepeater struct {
	PublicKey          string                               `json:"publicKey"`
	Name               string                               `json:"name"`
	Location           *CrossBorderLocation                 `json:"location"`
	LastHeardAt        *string                              `json:"lastHeardAt"`
	IATA               string                               `json:"iata"`
	Enabled            int                                  `json:"enabled"`
	Mobile             bool                                 `json:"mobile"`
	AdvertBytes        int                                  `json:"advertBytes"`
	SourceRegions      []string                             `json:"sourceRegions"`
	Scopes             []CrossBorderMeshMapperScopeEvidence `json:"scopes"`
	ScopesCheckedAt    *string                              `json:"scopesCheckedAt"`
	DefaultScope       *string                              `json:"defaultScope"`
	DefaultScopeState  string                               `json:"defaultScopeState,omitempty"`
	DefaultScopeAt     *string                              `json:"defaultScopeAt"`
	DefaultScopeSource string                               `json:"defaultScopeSource,omitempty"`
}

const (
	meshMapperRefresh           = 30 * time.Minute
	meshMapperMaxRegions        = 8
	meshMapperMaxBytes          = 8 << 20
	meshMapperMaxRepeaters      = 4096
	meshMapperMaxScopes         = 256
	meshMapperMaxRepeaterScopes = 64
)

// One provider belongs to one immutable credential/region assignment. Replace
// it rather than reusing its cached data when either configuration changes.
type crossBorderMeshMapper struct {
	client      *http.Client
	regions     []string
	key         string
	refreshing  chan struct{}
	nextAttempt time.Time
	copies      map[string]meshMapperRegionCopy
	snapshot    atomic.Pointer[CrossBorderMeshMapperSnapshot]
	onChange    func()
}

type meshMapperRegionCopy struct {
	repeaters                                []CrossBorderMeshMapperRepeater
	scopes                                   []CrossBorderMeshMapperScope
	repeatersAt, scopesAt                    time.Time
	repeatersStatus, scopesStatus, errorCode string
	repeatersPartial, scopesPartial          bool
}

func newCrossBorderMeshMapper(regions []string, key string) (*crossBorderMeshMapper, error) {
	seen := map[string]bool{}
	codes := []string{}
	for _, region := range regions {
		code := strings.ToUpper(strings.TrimSpace(region))
		if len(code) < 3 || len(code) > 8 {
			return nil, errors.New("invalid MeshMapper region code")
		}
		for _, c := range code {
			if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
				return nil, errors.New("invalid MeshMapper region code")
			}
		}
		if !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
		if len(codes) > meshMapperMaxRegions {
			return nil, errors.New("too many MeshMapper regions")
		}
	}
	sort.Strings(codes)
	p := &crossBorderMeshMapper{regions: codes, key: strings.TrimSpace(key), refreshing: make(chan struct{}, 1), copies: map[string]meshMapperRegionCopy{}}
	p.client = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := "loading"
	if p.key == "" || len(codes) == 0 {
		status = "disabled"
	}
	p.snapshot.Store(&CrossBorderMeshMapperSnapshot{Status: status, Regions: []CrossBorderMeshMapperRegion{}, Repeaters: []CrossBorderMeshMapperRepeater{}, Scopes: []CrossBorderMeshMapperScope{}})
	return p, nil
}

func (p *crossBorderMeshMapper) Run(ctx context.Context) {
	if p == nil || p.key == "" || len(p.regions) == 0 {
		return
	}
	for {
		p.refresh(ctx, time.Now().UTC())
		timer := time.NewTimer(meshMapperRefresh)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Refresh is deliberately independent of visitor requests and the traffic
// polling interval. Eight regions cost at most 768 admitted reads/day at this
// cadence (including failed reads); HTTP redirects never carry the key.
func (p *crossBorderMeshMapper) refresh(ctx context.Context, now time.Time) {
	if p == nil || p.key == "" || len(p.regions) == 0 || ctx.Err() != nil {
		return
	}
	select {
	case p.refreshing <- struct{}{}:
		defer func() { <-p.refreshing }()
	default:
		return
	}
	if now.Before(p.nextAttempt) {
		return
	}
	p.nextAttempt = now.Add(meshMapperRefresh)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	successes := 0
	for _, region := range p.regions {
		copy := p.copies[region]
		copy.errorCode = ""
		for _, endpoint := range []string{"get_repeaters.php", "get_scopes.php"} {
			body, status, code, retryAt := p.fetch(ctx, region, endpoint, now)
			if retryAt.After(p.nextAttempt) {
				p.nextAttempt = retryAt
			}
			if status == "access_denied" {
				// Access is no longer established. A TTL must not keep a previously
				// authorized external identity or scope snapshot publicly visible.
				clear(p.copies)
				p.publish(now, "access_denied", code, 0)
				return
			}
			if endpoint == "get_repeaters.php" {
				if status == "ready" {
					rows, partial, err := meshMapperDecodeRepeaters(body, now)
					if err != nil {
						status, code = "unavailable", "invalid_response"
					} else {
						copy.repeaters, copy.repeatersAt, copy.repeatersPartial = rows, now, partial
						successes++
					}
				}
				copy.repeatersStatus = status
			} else {
				if status == "ready" {
					rows, partial, err := meshMapperDecodeScopes(body, region)
					if err != nil {
						status, code = "unavailable", "invalid_response"
					} else {
						copy.scopes, copy.scopesAt, copy.scopesPartial = rows, now, partial
						successes++
					}
				}
				copy.scopesStatus = status
			}
			if code != "" {
				copy.errorCode = code
			}
			p.copies[region] = copy
			if status == "rate_limited" {
				p.publish(now, status, code, successes)
				return
			}
		}
	}
	p.publish(now, "", "", successes)
}

func (p *crossBorderMeshMapper) fetch(ctx context.Context, region, endpoint string, now time.Time) ([]byte, string, string, time.Time) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+strings.ToLower(region)+".meshmapper.net/"+endpoint, nil)
	if err != nil {
		return nil, "unavailable", "request_failed", time.Time{}
	}
	req.Header.Set("X-API-Key", p.key)
	// Go's default transport requests and transparently decompresses gzip. The
	// limit below therefore applies to the uncompressed body, not the wire size.
	req.Header.Set("Accept", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, "unavailable", "request_failed", time.Time{}
	}
	defer res.Body.Close()
	retryAt := meshMapperRetryAt(res.Header.Get("Retry-After"), now)
	if res.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure)
		code := "upstream_unavailable"
		switch failure.Error {
		case "missing_key", "invalid_key", "wrong_key_type", "api_not_allowed", "no_region", "region_not_allowed", "ip_not_allowed", "key_rate_limited", "slow_down", "rate_limited", "auth_unavailable", "zone_not_found":
			code = failure.Error
		}
		if res.StatusCode == 401 || res.StatusCode == 403 {
			return nil, "access_denied", code, retryAt
		}
		if res.StatusCode == 429 || code == "key_rate_limited" || code == "slow_down" || code == "rate_limited" {
			return nil, "rate_limited", code, retryAt
		}
		return nil, "unavailable", code, retryAt
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, meshMapperMaxBytes+1))
	if err != nil || len(body) > meshMapperMaxBytes || !json.Valid(body) {
		return nil, "unavailable", "invalid_response", retryAt
	}
	return body, "ready", "", retryAt
}

func meshMapperRetryAt(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if when, err := http.ParseTime(value); err == nil {
		return when
	}
	return time.Time{}
}

// Arrays are decoded one item at a time, so an 8 MiB response containing a
// huge number of tiny objects cannot allocate an unbounded typed slice.
func meshMapperDecodeList[T any](data []byte, limit int) ([]T, bool, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('[') {
		return nil, false, errors.New("expected list")
	}
	items := []T{}
	for d.More() {
		if len(items) == limit {
			return items, true, nil
		}
		var item T
		if err := d.Decode(&item); err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	return items, false, nil
}

type meshMapperRepeaterWire struct {
	PublicKey          string          `json:"hex_id"`
	Name               string          `json:"name"`
	Lat                json.Number     `json:"lat"`
	Lon                json.Number     `json:"lon"`
	LastHeard          json.Number     `json:"last_heard"`
	IATA               string          `json:"iata"`
	Enabled            json.Number     `json:"enabled"`
	Mobile             json.Number     `json:"is_carpeater"`
	AdvertBytes        json.Number     `json:"advert_bytes"`
	Scopes             json.RawMessage `json:"scopes"`
	ScopesCheckedAt    json.Number     `json:"scopes_checked_at"`
	DefaultScope       *string         `json:"default_scope"`
	DefaultScopeState  string          `json:"default_scope_state"`
	DefaultScopeAt     json.Number     `json:"default_scope_at"`
	DefaultScopeSource string          `json:"default_scope_src"`
}

type meshMapperScopeWire struct {
	Name            string      `json:"scope"`
	ObservedAt      json.Number `json:"observed_at"`
	ObservedPackets json.Number `json:"observed_pkts"`
	Proof           string      `json:"proof"`
	ReportedAt      json.Number `json:"reported_at"`
	UploadedAt      json.Number `json:"uploaded_at"`
}

func meshMapperStamp(n json.Number, now time.Time) *string {
	value, err := n.Int64()
	if err != nil || value <= 0 || value > now.Add(5*time.Minute).Unix() {
		return nil
	}
	s := time.Unix(value, 0).UTC().Format(time.RFC3339)
	return &s
}

func meshMapperCount(n json.Number) int {
	value, err := n.Int64()
	if err != nil || value < 0 || value > 1<<31-1 {
		return 0
	}
	return int(value)
}

func meshMapperScopeName(s string) (string, bool) {
	s = normScope(s)
	if s == "" || len(s) > 64 {
		return "", false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return "", false
		}
	}
	return s, true
}

func meshMapperDecodeRepeaters(body []byte, now time.Time) ([]CrossBorderMeshMapperRepeater, bool, error) {
	rows, partial, err := meshMapperDecodeList[meshMapperRepeaterWire](body, meshMapperMaxRepeaters)
	if err != nil {
		return nil, false, err
	}
	result := make([]CrossBorderMeshMapperRepeater, 0, len(rows))
	for _, raw := range rows {
		if len(raw.PublicKey) != 64 {
			partial = true
			continue
		}
		if _, err := hex.DecodeString(raw.PublicKey); err != nil {
			partial = true
			continue
		}
		row := CrossBorderMeshMapperRepeater{PublicKey: strings.ToLower(raw.PublicKey), Name: raw.Name, IATA: raw.IATA, Enabled: meshMapperCount(raw.Enabled), Mobile: meshMapperCount(raw.Mobile) == 1, AdvertBytes: meshMapperCount(raw.AdvertBytes), LastHeardAt: meshMapperStamp(raw.LastHeard, now), ScopesCheckedAt: meshMapperStamp(raw.ScopesCheckedAt, now), SourceRegions: []string{}, Scopes: []CrossBorderMeshMapperScopeEvidence{}}
		if len(row.Name) > 128 {
			row.Name = row.Name[:128]
		}
		if !validCrossBorderIATA(row.IATA) {
			row.IATA = ""
		}
		if row.Enabled > 3 {
			row.Enabled = 0
		}
		if row.AdvertBytes < 1 || row.AdvertBytes > 3 {
			row.AdvertBytes = 0
		}
		lat, e1 := raw.Lat.Float64()
		lon, e2 := raw.Lon.Float64()
		if e1 == nil && e2 == nil && !math.IsNaN(lat) && !math.IsNaN(lon) && math.Abs(lat) <= 90 && math.Abs(lon) <= 180 && (lat != 0 || lon != 0) {
			row.Location = &CrossBorderLocation{Lat: lat, Lon: lon}
		}
		if raw.DefaultScopeState == "named" || raw.DefaultScopeState == "unknown" || raw.DefaultScopeState == "none" {
			row.DefaultScopeState, row.DefaultScopeAt = raw.DefaultScopeState, meshMapperStamp(raw.DefaultScopeAt, now)
			if raw.DefaultScopeSource == "advert" || raw.DefaultScopeSource == "reported" {
				row.DefaultScopeSource = raw.DefaultScopeSource
			}
			if raw.DefaultScopeState == "named" {
				row.DefaultScopeState = "unknown"
				if raw.DefaultScope != nil {
					if name, valid := meshMapperScopeName(*raw.DefaultScope); valid && name != "*" {
						row.DefaultScope, row.DefaultScopeState = &name, "named"
					}
				}
			}
		}
		if len(raw.Scopes) > 0 && string(raw.Scopes) != "null" {
			scopes, capped, err := meshMapperDecodeList[meshMapperScopeWire](raw.Scopes, meshMapperMaxRepeaterScopes)
			if err != nil {
				return nil, false, err
			}
			partial = partial || capped
			for _, scope := range scopes {
				name, valid := meshMapperScopeName(scope.Name)
				if !valid {
					partial = true
					continue
				}
				value := CrossBorderMeshMapperScopeEvidence{Name: name, ObservedAt: meshMapperStamp(scope.ObservedAt, now), ObservedPackets: meshMapperCount(scope.ObservedPackets), ReportedAt: meshMapperStamp(scope.ReportedAt, now), UploadedAt: meshMapperStamp(scope.UploadedAt, now), SourceRegions: []string{}}
				if scope.Proof == "multibyte" || scope.Proof == "floor" {
					value.Proof = scope.Proof
				}
				row.Scopes = append(row.Scopes, value)
			}
			sort.Slice(row.Scopes, func(i, j int) bool { return row.Scopes[i].Name < row.Scopes[j].Name })
		}
		result = append(result, row)
	}
	return result, partial, nil
}

func meshMapperDecodeScopes(body []byte, region string) ([]CrossBorderMeshMapperScope, bool, error) {
	var raw struct {
		Region string          `json:"region"`
		Scopes json.RawMessage `json:"scopes"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Region != region {
		return nil, false, errors.New("invalid scope region")
	}
	type entry struct {
		Name       string      `json:"name"`
		Repeaters  json.Number `json:"repeaters"`
		Default    json.Number `json:"default"`
		Monitored  bool        `json:"monitored"`
		Wardriving bool        `json:"wardriving"`
	}
	rows, partial, err := meshMapperDecodeList[entry](raw.Scopes, meshMapperMaxScopes)
	if err != nil {
		return nil, false, err
	}
	result := make([]CrossBorderMeshMapperScope, 0, len(rows))
	for _, row := range rows {
		name, valid := meshMapperScopeName(row.Name)
		if !valid || name == "*" {
			continue
		}
		result = append(result, CrossBorderMeshMapperScope{Name: name, Region: region, Repeaters: meshMapperCount(row.Repeaters), DefaultAdvertisers: meshMapperCount(row.Default), Monitored: row.Monitored, Wardriving: row.Wardriving})
	}
	return result, partial, nil
}

func (p *crossBorderMeshMapper) publish(now time.Time, override, errorCode string, successes int) {
	stamp := now.UTC().Format(time.RFC3339)
	s := CrossBorderMeshMapperSnapshot{Status: "ready", CheckedAt: &stamp, Regions: []CrossBorderMeshMapperRegion{}, Repeaters: []CrossBorderMeshMapperRepeater{}, Scopes: []CrossBorderMeshMapperScope{}, Revision: p.Revision() + 1}
	byKey := map[string]int{}
	var oldest time.Time
	for _, code := range p.regions {
		c := p.copies[code]
		row := CrossBorderMeshMapperRegion{Code: code, Status: "ready", ErrorCode: c.errorCode}
		if c.repeatersStatus != "ready" || c.scopesStatus != "ready" {
			row.Status = "unavailable"
			s.Partial = true
		}
		for _, part := range []struct {
			at   time.Time
			dest **string
		}{{c.repeatersAt, &row.RepeatersFetchedAt}, {c.scopesAt, &row.ScopesFetchedAt}} {
			if !part.at.IsZero() {
				v := part.at.UTC().Format(time.RFC3339)
				*part.dest = &v
				if oldest.IsZero() || part.at.Before(oldest) {
					oldest = part.at
				}
				if row.Status != "ready" {
					row.Status = "stale"
				}
			}
		}
		if override == "access_denied" {
			row.Status, row.ErrorCode = override, errorCode
		}
		s.Partial = s.Partial || c.repeatersPartial || c.scopesPartial
		s.Regions = append(s.Regions, row)
		for _, repeater := range c.repeaters {
			if idx, ok := byKey[repeater.PublicKey]; ok {
				if meshMapperMergeRepeater(&s.Repeaters[idx], repeater, code) {
					s.Partial = true
				}
			} else if len(s.Repeaters) < meshMapperMaxRepeaters {
				copy := meshMapperCloneRepeater(repeater)
				copy.SourceRegions = []string{code}
				for i := range copy.Scopes {
					copy.Scopes[i].SourceRegions = []string{code}
				}
				byKey[copy.PublicKey] = len(s.Repeaters)
				s.Repeaters = append(s.Repeaters, copy)
			} else {
				s.Partial = true
			}
		}
		// Counts stay per source; member/group populations can overlap.
		s.Scopes = append(s.Scopes, c.scopes...)
	}
	if !oldest.IsZero() {
		v := oldest.UTC().Format(time.RFC3339)
		s.FetchedAt = &v
	}
	if s.Partial {
		s.Status = "partial"
		if successes == 0 {
			s.Status = "stale"
		}
		if oldest.IsZero() {
			s.Status = "unavailable"
		}
	}
	if override != "" {
		s.Status, s.Partial = override, true
	}
	sort.Slice(s.Repeaters, func(i, j int) bool { return s.Repeaters[i].PublicKey < s.Repeaters[j].PublicKey })
	p.snapshot.Store(&s)
	if p.onChange != nil {
		p.onChange()
	}
}

func meshMapperLater(a, b *string) bool { return b != nil && (a == nil || *b > *a) }

func meshMapperMergeRepeater(dst *CrossBorderMeshMapperRepeater, src CrossBorderMeshMapperRepeater, region string) bool {
	dst.SourceRegions = meshMapperAddRegion(dst.SourceRegions, region)
	if meshMapperLater(dst.LastHeardAt, src.LastHeardAt) {
		dst.Name, dst.IATA, dst.LastHeardAt = src.Name, src.IATA, meshMapperClonePointer(src.LastHeardAt)
		dst.Location, dst.AdvertBytes, dst.Mobile, dst.Enabled = meshMapperCloneLocation(src.Location), src.AdvertBytes, src.Mobile, src.Enabled
	} else if dst.LastHeardAt != nil && src.LastHeardAt != nil && *dst.LastHeardAt == *src.LastHeardAt && dst.Location != nil && src.Location != nil && (dst.Location.Lat != src.Location.Lat || dst.Location.Lon != src.Location.Lon) {
		dst.Location = nil
	}
	if meshMapperLater(dst.ScopesCheckedAt, src.ScopesCheckedAt) {
		dst.ScopesCheckedAt = meshMapperClonePointer(src.ScopesCheckedAt)
	}
	if meshMapperLater(dst.DefaultScopeAt, src.DefaultScopeAt) {
		dst.DefaultScope, dst.DefaultScopeState, dst.DefaultScopeAt, dst.DefaultScopeSource = meshMapperClonePointer(src.DefaultScope), src.DefaultScopeState, meshMapperClonePointer(src.DefaultScopeAt), src.DefaultScopeSource
	} else if dst.DefaultScopeAt != nil && src.DefaultScopeAt != nil && *dst.DefaultScopeAt == *src.DefaultScopeAt {
		left, right := "", ""
		if dst.DefaultScope != nil {
			left = *dst.DefaultScope
		}
		if src.DefaultScope != nil {
			right = *src.DefaultScope
		}
		if left != right || dst.DefaultScopeState != src.DefaultScopeState {
			dst.DefaultScope, dst.DefaultScopeState, dst.DefaultScopeSource = nil, "conflict", ""
		}
	}
	indexes := map[string]int{}
	for i, scope := range dst.Scopes {
		indexes[scope.Name] = i
	}
	partial := false
	for _, scope := range src.Scopes {
		if i, ok := indexes[scope.Name]; ok {
			cur := &dst.Scopes[i]
			cur.SourceRegions = meshMapperAddRegion(cur.SourceRegions, region)
			if meshMapperLater(cur.ObservedAt, scope.ObservedAt) {
				cur.ObservedAt, cur.ObservedPackets, cur.Proof = meshMapperClonePointer(scope.ObservedAt), scope.ObservedPackets, scope.Proof
			}
			if meshMapperLater(cur.ReportedAt, scope.ReportedAt) {
				cur.ReportedAt = meshMapperClonePointer(scope.ReportedAt)
			}
			if meshMapperLater(cur.UploadedAt, scope.UploadedAt) {
				cur.UploadedAt = meshMapperClonePointer(scope.UploadedAt)
			}
		} else if len(dst.Scopes) < meshMapperMaxRepeaterScopes {
			indexes[scope.Name] = len(dst.Scopes)
			value := meshMapperCloneScope(scope)
			value.SourceRegions = []string{region}
			dst.Scopes = append(dst.Scopes, value)
		} else {
			partial = true
		}
	}
	sort.Slice(dst.Scopes, func(i, j int) bool { return dst.Scopes[i].Name < dst.Scopes[j].Name })
	return partial
}

func meshMapperAddRegion(regions []string, region string) []string {
	for _, known := range regions {
		if known == region {
			return regions
		}
	}
	if len(regions) < meshMapperMaxRegions {
		return append(regions, region)
	}
	return regions
}

func meshMapperClonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}
func meshMapperCloneLocation(p *CrossBorderLocation) *CrossBorderLocation {
	if p == nil {
		return nil
	}
	value := *p
	value.ReportedAt = meshMapperClonePointer(p.ReportedAt)
	return &value
}
func meshMapperCloneScope(s CrossBorderMeshMapperScopeEvidence) CrossBorderMeshMapperScopeEvidence {
	s.ObservedAt, s.ReportedAt, s.UploadedAt = meshMapperClonePointer(s.ObservedAt), meshMapperClonePointer(s.ReportedAt), meshMapperClonePointer(s.UploadedAt)
	s.SourceRegions = append([]string{}, s.SourceRegions...)
	return s
}
func meshMapperCloneRepeater(r CrossBorderMeshMapperRepeater) CrossBorderMeshMapperRepeater {
	r.Location, r.LastHeardAt = meshMapperCloneLocation(r.Location), meshMapperClonePointer(r.LastHeardAt)
	r.DefaultScope, r.DefaultScopeAt, r.ScopesCheckedAt = meshMapperClonePointer(r.DefaultScope), meshMapperClonePointer(r.DefaultScopeAt), meshMapperClonePointer(r.ScopesCheckedAt)
	r.SourceRegions = append([]string{}, r.SourceRegions...)
	r.Scopes = append([]CrossBorderMeshMapperScopeEvidence{}, r.Scopes...)
	for i := range r.Scopes {
		r.Scopes[i] = meshMapperCloneScope(r.Scopes[i])
	}
	return r
}

func (p *crossBorderMeshMapper) Snapshot() CrossBorderMeshMapperSnapshot {
	if p == nil {
		return CrossBorderMeshMapperSnapshot{Status: "disabled", Regions: []CrossBorderMeshMapperRegion{}, Repeaters: []CrossBorderMeshMapperRepeater{}, Scopes: []CrossBorderMeshMapperScope{}}
	}
	s := *p.snapshot.Load()
	s.FetchedAt, s.CheckedAt = meshMapperClonePointer(s.FetchedAt), meshMapperClonePointer(s.CheckedAt)
	s.Regions = append([]CrossBorderMeshMapperRegion{}, s.Regions...)
	for i := range s.Regions {
		s.Regions[i].RepeatersFetchedAt = meshMapperClonePointer(s.Regions[i].RepeatersFetchedAt)
		s.Regions[i].ScopesFetchedAt = meshMapperClonePointer(s.Regions[i].ScopesFetchedAt)
	}
	s.Repeaters = append([]CrossBorderMeshMapperRepeater{}, s.Repeaters...)
	for i := range s.Repeaters {
		s.Repeaters[i] = meshMapperCloneRepeater(s.Repeaters[i])
	}
	s.Scopes = append([]CrossBorderMeshMapperScope{}, s.Scopes...)
	return s
}
func (p *crossBorderMeshMapper) Revision() uint64 {
	if p == nil || p.snapshot.Load() == nil {
		return 0
	}
	return p.snapshot.Load().Revision
}
