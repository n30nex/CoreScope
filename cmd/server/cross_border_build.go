package main

import (
	"context"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

func crossBorderBase(cfg CrossBorderConfig, filters CrossBorderFilters, now time.Time) CrossBorderResponse {
	r := CrossBorderResponse{Enabled: cfg.Enabled, Status: "unavailable", GeneratedAt: now.UTC().Format(time.RFC3339), Filters: filters,
		Sources: CrossBorderSources{
			Local: CrossBorderSource{Label: cfg.LocalLabel, Status: "unavailable", SelectionIATA: filters.LocalIATA},
			Peer:  CrossBorderSource{Label: cfg.PeerLabel, Status: "unavailable", SelectionIATA: filters.PeerIATA, ReferenceProfile: cfg.PeerReferenceProfile}},
		Options: CrossBorderOptions{LocalRegions: []CrossBorderRegion{}, PeerRegions: []CrossBorderRegion{}, WindowMinutes: []int{},
			Scopes: []CrossBorderScopeOption{{Value: "all", Label: "All scopes"}, {Value: "unscoped", Label: "No named transport scope"}, {Value: "unknown", Label: "Scope unresolved"}}},
		Limits:        CrossBorderLimits{ObservationRows: cfg.ObservationLimit, MetadataRows: cfg.MetadataLimit, PacketRows: cfg.PacketLimit, ObservationsPerPacket: cfg.ObservationsPerPacket, CacheSeconds: cfg.CacheSeconds},
		PeerObservers: []CrossBorderObserver{}, PeerRepeaters: []CrossBorderRepeater{}, Packets: []CrossBorderPacket{},
		Findings: []CrossBorderFinding{
			{Code: "shared_content_only", Severity: "info", Message: "Matching content in two samples does not establish an RF route across a border or RF latency."},
			{Code: "passive_limits", Severity: "info", Message: "Missing observations are not packet loss or evidence of misconfiguration. SNR describes the listed reception only."},
			{Code: "reported_radio_hardware_only", Severity: "info", Message: "Radio descriptions identify reported hardware; actual frequency, bandwidth, spreading factor and coding rate are not available."},
			{Code: "sample_availability", Severity: "info", Message: "Source status describes query and retained-data availability, not a broker connection. Retained evidence is not proof of continuous collection. Local counts cover peer content candidates only."},
			{Code: "observer_identity_limit", Severity: "info", Message: "Independent overlap means distinct reported observer IDs in these datasets; it does not verify separate physical devices or independent MQTT delivery."},
		}}
	if r.Filters.LocalIATA == nil {
		r.Filters.LocalIATA = []string{}
		r.Sources.Local.SelectionIATA = []string{}
	}
	if r.Filters.PeerIATA == nil {
		r.Filters.PeerIATA = []string{}
		r.Sources.Peer.SelectionIATA = []string{}
	}
	for _, minutes := range []int{15, 60, 180, 360} {
		if minutes <= cfg.MaxWindowMinutes {
			r.Options.WindowMinutes = append(r.Options.WindowMinutes, minutes)
		}
	}
	if !cfg.Enabled {
		r.Status = "disabled"
		r.Sources.Local.Status = "disabled"
		r.Sources.Peer.Status = "disabled"
	}
	return r
}

type crossBorderWire struct {
	route, payload, width, count int
	lastHop                      string
	scope                        CrossBorderScope
}

// Only a bounded raw prefix is fetched for header/path validation. It is never
// serialized, payloads are never decoded, and decrypted JSON is never queried.
// A missing observation header is not replaced with a representative header.
func crossBorderReadWire(row crossBorderObservationRow) (crossBorderWire, bool) {
	b, err := hex.DecodeString(row.RawHex)
	if err != nil || len(b) < 3 {
		return crossBorderWire{}, false
	}
	w := crossBorderWire{route: int(b[0] & 3), payload: int((b[0] >> 2) & 15), scope: CrossBorderScope{Kind: "unscoped"}}
	offset := 1
	if packetpath.IsTransportRoute(w.route) {
		offset += 4
	}
	if len(b) <= offset || !isValidPathLen(b[offset]) {
		return crossBorderWire{}, false
	}
	count, width := int(b[offset]&63), int(b[offset]>>6)+1
	end := offset + 1 + count*width
	rawLen := row.RawHexLength
	if rawLen == 0 {
		rawLen = len(row.RawHex)
	}
	if rawLen%2 != 0 || rawLen/2 <= end || rawLen/2-end > maxPacketPayload || len(b) <= end {
		return crossBorderWire{}, false
	}
	w.width = packetpath.HashSize(row.RawHex)
	if packetpath.PathBytesAreHops(byte(w.payload)) {
		w.count = count
		if count > 0 {
			w.lastHop = hex.EncodeToString(b[end-width : end])
		}
	}
	if packetpath.IsTransportRoute(w.route) && (b[1] != 0 || b[2] != 0) {
		w.scope.Kind = "unknown"
		// The named scope belongs to the canonical transmission. It can only
		// describe this observation when both carry the same transport code.
		canonical, err := hex.DecodeString(row.CanonicalRawHex)
		if err == nil && len(canonical) >= 5 && packetpath.IsTransportRoute(int(canonical[0]&3)) && canonical[1] == b[1] && canonical[2] == b[2] && !(b[1] == 255 && b[2] == 255) && row.ScopeName != nil {
			name := normScope(strings.TrimSpace(*row.ScopeName))
			if name != "" && !isScopeWildcard(name) {
				w.scope.Kind = "named"
				w.scope.Name = &name
			}
		}
	}
	return w, true
}

func crossBorderScopeMatches(scope CrossBorderScope, filter string) bool {
	if filter == "all" {
		return true
	}
	if filter == "unscoped" || filter == "unknown" {
		return scope.Kind == filter
	}
	return scope.Kind == "named" && scope.Name != nil && *scope.Name == filter
}

func crossBorderScopeKey(scope CrossBorderScope) string {
	if scope.Name != nil {
		return scope.Kind + ":" + *scope.Name
	}
	return scope.Kind
}

func crossBorderSortedScopes(scopes map[string]CrossBorderScope) []CrossBorderScope {
	keys := make([]string, 0, len(scopes))
	for k := range scopes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CrossBorderScope, 0, len(keys))
	for _, k := range keys {
		out = append(out, scopes[k])
	}
	return out
}

func crossBorderTime(ts int64) string { return time.Unix(ts, 0).UTC().Format(time.RFC3339) }
func crossBorderPublicKey(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func crossBorderID(id string) string {
	if crossBorderPublicKey(id) {
		return strings.ToLower(id)
	}
	return id
}

type crossBorderObserverAgg struct {
	value  CrossBorderObserver
	scopes map[string]CrossBorderScope
	widths [4]bool
}
type crossBorderRepeaterAgg struct {
	value  CrossBorderRepeater
	scopes map[string]CrossBorderScope
	widths [4]bool
	hashes map[string]bool
}
type crossBorderPacketAgg struct {
	value                             CrossBorderPacket
	peerIndependent, localIndependent bool
	identityUnknown                   bool
}

func crossBorderRegions(snapshot crossBorderSnapshot, allowed []string) []CrossBorderRegion {
	if snapshot.Regions != nil {
		out := []CrossBorderRegion{}
		for _, r := range snapshot.Regions {
			if allowed == nil || crossBorderContains(allowed, r.IATA) {
				out = append(out, r)
			}
		}
		return out
	}
	counts := map[string]int{}
	for _, o := range snapshot.Observers {
		iata := strings.ToUpper(strings.TrimSpace(o.IATA))
		if validCrossBorderIATA(iata) && (allowed == nil || crossBorderContains(allowed, iata)) {
			counts[iata]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CrossBorderRegion, 0, len(keys))
	for _, k := range keys {
		out = append(out, CrossBorderRegion{IATA: k, Label: k, Observers: counts[k]})
	}
	return out
}

func buildCrossBorder(ctx context.Context, cfg CrossBorderConfig, filters CrossBorderFilters, local, peer crossBorderSnapshot, now time.Time) (CrossBorderResponse, error) {
	if err := ctx.Err(); err != nil {
		return CrossBorderResponse{}, err
	}
	r := crossBorderBase(cfg, filters, now)
	if !cfg.Enabled {
		return r, nil
	}
	r.Options.LocalRegions = crossBorderRegions(local, cfg.AllowedLocalIATA)
	r.Options.PeerRegions = crossBorderRegions(peer, nil)
	shared := map[string]bool{}
	localIDs := map[string]bool{}
	for _, o := range local.Observers {
		localIDs[crossBorderID(o.ID)] = true
	}
	for _, id := range local.SharedObserverIDs {
		shared[crossBorderID(id)] = true
	}
	for _, o := range peer.Observers {
		if localIDs[crossBorderID(o.ID)] {
			shared[crossBorderID(o.ID)] = true
		}
	}
	delete(shared, "")
	r.Summary.DuplicateObserverIDs = len(shared)
	if len(shared) > 0 {
		r.Findings = append(r.Findings, CrossBorderFinding{Code: "shared_observers", Severity: "warning", Message: "Observers present in both sources are excluded from independent overlap."})
	}
	identitiesComplete := local.SharedIdentityComplete || (!local.ObserversTruncated && !peer.ObserversTruncated)
	if !identitiesComplete {
		r.Findings = append(r.Findings, CrossBorderFinding{Code: "identity_sample_incomplete", Severity: "warning", Message: "Observer metadata is capped; independent overlap cannot be established for this sample."})
	}
	observerAgg := map[string]*crossBorderObserverAgg{}
	for _, o := range peer.Observers {
		if !crossBorderContains(filters.PeerIATA, strings.ToUpper(strings.TrimSpace(o.IATA))) {
			continue
		}
		o.SharedIdentity = shared[crossBorderID(o.ID)]
		o.HopWidths = []int{}
		o.ObservedScopes = []CrossBorderScope{}
		o.RadioHardwareReportedAt = nil
		o.CanRelayReportedAt = nil
		observerAgg[crossBorderID(o.ID)] = &crossBorderObserverAgg{value: o, scopes: map[string]CrossBorderScope{}}
	}
	nodes := map[string]crossBorderNode{}
	prefixes := map[string]string{}
	for _, n := range peer.Nodes {
		pk := crossBorderID(n.PublicKey)
		nodes[pk] = n
		if !crossBorderPublicKey(pk) {
			continue
		}
		for _, width := range []int{4, 6} {
			prefix := pk[:width]
			if previous, ok := prefixes[prefix]; ok && previous != pk {
				prefixes[prefix] = ""
			} else if !ok {
				prefixes[prefix] = pk
			}
		}
	}
	repeaters := map[string]*crossBorderRepeaterAgg{}
	ensureRepeater := func(pk string) *crossBorderRepeaterAgg {
		pk = crossBorderID(pk)
		if a := repeaters[pk]; a != nil {
			return a
		}
		n, ok := nodes[pk]
		if !ok || n.Hidden || (n.Role != "repeater" && n.Role != "room") {
			return nil
		}
		v := CrossBorderRepeater{PublicKey: pk, Name: n.Name, Role: n.Role, LastSeenAt: n.LastSeenAt, ObservedScopes: []CrossBorderScope{}, HopWidths: []int{}}
		v.Location = crossBorderLocation(n.Lat, n.Lon)
		if n.ConfiguredScope != nil {
			names, wildcard := splitDeclaredRegions(*n.ConfiguredScope)
			v.DeclaredScopes = &CrossBorderDeclaredScopes{Names: names, AllowsUnscoped: wildcard, ReportedAt: n.ScopeAt, Truncated: n.DeclaredTruncated}
		}
		a := &crossBorderRepeaterAgg{value: v, scopes: map[string]CrossBorderScope{}, hashes: map[string]bool{}}
		repeaters[pk] = a
		return a
	}
	for id := range observerAgg {
		ensureRepeater(id)
	}
	packets := map[string]*crossBorderPacketAgg{}
	namedScopes := map[string]bool{}
	since := now.Add(-time.Duration(filters.WindowMinutes) * time.Minute).Unix()
	for side, snapshot := range []crossBorderSnapshot{peer, local} {
		source := &r.Sources.Peer
		if side == 1 {
			source = &r.Sources.Local
		}
		source.ObservationsTruncated = snapshot.ObservationsTruncated
		source.ObserversTruncated = snapshot.ObserversTruncated
		source.NodesTruncated = snapshot.NodesTruncated
		source.OldestStoredObservationAt = snapshot.OldestStoredObservationAt
		source.RowsRead = len(snapshot.Observations)
		if !snapshot.Available {
			continue
		}
		source.Status = "empty"
		var newest int64
		if snapshot.LatestStoredObservationAt != nil {
			if latest, err := time.Parse(time.RFC3339, *snapshot.LatestStoredObservationAt); err == nil {
				newest = latest.Unix()
			}
		}
		for i, row := range snapshot.Observations {
			if i&127 == 0 {
				if err := ctx.Err(); err != nil {
					return CrossBorderResponse{}, err
				}
			}
			if row.Timestamp > newest {
				newest = row.Timestamp
			}
			if row.Timestamp < since || row.Timestamp > now.Unix() {
				continue
			}
			direction := strings.ToLower(strings.TrimSpace(row.Direction))
			if direction == "tx" {
				source.ExcludedTXObservations++
				continue
			}
			wire, valid := crossBorderReadWire(row)
			if !valid || row.Hash == "" {
				source.ExcludedInvalidObservations++
				continue
			}
			if side == 0 && wire.scope.Name != nil {
				namedScopes[*wire.scope.Name] = true
			}
			// Scope selects the peer cohort. A local copy can use another
			// transport scope (or have no matched key) and still be evidence
			// of the same content; preserve that observation's own scope.
			if side == 0 && !crossBorderScopeMatches(wire.scope, filters.Scope) {
				continue
			}
			if direction != "rx" {
				direction = "unknown"
				source.UnknownDirectionObservations++
			}
			id := crossBorderID(row.ObserverID)
			obs := CrossBorderObservation{ObserverID: row.ObserverID, ObservedAt: crossBorderTime(row.Timestamp), Direction: direction, SNR: row.SNR, RSSI: row.RSSI, SharedObserver: shared[id], RouteType: &wire.route, Scope: wire.scope}
			if wire.width > 0 {
				obs.HopWidth = &wire.width
			}
			if packetpath.PathBytesAreHops(byte(wire.payload)) {
				obs.HopCount = &wire.count
			}
			a := packets[row.Hash]
			if side == 1 && a == nil {
				continue
			}
			if a == nil {
				a = &crossBorderPacketAgg{value: CrossBorderPacket{Hash: row.Hash, PayloadType: wire.payload, RouteType: &wire.route, Scope: wire.scope, LastSeenAt: obs.ObservedAt, PeerObservations: []CrossBorderObservation{}, LocalObservations: []CrossBorderObservation{}}}
				packets[row.Hash] = a
			}
			if id == "" {
				a.identityUnknown = true
			}
			if side == 0 {
				a.value.PeerObservationCount++
				a.peerIndependent = a.peerIndependent || (id != "" && !shared[id] && identitiesComplete)
				if obs.ObservedAt > a.value.LastSeenAt {
					a.value.LastSeenAt = obs.ObservedAt
					a.value.Scope = wire.scope
					a.value.RouteType = &wire.route
				}
				if len(a.value.PeerObservations) < cfg.ObservationsPerPacket {
					a.value.PeerObservations = append(a.value.PeerObservations, obs)
				} else {
					a.value.ObservationsTruncated = true
				}
				if oa := observerAgg[id]; oa != nil {
					oa.value.SampleObservations++
					if direction == "unknown" {
						oa.value.UnknownDirectionObservations++
					}
					oa.widths[wire.width] = true
					oa.scopes[crossBorderScopeKey(wire.scope)] = wire.scope
				}
				if wire.payload == PayloadADVERT {
					ensureRepeater(row.FromPubkey)
				}
				if !peer.NodesTruncated && (wire.route == RouteFlood || wire.route == RouteTransportFlood) && len(wire.lastHop) >= minForwarderHopHexLen {
					if ra := ensureRepeater(prefixes[wire.lastHop]); ra != nil {
						if !ra.hashes[row.Hash] {
							ra.hashes[row.Hash] = true
							ra.value.SampleForwardedPackets++
						}
						ra.scopes[crossBorderScopeKey(wire.scope)] = wire.scope
						ra.widths[wire.width] = true
						// Transport code zero does not establish ordinary wildcard
						// flood policy, even though its scope is labelled unscoped.
						if wire.route == RouteFlood {
							ra.value.ObservedUnscoped = true
						}
						if wire.scope.Kind == "unknown" {
							ra.value.ObservedUnknownScope = true
						}
						if ra.value.LastForwardedAt == nil || obs.ObservedAt > *ra.value.LastForwardedAt {
							ts := obs.ObservedAt
							ra.value.LastForwardedAt = &ts
						}
					}
				}
			} else {
				a.value.LocalObservationCount++
				a.localIndependent = a.localIndependent || (id != "" && !shared[id] && identitiesComplete)
				if len(a.value.LocalObservations) < cfg.ObservationsPerPacket {
					a.value.LocalObservations = append(a.value.LocalObservations, obs)
				} else {
					a.value.ObservationsTruncated = true
				}
			}
		}
		if newest > 0 {
			ts := crossBorderTime(newest)
			source.LatestObservationAt = &ts
			age := now.Sub(time.Unix(newest, 0)).Seconds()
			if age < 0 {
				age = 0
			}
			source.AgeSeconds = &age
			source.Status = "available"
			if age > float64(cfg.StaleAfterSeconds) {
				source.Status = "stale"
			}
		}
	}
	r.Summary.PeerPackets = len(packets)
	for _, a := range packets {
		p := a.value
		switch {
		case !local.Available:
			p.Overlap = "local_unavailable"
		case p.LocalObservationCount == 0:
			p.Overlap = "not_seen_locally"
			r.Summary.NotSeenLocallyPackets++
		case a.peerIndependent && a.localIndependent:
			p.Overlap = "independent"
			r.Summary.IndependentSharedPackets++
		case !identitiesComplete || a.identityUnknown:
			p.Overlap = "identity_unverified"
			r.Summary.IdentityUnverifiedPackets++
		default:
			p.Overlap = "shared_observer_only"
			r.Summary.SharedObserverOnlyPackets++
		}
		if p.LocalObservationCount > 0 {
			r.Summary.LocalReferencePackets++
		}
		r.Packets = append(r.Packets, p)
	}
	sort.Slice(r.Packets, func(i, j int) bool {
		if r.Packets[i].LastSeenAt == r.Packets[j].LastSeenAt {
			return r.Packets[i].Hash < r.Packets[j].Hash
		}
		return r.Packets[i].LastSeenAt > r.Packets[j].LastSeenAt
	})
	if len(r.Packets) > cfg.PacketLimit {
		r.Completeness.PacketsTruncated = true
		r.Packets = append([]CrossBorderPacket{}, r.Packets[:cfg.PacketLimit]...)
	}
	for _, oa := range observerAgg {
		for width := 1; width <= 3; width++ {
			if oa.widths[width] {
				oa.value.HopWidths = append(oa.value.HopWidths, width)
			}
		}
		oa.value.ObservedScopes = crossBorderSortedScopes(oa.scopes)
		r.PeerObservers = append(r.PeerObservers, oa.value)
	}
	sort.Slice(r.PeerObservers, func(i, j int) bool { return r.PeerObservers[i].ID < r.PeerObservers[j].ID })
	for _, ra := range repeaters {
		for width := 1; width <= 3; width++ {
			if ra.widths[width] {
				ra.value.HopWidths = append(ra.value.HopWidths, width)
			}
		}
		ra.value.ObservedScopes = crossBorderSortedScopes(ra.scopes)
		r.PeerRepeaters = append(r.PeerRepeaters, ra.value)
	}
	sort.Slice(r.PeerRepeaters, func(i, j int) bool { return r.PeerRepeaters[i].PublicKey < r.PeerRepeaters[j].PublicKey })
	r.Completeness.PeerRepeatersTruncated = peer.NodesTruncated
	names := make([]string, 0, len(namedScopes))
	for name := range namedScopes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		r.Options.Scopes = append(r.Options.Scopes, CrossBorderScopeOption{Value: name, Label: name})
	}
	r.Completeness.Complete = local.Available && peer.Available && !local.ObservationsTruncated && !peer.ObservationsTruncated && !local.ObserversTruncated && !peer.ObserversTruncated && !peer.NodesTruncated && !r.Completeness.PacketsTruncated && r.Sources.Peer.ExcludedInvalidObservations == 0 && r.Sources.Local.ExcludedInvalidObservations == 0
	for _, p := range r.Packets {
		if p.ObservationsTruncated {
			r.Completeness.Complete = false
		}
	}
	if peer.Available {
		r.Status = "ready"
		if !r.Completeness.Complete || r.Sources.Local.Status == "stale" || r.Sources.Peer.Status == "stale" {
			r.Status = "partial"
		}
	}
	if !local.Available || !peer.Available {
		r.Findings = append(r.Findings, CrossBorderFinding{Code: "source_unavailable", Severity: "warning", Message: "A source could not be read. Unavailable reference evidence is not a negative reception result."})
	}
	if !r.Completeness.Complete {
		r.Findings = append(r.Findings, CrossBorderFinding{Code: "bounded_sample", Severity: "warning", Message: "This response is a partial bounded sample. Counts are not network-wide totals and missing matches do not measure packet loss."})
	}
	return r, nil
}
