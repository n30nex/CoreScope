package main

import "context"

func crossBorderHasHiddenNames(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	cfg.IsNameHidden("")
	prefixes := cfg.hiddenPrefixesPtr.Load()
	return prefixes != nil && len(*prefixes) > 0
}

// The legacy response has independently selected/capped metadata. Query the
// union of its candidate identities against each full database's names, so an
// alias cannot bypass a privacy decision on the other side. This is two bulk
// reads only when name hiding is configured; the dashboard already has the
// complete bounded hidden sets and does not call this helper.
func applyCrossBorderJointPrivacy(ctx context.Context, localDB, peerDB *DB, local, peer *crossBorderSnapshot, cfg *Config) error {
	if !crossBorderHasHiddenNames(cfg) {
		return crossBorderApplyHiddenIdentities(ctx, local, peer)
	}
	ids := map[string]bool{}
	add := func(raw string) bool {
		key := crossBorderID(raw)
		if key == "" || ids[key] {
			return true
		}
		if len(ids) >= crossBorderDashboardKeys {
			return false
		}
		ids[key] = true
		return true
	}
	for _, snapshot := range []*crossBorderSnapshot{local, peer} {
		for _, observer := range snapshot.Observers {
			if !add(observer.ID) {
				return errCrossBorderUnavailable
			}
		}
		for _, node := range snapshot.Nodes {
			if !add(node.PublicKey) {
				return errCrossBorderUnavailable
			}
		}
		for i, row := range snapshot.Observations {
			if i&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if !add(row.ObserverID) || !add(row.FromPubkey) {
				return errCrossBorderUnavailable
			}
		}
	}
	if len(ids) > 0 {
		for i, db := range []*DB{localDB, peerDB} {
			if db == nil || db.conn == nil {
				return errCrossBorderUnavailable
			}
			hidden, err := db.crossBorderHiddenIDs(ctx, ids, cfg)
			if err != nil {
				return err
			}
			snapshot := local
			if i == 1 {
				snapshot = peer
			}
			if snapshot.DashboardHiddenIDs == nil {
				snapshot.DashboardHiddenIDs = map[string]bool{}
			}
			for key := range hidden {
				if !snapshot.DashboardHiddenIDs[key] && len(snapshot.DashboardHiddenIDs) >= crossBorderDashboardKeys {
					return errCrossBorderUnavailable
				}
				snapshot.DashboardHiddenIDs[key] = true
			}
		}
	}
	return crossBorderApplyHiddenIdentities(ctx, local, peer)
}

// A name-hidden observer may have no node row on that side of the comparison.
// Preserve the full bounded hidden identity result and apply its policy to
// both datasets before producing any public metadata or inferred paths.
func crossBorderApplyHiddenIdentities(ctx context.Context, local, peer *crossBorderSnapshot) error {
	hidden := map[string]bool{}
	for _, snapshot := range []*crossBorderSnapshot{local, peer} {
		if len(snapshot.DashboardHiddenIDs) > crossBorderDashboardKeys {
			return errCrossBorderUnavailable
		}
		for key := range snapshot.DashboardHiddenIDs {
			hidden[crossBorderID(key)] = true
		}
	}
	if len(hidden) == 0 {
		return ctx.Err()
	}
	keys := crossBorderIdentityKeys(hidden)
	for _, snapshot := range []*crossBorderSnapshot{local, peer} {
		if err := ctx.Err(); err != nil {
			return err
		}
		known := map[string]bool{}
		for i := range snapshot.DashboardNodes {
			node := &snapshot.DashboardNodes[i]
			key := crossBorderID(node.PublicKey)
			known[key] = true
			node.Hidden = node.Hidden || hidden[key]
		}
		// Hidden observer-only keys must also prevent a visible prefix from
		// being called unique. A capped collision index remains incomplete.
		for _, key := range keys {
			if known[key] || !crossBorderPublicKey(key) {
				continue
			}
			if len(snapshot.DashboardNodes) >= crossBorderDashboardKeys {
				snapshot.DashboardNodeIndexComplete = false
				break
			}
			snapshot.DashboardNodes = append(snapshot.DashboardNodes, crossBorderNodeIdentity{PublicKey: key, Hidden: true})
			known[key] = true
		}
		for i := range snapshot.Nodes {
			node := &snapshot.Nodes[i]
			node.Hidden = node.Hidden || hidden[crossBorderID(node.PublicKey)]
		}
		observers := snapshot.Observers[:0]
		for _, observer := range snapshot.Observers {
			if !hidden[crossBorderID(observer.ID)] {
				observers = append(observers, observer)
			}
		}
		snapshot.Observers = observers
		shared := snapshot.SharedObserverIDs[:0]
		for _, id := range snapshot.SharedObserverIDs {
			if !hidden[crossBorderID(id)] {
				shared = append(shared, id)
			}
		}
		snapshot.SharedObserverIDs = shared
		rows := snapshot.Observations[:0]
		for i, row := range snapshot.Observations {
			if i&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if !hidden[crossBorderID(row.ObserverID)] && !hidden[crossBorderID(row.FromPubkey)] {
				rows = append(rows, row)
			}
		}
		snapshot.Observations = rows
		metrics := snapshot.DashboardMetrics[:0]
		for _, metric := range snapshot.DashboardMetrics {
			if !hidden[crossBorderID(metric.Observer.ID)] {
				metrics = append(metrics, metric)
			}
		}
		snapshot.DashboardMetrics = metrics
	}
	return ctx.Err()
}
