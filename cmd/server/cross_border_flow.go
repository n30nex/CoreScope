package main

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/lora"
)

// Flow is retained, received flood-path evidence at reported positions. It is
// not an origin-country classifier, an arrival counter, or an RF loss metric.
type CrossBorderFlow struct {
	Status              string                     `json:"status"`
	IdentityComplete    bool                       `json:"identityComplete"`
	EligibleContents    int                        `json:"eligibleContents"`
	UnresolvedContents  int                        `json:"unresolvedContents"`
	NonCrossingContents int                        `json:"nonCrossingContents"`
	PathsTruncated      bool                       `json:"pathsTruncated"`
	RepeatersTruncated  bool                       `json:"repeatersTruncated"`
	Directions          []CrossBorderFlowDirection `json:"directions"`
	Paths               []CrossBorderFlowPath      `json:"paths"`
	Repeaters           []CrossBorderFlowRepeater  `json:"repeaters"`
}

type CrossBorderFlowDirection struct {
	Key               string                      `json:"key"`
	Label             string                      `json:"label"`
	Contents          int                         `json:"contents"`
	Scoped            int                         `json:"scoped"`
	Unscoped          int                         `json:"unscoped"`
	TransportWildcard int                         `json:"transportWildcard"`
	Mixed             int                         `json:"mixed"`
	UnknownScopeName  int                         `json:"unknownScopeName"` // subset of Scoped
	FurthestKm        *float64                    `json:"furthestKm"`
	Repeaters         int                         `json:"repeaters"`
	Timeline          []CrossBorderFlowBucket     `json:"timeline"`
	Scopes            []CrossBorderFlowScopeCount `json:"scopes"` // overlapping content counts
}

type CrossBorderFlowBucket struct {
	At       string `json:"at"`
	Complete bool   `json:"complete"`
	Contents *int   `json:"contents"`
}

type CrossBorderFlowScopeCount struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Contents int    `json:"contents"`
}

type CrossBorderFlowHop struct {
	Index     int                  `json:"index"`
	PublicKey string               `json:"publicKey,omitempty"`
	Name      string               `json:"name,omitempty"`
	Country   string               `json:"country,omitempty"`
	Location  *CrossBorderLocation `json:"location"`
	Reason    string               `json:"reason,omitempty"`
	Kind      string               `json:"kind"`
	borderKm  *float64
}

type CrossBorderFlowCrossing struct {
	FromIndex int     `json:"fromIndex"`
	ToIndex   int     `json:"toIndex"`
	Direction string  `json:"direction"`
	LegKm     float64 `json:"legKm"`
}

type CrossBorderFlowPath struct {
	ID              string                    `json:"id"`
	Hash            string                    `json:"hash"`
	Directions      []string                  `json:"directions"`
	FirstRecordedAt string                    `json:"firstRecordedAt"`
	Scope           CrossBorderScope          `json:"scope"`
	ScopeKey        string                    `json:"scopeKey"`
	HopWidth        int                       `json:"hopWidth"`
	Hops            []CrossBorderFlowHop      `json:"hops"`
	Crossings       []CrossBorderFlowCrossing `json:"crossings"`
	DistanceKm      *float64                  `json:"distanceKm"`
	FurthestKm      *float64                  `json:"furthestKm"`
	AirtimeMs       *float64                  `json:"airtimeMs"`
	AirtimeSource   string                    `json:"airtimeSource"`
	SNR             *float64                  `json:"snr"`
	RSSI            *float64                  `json:"rssi"`
}

type CrossBorderFlowRepeater struct {
	PublicKey        string                      `json:"publicKey"`
	Name             string                      `json:"name"`
	Country          string                      `json:"country"`
	Location         *CrossBorderLocation        `json:"location"`
	Directions       []string                    `json:"directions"`
	Contents         int                         `json:"contents"`
	CrossingContents int                         `json:"crossingContents"`
	Scopes           []CrossBorderFlowScopeCount `json:"scopes"`
	LastRecordedAt   string                      `json:"lastRecordedAt"`
}

var crossBorderFlowKeys = [2]string{"ca_us", "us_ca"}

type crossBorderFlowNode struct {
	hop              CrossBorderFlowHop
	hidden, conflict bool
}
type crossBorderFlowRow struct {
	row  *crossBorderObservationRow
	wire crossBorderWire
}
type crossBorderFlowRepeaterWork struct {
	value      CrossBorderFlowRepeater
	directions uint8
	scopes     map[string]int
}
type crossBorderFlowContentRepeater struct {
	directions uint8
	crossing   bool
	scopes     map[string]bool
	at         int64
}

// A bounded min-heap retains the newest path samples while every eligible
// content contributes to the summaries. It never retains the full row scan.
type crossBorderFlowPaths []CrossBorderFlowPath

func (p crossBorderFlowPaths) Len() int { return len(p) }
func (p crossBorderFlowPaths) Less(i, j int) bool {
	if p[i].FirstRecordedAt != p[j].FirstRecordedAt {
		return p[i].FirstRecordedAt < p[j].FirstRecordedAt
	}
	return p[i].ID < p[j].ID
}
func (p crossBorderFlowPaths) Swap(i, j int) { p[i], p[j] = p[j], p[i] }
func (p *crossBorderFlowPaths) Push(v any)   { *p = append(*p, v.(CrossBorderFlowPath)) }
func (p *crossBorderFlowPaths) Pop() any {
	a := *p
	v := a[len(a)-1]
	a[len(a)-1] = CrossBorderFlowPath{}
	*p = a[:len(a)-1]
	return v
}

func buildCrossBorderFlow(ctx context.Context, cfg CrossBorderConfig, f CrossBorderFilters, local, peer crossBorderSnapshot, dashboard *CrossBorderDashboard, now time.Time) (*CrossBorderFlow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &CrossBorderFlow{Status: "empty", Paths: []CrossBorderFlowPath{}, Repeaters: []CrossBorderFlowRepeater{}, Directions: []CrossBorderFlowDirection{}}
	result.IdentityComplete = local.Available && peer.Available && local.DashboardNodeIndexComplete && peer.DashboardNodeIndexComplete && len(local.DashboardNodes) <= crossBorderDashboardKeys && len(peer.DashboardNodes) <= crossBorderDashboardKeys
	for i, label := range []string{"Canada → USA", "USA → Canada"} {
		d := CrossBorderFlowDirection{Key: crossBorderFlowKeys[i], Label: label, Timeline: []CrossBorderFlowBucket{}, Scopes: []CrossBorderFlowScopeCount{}}
		for j, left := range dashboard.Local.Timeline {
			p := CrossBorderFlowBucket{At: left.At}
			if result.IdentityComplete && left.Complete && j < len(dashboard.Peer.Timeline) && dashboard.Peer.Timeline[j].Complete {
				p.Complete = true
				p.Contents = dashboardInt(0)
			}
			d.Timeline = append(d.Timeline, p)
		}
		result.Directions = append(result.Directions, d)
	}
	if !local.Available || !peer.Available {
		result.Status = "unavailable"
	} else if !result.IdentityComplete || local.NodesTruncated || peer.NodesTruncated || local.ObservationsTruncated || peer.ObservationsTruncated {
		result.Status = "partial"
	}
	var nodes map[string]*crossBorderFlowNode
	var index map[string]string
	if result.IdentityComplete {
		var err error
		nodes, index, err = crossBorderFlowNodes(ctx, local, peer)
		if err != nil {
			return nil, err
		}
	}
	groups := map[string][]crossBorderFlowRow{}
	since := now.Add(-time.Duration(f.WindowMinutes) * time.Minute).Unix()
	for source, snapshot := range []crossBorderSnapshot{local, peer} {
		regions := f.LocalIATA
		if source == 1 {
			regions = f.PeerIATA
		}
		limit := crossBorderDashboardLimit(cfg)
		rows := snapshot.Observations
		if len(rows) > limit {
			rows = rows[:limit]
			result.Status = "partial"
		}
		for i := range rows {
			if i&127 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			r := &rows[i]
			if r.Timestamp < since || r.Timestamp > now.Unix() || r.Hash == "" || !strings.EqualFold(strings.TrimSpace(r.Direction), "rx") || !crossBorderContains(regions, strings.ToUpper(strings.TrimSpace(r.ObserverIATA))) {
				continue
			}
			w, ok := crossBorderReadWire(*r)
			if !ok || (w.route != RouteFlood && w.route != RouteTransportFlood) || !crossBorderScopeMatches(w.scope, f.Scope) {
				continue
			}
			groups[r.Hash] = append(groups[r.Hash], crossBorderFlowRow{r, w})
		}
	}
	result.EligibleContents = len(groups)
	pathLimit := cfg.PacketLimit
	if pathLimit <= 0 || pathLimit > 100 {
		pathLimit = 100
	}
	paths := &crossBorderFlowPaths{}
	scopeCounts := [2]map[string]int{{}, {}}
	repeaters := map[string]*crossBorderFlowRepeaterWork{}
	directionRepeaters := [2]map[string]bool{{}, {}}
	qualifyingPaths := 0
	for hash, rows := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var masks [2]uint8
		var unnamed [2]bool
		var first [2]int64
		unresolved := false
		scopeSeen := [2]map[string]bool{{}, {}}
		repeaterSeen := map[string]*crossBorderFlowContentRepeater{}
		pathSeen := map[string]bool{}
		firstPathTimes := map[string]int64{}
		for i, entry := range rows {
			if i&127 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			id := crossBorderFlowPathID(*entry.row, entry.wire)
			if at, exists := firstPathTimes[id]; !exists || entry.row.Timestamp < at {
				firstPathTimes[id] = entry.row.Timestamp
			}
		}
		for rowIndex, entry := range rows {
			if rowIndex&127 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			row, w := entry.row, entry.wire
			points := crossBorderFlowPoints(*row, w, nodes, index, result.IdentityComplete)
			if len(points) < 2 {
				unresolved = true
			}
			for _, point := range points {
				if point.Location == nil || point.Country == "" {
					unresolved = true
					break
				}
			}
			crossings, dirs, reach, distance := crossBorderFlowCrossings(points)
			if dirs == 0 {
				continue
			}
			scopeKey, _ := crossBorderDashboardScope(w)
			for direction := 0; direction < 2; direction++ {
				if dirs&(1<<direction) == 0 {
					continue
				}
				masks[direction] |= crossBorderFlowScopeMask(scopeKey)
				unnamed[direction] = unnamed[direction] || scopeKey == "unknown"
				if first[direction] == 0 || row.Timestamp < first[direction] {
					first[direction] = row.Timestamp
				}
				scopeSeen[direction][scopeKey] = true
				crossBorderFlowMax(&result.Directions[direction].FurthestKm, reach[direction])
			}
			endpoints := map[int]bool{}
			for _, c := range crossings {
				endpoints[c.FromIndex] = true
				endpoints[c.ToIndex] = true
			}
			for i, p := range points {
				if p.Kind != "repeater" || p.PublicKey == "" {
					continue
				}
				r := repeaterSeen[p.PublicKey]
				if r == nil {
					r = &crossBorderFlowContentRepeater{scopes: map[string]bool{}}
					repeaterSeen[p.PublicKey] = r
				}
				r.directions |= dirs
				r.crossing = r.crossing || endpoints[i]
				r.scopes[scopeKey] = true
				if row.Timestamp > r.at {
					r.at = row.Timestamp
				}
			}
			id := crossBorderFlowPathID(*row, w)
			if pathSeen[id] {
				continue
			}
			pathSeen[id] = true
			qualifyingPaths++
			at := crossBorderTime(firstPathTimes[id])
			if paths.Len() == pathLimit && (at < (*paths)[0].FirstRecordedAt || (at == (*paths)[0].FirstRecordedAt && id <= (*paths)[0].ID)) {
				continue
			}
			p := CrossBorderFlowPath{ID: id, Hash: hash, Directions: crossBorderFlowDirectionNames(dirs), FirstRecordedAt: at, Scope: w.scope, ScopeKey: scopeKey, HopWidth: w.width, Hops: points, Crossings: crossings, DistanceKm: distance, SNR: row.SNR, RSSI: row.RSSI}
			for _, r := range reach {
				crossBorderFlowMax(&p.FurthestKm, r)
			}
			p.AirtimeMs, p.AirtimeSource = crossBorderFlowAirtime(*row, cfg.PeerReferenceProfile)
			if paths.Len() < pathLimit {
				heap.Push(paths, p)
			} else {
				(*paths)[0] = p
				heap.Fix(paths, 0)
			}
		}
		if masks[0] == 0 && masks[1] == 0 {
			if unresolved {
				result.UnresolvedContents++
			} else {
				result.NonCrossingContents++
			}
		}
		for direction, mask := range masks {
			if mask == 0 {
				continue
			}
			d := &result.Directions[direction]
			d.Contents++
			switch mask {
			case 1:
				d.Unscoped++
			case 2:
				d.TransportWildcard++
			case 4:
				d.Scoped++
				if unnamed[direction] {
					d.UnknownScopeName++
				}
			default:
				d.Mixed++
			}
			bucket := int((first[direction] - since) / int64(dashboard.BucketMinutes*60))
			if bucket >= len(d.Timeline) {
				bucket = len(d.Timeline) - 1
			}
			if bucket >= 0 && d.Timeline[bucket].Contents != nil {
				*d.Timeline[bucket].Contents++
			}
			for key := range scopeSeen[direction] {
				scopeCounts[direction][key]++
			}
		}
		for key, seen := range repeaterSeen {
			work := repeaters[key]
			if work == nil {
				n := nodes[key]
				work = &crossBorderFlowRepeaterWork{value: CrossBorderFlowRepeater{PublicKey: key, Name: n.hop.Name, Country: n.hop.Country, Location: n.hop.Location}, scopes: map[string]int{}}
				repeaters[key] = work
			}
			work.value.Contents++
			if seen.crossing {
				work.value.CrossingContents++
			}
			work.directions |= seen.directions
			for direction := 0; direction < 2; direction++ {
				if seen.directions&(1<<direction) != 0 {
					directionRepeaters[direction][key] = true
				}
			}
			for scope := range seen.scopes {
				work.scopes[scope]++
			}
			at := crossBorderTime(seen.at)
			if at > work.value.LastRecordedAt {
				work.value.LastRecordedAt = at
			}
		}
	}
	for i := range result.Directions {
		result.Directions[i].Scopes = crossBorderFlowScopes(scopeCounts[i])
		result.Directions[i].Repeaters = len(directionRepeaters[i])
	}
	for _, work := range repeaters {
		work.value.Directions = crossBorderFlowDirectionNames(work.directions)
		work.value.Scopes = crossBorderFlowScopes(work.scopes)
		result.Repeaters = append(result.Repeaters, work.value)
	}
	sort.Slice(result.Repeaters, func(i, j int) bool {
		a, b := result.Repeaters[i], result.Repeaters[j]
		if a.Contents != b.Contents {
			return a.Contents > b.Contents
		}
		return a.PublicKey < b.PublicKey
	})
	metadataLimit := cfg.MetadataLimit
	if metadataLimit <= 0 || metadataLimit > crossBorderDashboardMetadata {
		metadataLimit = crossBorderDashboardMetadata
	}
	if len(result.Repeaters) > metadataLimit {
		result.Repeaters = append([]CrossBorderFlowRepeater{}, result.Repeaters[:metadataLimit]...)
		result.RepeatersTruncated = true
	}
	result.Paths = append(result.Paths, (*paths)...)
	sort.Slice(result.Paths, func(i, j int) bool {
		a, b := result.Paths[i], result.Paths[j]
		if a.FirstRecordedAt != b.FirstRecordedAt {
			return a.FirstRecordedAt > b.FirstRecordedAt
		}
		return a.ID > b.ID
	})
	result.PathsTruncated = qualifyingPaths > len(result.Paths)
	if result.Status == "empty" && qualifyingPaths > 0 {
		result.Status = "available"
	}
	if result.PathsTruncated || result.RepeatersTruncated {
		result.Status = "partial"
	}
	return result, ctx.Err()
}

func crossBorderFlowNodes(ctx context.Context, local, peer crossBorderSnapshot) (map[string]*crossBorderFlowNode, map[string]string, error) {
	identities := map[string]crossBorderNodeIdentity{}
	nodes := map[string]*crossBorderFlowNode{}
	for _, s := range []crossBorderSnapshot{local, peer} {
		for i, id := range s.DashboardNodes {
			if i&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
			if i >= crossBorderDashboardKeys {
				break
			}
			key := crossBorderID(id.PublicKey)
			if !crossBorderPublicKey(key) {
				continue
			}
			previous := identities[key]
			id.Hidden = id.Hidden || previous.Hidden
			if id.Role == "" {
				// An observer-only identity is a collision candidate, not
				// evidence contradicting a node role known by the other feed.
				id.Role = previous.Role
			} else if previous.Role != "" && previous.Role != id.Role {
				id.Role = "conflict"
			}
			identities[key] = id
		}
		for i, n := range s.Nodes {
			if i >= crossBorderDashboardMetadata {
				break
			}
			key := crossBorderID(n.PublicKey)
			if !crossBorderPublicKey(key) {
				continue
			}
			v := nodes[key]
			if v == nil {
				v = &crossBorderFlowNode{hop: CrossBorderFlowHop{PublicKey: key, Name: n.Name, Kind: "repeater"}}
				nodes[key] = v
			}
			v.hidden = v.hidden || n.Hidden
			loc := crossBorderLocation(n.Lat, n.Lon)
			if loc != nil && loc.Lat == 0 && loc.Lon == 0 {
				loc = nil
			}
			if loc != nil && v.hop.Location != nil && (loc.Lat != v.hop.Location.Lat || loc.Lon != v.hop.Location.Lon) {
				v.conflict = true
			}
			if loc != nil {
				v.hop.Location = loc
			}
		}
	}
	for _, snapshot := range []crossBorderSnapshot{local, peer} {
		if len(snapshot.DashboardHiddenIDs) > crossBorderDashboardKeys {
			return nil, nil, errCrossBorderUnavailable
		}
		for rawKey := range snapshot.DashboardHiddenIDs {
			key := crossBorderID(rawKey)
			if !crossBorderPublicKey(key) {
				continue
			}
			identity := identities[key]
			identity.PublicKey, identity.Hidden = key, true
			identities[key] = identity
			if node := nodes[key]; node != nil {
				node.hidden = true
			}
		}
	}
	all := make([]crossBorderNodeIdentity, 0, len(identities))
	for key, id := range identities {
		all = append(all, id)
		v := nodes[key]
		if v == nil {
			continue
		}
		v.hidden = v.hidden || id.Hidden
		if id.Role != "repeater" && id.Role != "room" {
			v.hop.Kind = "receiver"
		}
	}
	for _, s := range []crossBorderSnapshot{local, peer} {
		for i, o := range s.Observers {
			if i >= crossBorderDashboardMetadata {
				break
			}
			key := crossBorderID(o.ID)
			if !crossBorderPublicKey(key) {
				continue
			}
			v := nodes[key]
			if v == nil {
				v = &crossBorderFlowNode{hop: CrossBorderFlowHop{PublicKey: key, Name: o.Name, Kind: "receiver"}}
				nodes[key] = v
			}
			if o.Location != nil {
				if v.hop.Location != nil && (o.Location.Lat != v.hop.Location.Lat || o.Location.Lon != v.hop.Location.Lon) {
					v.conflict = true
				}
				v.hop.Location = o.Location
			}
			if id, ok := identities[key]; ok {
				v.hidden = v.hidden || id.Hidden
			} else {
				all = append(all, crossBorderNodeIdentity{PublicKey: key, Role: "observer"})
			}
		}
	}
	for _, v := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if v.hidden {
			v.hop = CrossBorderFlowHop{Kind: "unknown", Reason: "unresolved_hop"}
			continue
		}
		if v.conflict {
			v.hop.Location = nil
			v.hop.Reason = "position_conflict"
		} else if v.hop.Location == nil {
			v.hop.Reason = "position_unavailable"
		}
		v.hop.Country = crossBorderCountry(v.hop.Location)
		if v.hop.Country != "" {
			v.hop.borderKm = crossBorderDistanceToBorderKM(v.hop.Location)
		} else if v.hop.Reason == "" {
			v.hop.Reason = "country_unresolved"
		}
	}
	return nodes, crossBorderDashboardPrefixIndex(all), ctx.Err()
}

func crossBorderFlowPoints(row crossBorderObservationRow, w crossBorderWire, nodes map[string]*crossBorderFlowNode, index map[string]string, complete bool) []CrossBorderFlowHop {
	prefixes := crossBorderDashboardHops(row, w)
	points := make([]CrossBorderFlowHop, 0, len(prefixes)+1)
	for i, prefix := range prefixes {
		point := CrossBorderFlowHop{Index: i, Kind: "unknown", Reason: "unresolved_hop"}
		if w.width < 2 {
			point.Reason = "one_byte_path"
		} else if !complete {
			point.Reason = "identity_index_incomplete"
		} else if n := nodes[index[prefix]]; n != nil && !n.hidden && n.hop.Kind == "repeater" {
			point = n.hop
			point.Index = i
		}
		points = append(points, point)
	}
	// An explicit receiver is the last observed point, never a forwarding hop.
	if n := nodes[crossBorderID(row.ObserverID)]; n != nil && !n.hidden && complete {
		point := n.hop
		point.Index = len(points)
		point.Kind = "receiver"
		if len(points) == 0 || points[len(points)-1].PublicKey != point.PublicKey {
			points = append(points, point)
		}
	}
	return points
}

func crossBorderFlowCrossings(points []CrossBorderFlowHop) ([]CrossBorderFlowCrossing, uint8, [2]*float64, *float64) {
	crossings := []CrossBorderFlowCrossing{}
	var dirs uint8
	var reach [2]*float64
	var distance float64
	complete := len(points) > 1
	active := -1
	for i, p := range points {
		if p.Location == nil || p.Country == "" {
			complete = false
			active = -1
			continue
		}
		if i > 0 {
			a := points[i-1]
			if a.Location == nil || a.Country == "" {
				complete = false
				continue
			}
			leg := haversineKm(a.Location.Lat, a.Location.Lon, p.Location.Lat, p.Location.Lon)
			distance += leg
			if a.Country != p.Country {
				active = 0
				if a.Country == "US" {
					active = 1
				}
				dirs |= 1 << active
				crossings = append(crossings, CrossBorderFlowCrossing{FromIndex: i - 1, ToIndex: i, Direction: crossBorderFlowKeys[active], LegKm: leg})
			}
		}
		if active >= 0 {
			crossBorderFlowMax(&reach[active], p.borderKm)
		}
	}
	var km *float64
	if complete {
		km = &distance
	}
	return crossings, dirs, reach, km
}

func crossBorderFlowMax(dst **float64, value *float64) {
	if value != nil && (*dst == nil || **dst < *value) {
		v := *value
		*dst = &v
	}
}
func crossBorderFlowScopeMask(key string) uint8 {
	if key == "unscoped" {
		return 1
	}
	if key == "transport-zero" {
		return 2
	}
	return 4
}
func crossBorderFlowDirectionNames(mask uint8) []string {
	out := []string{}
	for i, key := range crossBorderFlowKeys {
		if mask&(1<<i) != 0 {
			out = append(out, key)
		}
	}
	return out
}
func crossBorderFlowScopes(counts map[string]int) []CrossBorderFlowScopeCount {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []CrossBorderFlowScopeCount{}
	other := 0
	for _, key := range keys {
		if len(out) >= 128 {
			other += counts[key]
			continue
		}
		label := strings.TrimPrefix(key, "named:")
		switch key {
		case "unscoped":
			label = "Unscoped"
		case "transport-zero":
			label = "Transport wildcard"
		case "unknown":
			label = "Scoped · name unresolved"
		}
		out = append(out, CrossBorderFlowScopeCount{key, label, counts[key]})
	}
	if other > 0 {
		out = append(out, CrossBorderFlowScopeCount{Key: "other", Label: "Other scope associations", Contents: other})
	}
	return out
}
func crossBorderFlowPathID(row crossBorderObservationRow, w crossBorderWire) string {
	n := (2 + w.count*w.width) * 2
	if w.route == RouteTransportFlood {
		n += 8
	}
	if n > len(row.RawHex) {
		n = len(row.RawHex)
	}
	h := sha256.Sum256([]byte(row.Hash + "|" + crossBorderID(row.ObserverID) + "|" + strings.ToLower(row.RawHex[:n])))
	return hex.EncodeToString(h[:12])
}
func crossBorderFlowAirtime(row crossBorderObservationRow, p *CrossBorderReferenceProfile) (*float64, string) {
	if p == nil {
		return nil, "unavailable"
	}
	size := row.RawHexLength / 2
	if size == 0 {
		size = len(row.RawHex) / 2
	}
	d := lora.TimeOnAir(size, lora.Preset{FreqHz: p.FrequencyMHz * 1e6, BWkHz: p.BandwidthKHz, SF: p.SpreadingFactor, CR: p.CodingRate, Preamble: lora.PreambleForSF(p.SpreadingFactor)})
	if d <= 0 {
		return nil, "unavailable"
	}
	ms := float64(d) / float64(time.Millisecond)
	return &ms, "Modelled saved frame using " + p.Label + "; not measured radio airtime"
}
