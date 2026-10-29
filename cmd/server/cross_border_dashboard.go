package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// Dashboard totals are independently sampled first-stored observer/path
// evidence. They are not a raw packet-arrival counter or country of origin.
type CrossBorderDashboard struct {
	BucketMinutes             int                        `json:"bucketMinutes"`
	FromAt                    string                     `json:"fromAt"`
	ToAt                      string                     `json:"toAt"`
	Local                     CrossBorderDashboardSource `json:"local"`
	Peer                      CrossBorderDashboardSource `json:"peer"`
	SharedContents            int                        `json:"sharedContents"`
	IndependentSharedContents int                        `json:"independentSharedContents"`
	RegionPairs               []CrossBorderRegionPair    `json:"regionPairs"`
	IdentityComplete          bool                       `json:"identityComplete"`
	RegionPairsTruncated      bool                       `json:"regionPairsTruncated"`
}

type CrossBorderDashboardSource struct {
	Label              string                         `json:"label"`
	Status             string                         `json:"status"`
	Records            int                            `json:"records"`
	Contents           int                            `json:"contents"`
	FloodRecords       int                            `json:"floodRecords"`
	Truncated          bool                           `json:"truncated"`
	CoveredFromAt      *string                        `json:"coveredFromAt"`
	RecordLimit        int                            `json:"recordLimit"`
	Widths             []CrossBorderWidthCount        `json:"widths"`
	UnknownAdvertisers int                            `json:"unknownAdvertisers"`
	Timeline           []CrossBorderDashboardBucket   `json:"timeline"`
	Scopes             []CrossBorderScopeCount        `json:"scopes"`
	Regions            []CrossBorderRegionCount       `json:"regions"`
	Repeaters          []CrossBorderDashboardRepeater `json:"repeaters"`
	Radio              CrossBorderRadio               `json:"radio"`
	NodeIndexComplete  bool                           `json:"nodeIndexComplete"`
	RepeatersTruncated bool                           `json:"repeatersTruncated"`
	ExcludedRecords    int                            `json:"excludedRecords"`
	ScopesTruncated    bool                           `json:"scopesTruncated"`
}

type CrossBorderWidthCount struct {
	Bytes       int `json:"bytes"`
	Records     int `json:"records"`
	Advertisers int `json:"advertisers"`
}

type CrossBorderDashboardBucket struct {
	At                string `json:"at"`
	Complete          bool   `json:"complete"`
	Records           *int   `json:"records"`
	OneByte           *int   `json:"oneByte"`
	TwoByte           *int   `json:"twoByte"`
	ThreeByte         *int   `json:"threeByte"`
	NamedScope        *int   `json:"namedScope"`
	Unscoped          *int   `json:"unscoped"`
	UnresolvedScope   *int   `json:"unresolvedScope"`
	TransportWildcard *int   `json:"transportWildcard"`
}

type CrossBorderScopeCount struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Records int    `json:"records"`
}

type CrossBorderRegionCount struct {
	IATA     string `json:"iata"`
	Records  int    `json:"records"`
	Contents int    `json:"contents"`
}

type CrossBorderRegionPair struct {
	LocalIATA string `json:"localIata"`
	PeerIATA  string `json:"peerIata"`
	Contents  int    `json:"contents"`
}

type CrossBorderDashboardRepeater struct {
	CrossBorderRepeater
	OwnAdvertWidth            *int    `json:"ownAdvertWidth"`
	OwnAdvertAt               *string `json:"ownAdvertAt"`
	CarryingAmbiguous         bool    `json:"carryingAmbiguous"`
	AmbiguousWidths           []int   `json:"ambiguousWidths"`
	ScopesTruncated           bool    `json:"scopesTruncated"`
	ObservedTransportWildcard bool    `json:"observedTransportWildcard"`
}

type CrossBorderRadio struct {
	Status             string                     `json:"status"`
	TxAirtimePct       *float64                   `json:"txAirtimePct"`
	RxAirtimePct       *float64                   `json:"rxAirtimePct"`
	ReportingObservers int                        `json:"reportingObservers"`
	ExcludedIntervals  int                        `json:"excludedIntervals"`
	Truncated          bool                       `json:"truncated"`
	Observers          []CrossBorderRadioObserver `json:"observers"`
}

type CrossBorderRadioObserver struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	IATA             string   `json:"iata"`
	TxAirtimePct     *float64 `json:"txAirtimePct"`
	RxAirtimePct     *float64 `json:"rxAirtimePct"`
	ValidSeconds     float64  `json:"validSeconds"`
	LatestAt         *string  `json:"latestAt"`
	ReportedCanRelay *bool    `json:"reportedCanRelay"`
}

type crossBorderNodeIdentity struct {
	PublicKey, Role string
	Hidden          bool
}

type crossBorderMetricRow struct {
	Observer CrossBorderObserver
	Sample   rawMetricsSample
}

func buildCrossBorderDashboard(ctx context.Context, cfg CrossBorderConfig, filters CrossBorderFilters, local, peer crossBorderSnapshot, now time.Time) (*CrossBorderDashboard, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket := 5
	if filters.WindowMinutes <= 15 {
		bucket = 1
	} else if filters.WindowMinutes > 60 {
		bucket = 15
	}
	d := &CrossBorderDashboard{BucketMinutes: bucket, FromAt: crossBorderTime(now.Add(-time.Duration(filters.WindowMinutes) * time.Minute).Unix()), ToAt: crossBorderTime(now.Unix()), RegionPairs: []CrossBorderRegionPair{}}
	shared := map[string]bool{}
	for _, id := range append(append([]string{}, local.SharedObserverIDs...), peer.SharedObserverIDs...) {
		shared[crossBorderID(id)] = true
	}
	localObserverIDs := map[string]bool{}
	for _, o := range local.Observers {
		localObserverIDs[crossBorderID(o.ID)] = true
	}
	for _, o := range peer.Observers {
		if localObserverIDs[crossBorderID(o.ID)] {
			shared[crossBorderID(o.ID)] = true
		}
	}
	d.IdentityComplete = local.Available && peer.Available && (local.SharedIdentityComplete || (!local.SharedIdentityChecked && !local.ObserversTruncated && !peer.ObserversTruncated))
	var left, right map[string]*crossBorderDashboardContent
	var err error
	d.Local, left, err = buildCrossBorderDashboardSource(ctx, cfg, filters, local, cfg.LocalLabel, filters.LocalIATA, shared, d.IdentityComplete, bucket, now)
	if err != nil {
		return nil, err
	}
	d.Peer, right, err = buildCrossBorderDashboardSource(ctx, cfg, filters, peer, cfg.PeerLabel, filters.PeerIATA, shared, d.IdentityComplete, bucket, now)
	if err != nil {
		return nil, err
	}
	// A region pair is overlapping content evidence, not an additive national
	// total. Bound the cross-product even for the supported128-region input.
	localRegions, peerRegions := filters.LocalIATA, filters.PeerIATA
	if len(localRegions) > 16 {
		localRegions = localRegions[:16]
		d.RegionPairsTruncated = true
	}
	if len(peerRegions) > 16 {
		peerRegions = peerRegions[:16]
		d.RegionPairsTruncated = true
	}
	pairs := map[CrossBorderRegionPair]int{}
	iterations := 0
	for hash, a := range left {
		b := right[hash]
		if b == nil {
			continue
		}
		d.SharedContents++
		if !d.IdentityComplete || !a.independent || !b.independent {
			continue
		}
		d.IndependentSharedContents++
		for _, l := range localRegions {
			if !a.independentRegions[l] {
				continue
			}
			for _, p := range peerRegions {
				iterations++
				if iterations&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				if b.independentRegions[p] {
					pairs[CrossBorderRegionPair{LocalIATA: l, PeerIATA: p}]++
				}
			}
		}
	}
	for _, l := range localRegions {
		for _, p := range peerRegions {
			if n := pairs[CrossBorderRegionPair{LocalIATA: l, PeerIATA: p}]; n > 0 {
				d.RegionPairs = append(d.RegionPairs, CrossBorderRegionPair{LocalIATA: l, PeerIATA: p, Contents: n})
			}
		}
	}
	return d, ctx.Err()
}

type crossBorderDashboardContent struct {
	index              int
	independent        bool
	independentRegions map[string]bool
}

type crossBorderDashboardRepeaterWork struct {
	value     CrossBorderDashboardRepeater
	widths    [4]bool
	ambiguous [4]bool
	scopes    map[string]CrossBorderScope
	contents  map[int]bool
	confirmed bool
}

func crossBorderDashboardHops(row crossBorderObservationRow, wire crossBorderWire) []string {
	if wire.width < 1 || wire.count == 0 {
		return nil
	}
	hops, err := packetpath.DecodeHopsForPayload(row.RawHex, byte(wire.payload))
	if err != nil {
		return nil
	}
	for i := range hops {
		hops[i] = strings.ToLower(hops[i])
	}
	return hops
}

func crossBorderDashboardPrefixIndex(nodes []crossBorderNodeIdentity) map[string]string {
	index := map[string]string{}
	for _, node := range nodes {
		key := crossBorderID(node.PublicKey)
		if !crossBorderPublicKey(key) {
			continue
		}
		for _, length := range []int{4, 6} {
			prefix := key[:length]
			if previous, exists := index[prefix]; !exists {
				index[prefix] = key
			} else if previous != key {
				index[prefix] = ""
			}
		}
	}
	return index
}

func crossBorderDashboardScope(wire crossBorderWire) (string, string) {
	if wire.scope.Kind == "named" && wire.scope.Name != nil {
		return "named:" + *wire.scope.Name, *wire.scope.Name
	}
	if wire.scope.Kind == "unknown" {
		return "unknown", "Unresolved scope"
	}
	if wire.route == RouteTransportFlood {
		return "transport-zero", "Transport wildcard (0000)"
	}
	return "unscoped", "Unscoped flood"
}

func dashboardInt(value int) *int { return &value }

func buildCrossBorderDashboardSource(ctx context.Context, cfg CrossBorderConfig, f CrossBorderFilters, snapshot crossBorderSnapshot, label string, regions []string, shared map[string]bool, identityComplete bool, bucket int, now time.Time) (CrossBorderDashboardSource, map[string]*crossBorderDashboardContent, error) {
	if len(snapshot.DashboardNodes) > crossBorderDashboardKeys {
		snapshot.DashboardNodes = snapshot.DashboardNodes[:crossBorderDashboardKeys]
		snapshot.DashboardNodeIndexComplete = false
	}
	result := CrossBorderDashboardSource{Label: label, Status: "unavailable", RecordLimit: crossBorderDashboardLimit(cfg), Truncated: snapshot.ObservationsTruncated, NodeIndexComplete: snapshot.DashboardNodeIndexComplete,
		Widths: []CrossBorderWidthCount{{Bytes: 1}, {Bytes: 2}, {Bytes: 3}}, Timeline: []CrossBorderDashboardBucket{}, Scopes: []CrossBorderScopeCount{}, Regions: []CrossBorderRegionCount{}, Repeaters: []CrossBorderDashboardRepeater{}, Radio: CrossBorderRadio{Status: "unavailable", Observers: []CrossBorderRadioObserver{}}}
	since := now.Add(-time.Duration(f.WindowMinutes) * time.Minute).Unix()
	count := (f.WindowMinutes + bucket - 1) / bucket
	if count < 1 || count > 360 {
		return result, nil, errCrossBorderUnavailable
	}
	for i := 0; i < count; i++ {
		result.Timeline = append(result.Timeline, CrossBorderDashboardBucket{At: crossBorderTime(since + int64(i*bucket*60))})
	}
	contents := map[string]*crossBorderDashboardContent{}
	if !snapshot.Available {
		return result, contents, nil
	}
	result.Status = "empty"
	rows := snapshot.Observations
	locallyCapped := len(rows) > result.RecordLimit
	if locallyCapped {
		rows = rows[:result.RecordLimit]
		result.Truncated = true
	}
	coverage := since
	coverageKnown := true
	if snapshot.OldestStoredObservationAt != nil {
		if at, err := time.Parse(time.RFC3339, *snapshot.OldestStoredObservationAt); err == nil && at.Unix() > coverage {
			coverage = at.Unix()
		}
	}
	oldest := int64(0)
	oldestKnown := false
	for _, row := range rows {
		if row.Timestamp >= since && row.Timestamp <= now.Unix() && (!oldestKnown || row.Timestamp < oldest) {
			oldest = row.Timestamp
			oldestKnown = true
		}
	}
	if result.Truncated {
		cutoff, known := oldest, oldestKnown
		if snapshot.DashboardScanCutoff != nil {
			cutoff, known = *snapshot.DashboardScanCutoff, true
			// A stricter local bound can only make coverage more conservative.
			if locallyCapped && oldestKnown && oldest > cutoff {
				cutoff = oldest
			}
		}
		if !known || cutoff >= now.Unix() {
			coverageKnown = false
		} else if cutoff+1 > coverage {
			// More rows may have been omitted at the oldest retained timestamp.
			// Only a bucket starting strictly after it can be complete.
			coverage = cutoff + 1
		}
	} else if snapshot.OldestStoredObservationAt == nil {
		if !oldestKnown {
			coverageKnown = false
		} else if oldest > coverage {
			coverage = oldest
		}
	}
	if coverageKnown && coverage <= now.Unix() {
		value := crossBorderTime(coverage)
		result.CoveredFromAt = &value
	}
	for i := range result.Timeline {
		point := &result.Timeline[i]
		if coverageKnown && since+int64(i*bucket*60) >= coverage {
			point.Complete = true
			point.Records = dashboardInt(0)
			point.OneByte = dashboardInt(0)
			point.TwoByte = dashboardInt(0)
			point.ThreeByte = dashboardInt(0)
			point.NamedScope = dashboardInt(0)
			point.Unscoped = dashboardInt(0)
			point.UnresolvedScope = dashboardInt(0)
			point.TransportWildcard = dashboardInt(0)
		}
	}
	index := crossBorderDashboardPrefixIndex(snapshot.DashboardNodes)
	identities := map[string]crossBorderNodeIdentity{}
	for _, node := range snapshot.DashboardNodes {
		identities[crossBorderID(node.PublicKey)] = node
	}
	nodes := map[string]crossBorderNode{}
	for _, node := range snapshot.Nodes {
		nodes[crossBorderID(node.PublicKey)] = node
	}
	observers := map[string]CrossBorderObserver{}
	for _, observer := range snapshot.Observers {
		observers[crossBorderID(observer.ID)] = observer
	}
	repeaters := map[string]*crossBorderDashboardRepeaterWork{}
	ensure := func(key string, confirmed bool) *crossBorderDashboardRepeaterWork {
		key = crossBorderID(key)
		identity, ok := identities[key]
		if !ok || identity.Hidden || (identity.Role != "repeater" && identity.Role != "room") {
			return nil
		}
		if old := repeaters[key]; old != nil {
			old.confirmed = old.confirmed || confirmed
			return old
		}
		node, metadataPresent := nodes[key]
		if !metadataPresent {
			result.RepeatersTruncated = true
		}
		if node.Hidden {
			return nil
		}
		v := CrossBorderDashboardRepeater{CrossBorderRepeater: CrossBorderRepeater{PublicKey: key, Name: node.Name, Role: identity.Role, LastSeenAt: node.LastSeenAt, Location: crossBorderLocation(node.Lat, node.Lon), ObservedScopes: []CrossBorderScope{}, HopWidths: []int{}}, AmbiguousWidths: []int{}}
		if node.ConfiguredScope != nil {
			names, wildcard := splitDeclaredRegions(*node.ConfiguredScope)
			v.DeclaredScopes = &CrossBorderDeclaredScopes{Names: names, AllowsUnscoped: wildcard, ReportedAt: node.ScopeAt, Truncated: node.DeclaredTruncated}
		}
		work := &crossBorderDashboardRepeaterWork{value: v, scopes: map[string]CrossBorderScope{}, contents: map[int]bool{}, confirmed: confirmed}
		repeaters[key] = work
		return work
	}
	for key := range observers {
		ensure(key, true)
	}
	scopes := map[string]*CrossBorderScopeCount{}
	regionCounts := map[string]*CrossBorderRegionCount{}
	regionContents := map[string]map[string]bool{}
	for _, region := range regions {
		regionCounts[region] = &CrossBorderRegionCount{IATA: region}
		regionContents[region] = map[string]bool{}
	}
	ambiguous := map[string]bool{}
	for i, row := range rows {
		if i&127 == 0 {
			if err := ctx.Err(); err != nil {
				return result, nil, err
			}
		}
		if row.Timestamp < since || row.Timestamp > now.Unix() || strings.EqualFold(strings.TrimSpace(row.Direction), "tx") {
			result.ExcludedRecords++
			continue
		}
		wire, valid := crossBorderReadWire(row)
		if !valid || row.Hash == "" {
			result.ExcludedRecords++
			continue
		}
		if !crossBorderScopeMatches(wire.scope, f.Scope) {
			continue
		}
		observerID := crossBorderID(row.ObserverID)
		region := strings.ToUpper(strings.TrimSpace(row.ObserverIATA))
		if region == "" {
			region = observers[observerID].IATA
		}
		if regionCounts[region] == nil {
			result.ExcludedRecords++
			continue
		}
		result.Records++
		content := contents[row.Hash]
		if content == nil {
			content = &crossBorderDashboardContent{index: len(contents), independentRegions: map[string]bool{}}
			contents[row.Hash] = content
		}
		if observerID != "" && !shared[observerID] && identityComplete {
			content.independent = true
			content.independentRegions[region] = true
		}
		regionCounts[region].Records++
		regionContents[region][row.Hash] = true
		bucketIndex := int((row.Timestamp - since) / int64(bucket*60))
		if bucketIndex >= len(result.Timeline) {
			bucketIndex = len(result.Timeline) - 1
		}
		point := &result.Timeline[bucketIndex]
		if point.Records != nil {
			*point.Records++
		}
		if (wire.route != RouteFlood && wire.route != RouteTransportFlood) || wire.width < 1 || wire.width > 3 {
			continue
		}
		result.FloodRecords++
		result.Widths[wire.width-1].Records++
		if point.Complete {
			widthValues := []*int{point.OneByte, point.TwoByte, point.ThreeByte}
			*widthValues[wire.width-1]++
		}
		scopeKey, scopeLabel := crossBorderDashboardScope(wire)
		if scopes[scopeKey] == nil {
			scopes[scopeKey] = &CrossBorderScopeCount{Key: scopeKey, Label: scopeLabel}
		}
		scopes[scopeKey].Records++
		if point.Complete {
			switch scopeKey {
			case "unscoped":
				*point.Unscoped++
			case "transport-zero":
				*point.TransportWildcard++
			case "unknown":
				*point.UnresolvedScope++
			default:
				*point.NamedScope++
			}
		}
		if wire.payload == PayloadADVERT {
			if repeater := ensure(row.FromPubkey, true); repeater != nil {
				at := crossBorderTime(row.Timestamp)
				if repeater.value.OwnAdvertAt == nil || at > *repeater.value.OwnAdvertAt {
					repeater.value.OwnAdvertAt = &at
					repeater.value.OwnAdvertWidth = dashboardInt(wire.width)
				} else if at == *repeater.value.OwnAdvertAt && repeater.value.OwnAdvertWidth != nil && *repeater.value.OwnAdvertWidth != wire.width {
					repeater.value.OwnAdvertWidth = nil
				}
			}
		}
		for _, hop := range crossBorderDashboardHops(row, wire) {
			key, known := index[hop]
			if len(hop) < minForwarderHopHexLen || !snapshot.DashboardNodeIndexComplete || key == "" {
				if len(hop) < minForwarderHopHexLen || known {
					ambiguous[hop] = true
				}
				continue
			}
			if repeater := ensure(key, true); repeater != nil {
				repeater.contents[content.index] = true
				repeater.widths[wire.width] = true
				if scopeKey == "transport-zero" {
					repeater.value.ObservedTransportWildcard = true
				} else {
					repeater.scopes[crossBorderScopeKey(wire.scope)] = wire.scope
				}
				repeater.value.ObservedUnscoped = repeater.value.ObservedUnscoped || wire.route == RouteFlood
				repeater.value.ObservedUnknownScope = repeater.value.ObservedUnknownScope || wire.scope.Kind == "unknown"
				at := crossBorderTime(row.Timestamp)
				if repeater.value.LastForwardedAt == nil || at > *repeater.value.LastForwardedAt {
					repeater.value.LastForwardedAt = &at
				}
			}
		}
	}
	// Resolve uncertain prefixes in one pass over known identities, rather
	// than expanding every colliding candidate inside each packet's hop loop.
	for key, identity := range identities {
		if identity.Hidden || !crossBorderPublicKey(key) {
			continue
		}
		for width := 1; width <= 3; width++ {
			if ambiguous[key[:width*2]] {
				if repeater := ensure(key, false); repeater != nil {
					repeater.ambiguous[width] = true
					repeater.value.CarryingAmbiguous = true
				}
			}
		}
	}
	for _, work := range repeaters {
		work.value.SampleForwardedPackets = len(work.contents)
		work.value.ObservedScopes = crossBorderSortedScopes(work.scopes)
		if len(work.value.ObservedScopes) > 32 {
			work.value.ObservedScopes = append([]CrossBorderScope{}, work.value.ObservedScopes[:32]...)
			work.value.ScopesTruncated = true
			result.ScopesTruncated = true
		}
		for width := 1; width <= 3; width++ {
			if work.widths[width] {
				work.value.HopWidths = append(work.value.HopWidths, width)
			}
			if work.ambiguous[width] {
				work.value.AmbiguousWidths = append(work.value.AmbiguousWidths, width)
			}
		}
		if work.value.OwnAdvertWidth != nil {
			result.Widths[*work.value.OwnAdvertWidth-1].Advertisers++
		} else if work.confirmed {
			result.UnknownAdvertisers++
		}
		result.Repeaters = append(result.Repeaters, work.value)
	}
	sort.Slice(result.Repeaters, func(i, j int) bool {
		a, b := result.Repeaters[i], result.Repeaters[j]
		if a.SampleForwardedPackets != b.SampleForwardedPackets {
			return a.SampleForwardedPackets > b.SampleForwardedPackets
		}
		return a.PublicKey < b.PublicKey
	})
	metadataLimit := cfg.MetadataLimit
	if metadataLimit <= 0 || metadataLimit > crossBorderDashboardMetadata {
		metadataLimit = crossBorderDashboardMetadata
	}
	if len(result.Repeaters) > metadataLimit {
		result.Repeaters = append([]CrossBorderDashboardRepeater{}, result.Repeaters[:metadataLimit]...)
		result.RepeatersTruncated = true
	}
	keys := make([]string, 0, len(scopes))
	for key := range scopes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	other, named := 0, 0
	for _, key := range keys {
		if strings.HasPrefix(key, "named:") {
			if named >= 60 {
				other += scopes[key].Records
				continue
			}
			named++
		}
		result.Scopes = append(result.Scopes, *scopes[key])
	}
	if other > 0 {
		result.Scopes = append(result.Scopes, CrossBorderScopeCount{Key: "other", Label: "Other scopes", Records: other})
		result.ScopesTruncated = true
	}
	for _, region := range regions {
		value := regionCounts[region]
		value.Contents = len(regionContents[region])
		result.Regions = append(result.Regions, *value)
	}
	result.Contents = len(contents)
	if result.Records > 0 {
		result.Status = "available"
	}
	var err error
	result.Radio, err = buildCrossBorderDashboardRadio(ctx, snapshot, since, now, metadataLimit)
	return result, contents, err
}
