package main

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func assertEvictedNodeMemberships(t *testing.T, s *PacketStore, removed map[int]bool) {
	t.Helper()
	for key, list := range s.byNode {
		for _, tx := range list[:cap(list)] {
			if tx != nil && removed[tx.ID] {
				t.Errorf("node index %q retains evicted transmission %d", key, tx.ID)
			}
		}
		for hash := range s.nodeHashes[key] {
			if s.byHash[hash] == nil {
				t.Errorf("node hash index %q retains evicted hash %q", key, hash)
			}
		}
	}
	for key, hashes := range s.nodeHashes {
		if len(hashes) == 0 || len(s.byNode[key]) == 0 {
			t.Errorf("node hash index %q has no live memberships", key)
		}
	}
}

func TestEvictionOwnsResolvedNodeMemberships(t *testing.T) {
	for _, mode := range []string{"missing", "different", "expanded", "matching", "index-disabled"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now().UTC()
			s := makeTestStore(4, now.Add(-30*time.Minute), 0)
			s.retentionHours = 1
			s.useResolvedPathIndex = mode != "index-disabled"
			s.initResolvedPathIndex()
			s.byPathHop = make(map[string][]*StoreTx)
			relay, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
			for _, tx := range s.packets {
				s.indexResolvedPathHops(tx, []string{relay}, make(map[string]bool))
			}
			s.packets[0].FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
			batch := map[int][]string{}
			switch mode {
			case "different":
				batch[1] = []string{other}
			case "expanded", "matching":
				batch[1] = []string{relay}
			}
			removed := map[int]bool{1: true}
			if mode == "expanded" {
				// The SQL batch was selected while only ID 1 was eligible.
				// Expiration can advance before the actual eviction lock is acquired.
				s.packets[1].FirstSeen = now.Add(-90 * time.Minute).Format(time.RFC3339)
				removed[2] = true
			}
			// Even a duplicate index entry must not keep an evicted packet alive.
			s.byNode[relay] = append(s.byNode[relay], s.packets[0])
			s.mu.Lock()
			got := s.EvictStaleWithRP(batch)
			s.mu.Unlock()
			if got != len(removed) {
				t.Fatalf("evicted %d packets, want %d", got, len(removed))
			}
			assertEvictedNodeMemberships(t, s, removed)
			if len(s.byNode[relay]) != 4-len(removed) {
				t.Errorf("resolved relay has %d memberships, want %d live packets", len(s.byNode[relay]), 4-len(removed))
			}
			for _, tx := range s.packets {
				if !s.nodeHashes[relay][tx.Hash] {
					t.Errorf("lost surviving hash %q", tx.Hash)
				}
			}
		})
	}
}

func TestEvictionAfterLocalPathResolution(t *testing.T) {
	for _, producer := range []string{"cold-missing", "ingest-different", "late-missing"} {
		t.Run(producer, func(t *testing.T) {
			relay := strings.Repeat("a", 64)
			other := strings.Repeat("b", 64)
			dsn := createTestDBWithResolvedPath(t, 2, nil)
			w, err := openFixtureSQL(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if _, err = w.Exec("INSERT INTO nodes(public_key,name,role) VALUES($1,'Synthetic relay','repeater')", relay); err != nil {
				t.Fatal(err)
			}
			var persisted any
			if producer == "ingest-different" {
				persisted = fmt.Sprintf(`[%q]`, other)
			}
			path := `["aa"]`
			if producer == "late-missing" {
				path = `[]`
			}
			if _, err = w.Exec("UPDATE observations SET path_json=$1, resolved_path=$2", path, persisted); err != nil {
				t.Fatal(err)
			}
			db, err := openFixtureReader(t, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			s := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 24})
			now := time.Now().UTC()
			if producer == "cold-missing" {
				if err := s.loadChunk(now.Add(-72*time.Hour), now); err != nil {
					t.Fatal(err)
				}
			} else {
				s.IngestNewFromDB(0, 10)
				if producer == "late-missing" {
					for id := 1; id <= 2; id++ {
						if _, err = w.Exec(`INSERT INTO observations(id,transmission_id,observer_id,path_json,timestamp)
							VALUES($1,$2,'late-observer','["aa"]',$3)`, id+2, id, now.Add(-48*time.Hour).Format(time.RFC3339)); err != nil {
							t.Fatal(err)
						}
					}
					s.IngestNewObservations(2, 10)
				}
			}
			if len(s.byNode[relay]) != 2 {
				t.Fatalf("producer did not locally resolve both packets: %d memberships", len(s.byNode[relay]))
			}
			if got := s.RunEviction(); got != 2 {
				t.Fatalf("evicted %d packets, want 2", got)
			}
			assertEvictedNodeMemberships(t, s, map[int]bool{1: true, 2: true})
			var count int
			if err := w.QueryRow("SELECT COUNT(*) FROM transmissions").Scan(&count); err != nil || count != 2 {
				t.Fatalf("cache eviction changed SQL history: count=%d, err=%v", count, err)
			}
		})
	}
}

func TestEvictionReleasesPrivateSliceTails(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(4, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	for _, tx := range s.packets[:2] {
		tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	}
	// Cold load can append newest-first buckets. Removing their oldest tail
	// must clear it, not just shorten the visible slice.
	slices.Reverse(s.byPayloadType[4])
	for _, list := range s.byObserver {
		slices.Reverse(list)
	}
	s.spTxIndex = make(map[string][]*StoreTx)
	s.spIndex = make(map[string]int)
	for i := len(s.packets) - 1; i >= 0; i-- {
		addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, s.packets[i])
	}
	if got := s.RunEviction(); got != 2 {
		t.Fatalf("evicted %d packets, want 2", got)
	}
	check := func(owner string, list []*StoreTx) {
		t.Helper()
		for _, tx := range list[:cap(list)] {
			if tx != nil && tx.ID <= 2 {
				t.Errorf("%s backing array retains evicted transmission %d", owner, tx.ID)
			}
		}
	}
	check("payload", s.byPayloadType[4])
	for key, list := range s.spTxIndex {
		check("subpath "+key, list)
	}
	for key, list := range s.byObserver {
		for _, obs := range list[:cap(list)] {
			if obs != nil && obs.TransmissionID <= 2 {
				t.Errorf("observer %q backing array retains evicted observation %d", key, obs.ID)
			}
		}
	}
	// An empty primary slice must not keep its former contents alive.
	for _, tx := range s.packets {
		tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	}
	if got := s.RunEviction(); got != 2 {
		t.Fatalf("final eviction removed %d packets, want 2", got)
	}
	for _, tx := range s.packets[:cap(s.packets)] {
		if tx != nil {
			t.Error("empty packet store retains a transmission in its backing array")
			break
		}
	}
}

func TestPathHopRemovalReleasesTail(t *testing.T) {
	old, live := &StoreTx{ID: 1}, &StoreTx{ID: 2}
	idx := map[string][]*StoreTx{"aa": {live, old}}
	removeTxFromSlice(idx, "aa", old)
	if !reflect.DeepEqual(idx["aa"], []*StoreTx{live}) {
		t.Fatal("path-hop removal lost its survivor")
	}
	for _, tx := range idx["aa"][:cap(idx["aa"])] {
		if tx == old {
			t.Fatal("removed path-hop transmission remains in the backing array")
		}
	}
}

func TestEvictionPreservesDistanceSnapshots(t *testing.T) {
	for _, operation := range []string{"eviction", "path-update"} {
		t.Run(operation, func(t *testing.T) {
			now := time.Now().UTC()
			s := makeTestStore(4, now.Add(-30*time.Minute), 0)
			s.retentionHours = 1
			s.packets[0].FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
			// Use the same snapshot boundary as computeAnalyticsDistance. The
			// arrays it owns must survive later eviction and live path updates.
			s.mu.RLock()
			hops, paths := s.snapshotDistanceIndexLocked()
			s.mu.RUnlock()
			wantHops, wantPaths := slices.Clone(hops), slices.Clone(paths)
			if operation == "eviction" {
				s.RunEviction()
			} else {
				s.nodeCacheTime = now
				s.nodePM = &prefixMap{m: map[string][]nodeInfo{}}
				s.mu.Lock()
				s.updateDistanceIndexForTxs(s.packets[:1])
				s.mu.Unlock()
			}
			if !reflect.DeepEqual(hops, wantHops) || !reflect.DeepEqual(paths, wantPaths) {
				t.Error("writer modified a distance snapshot borrowed by an unlocked reader")
			}
			for _, hop := range s.distHops[:cap(s.distHops)] {
				if hop.tx != nil && hop.tx.ID == 1 {
					t.Error("distance hop backing array retains a removed record")
				}
			}
			for _, path := range s.distPaths[:cap(s.distPaths)] {
				if path.tx != nil && path.tx.ID == 1 {
					t.Error("distance path backing array retains a removed record")
				}
			}
		})
	}
}

func TestEvictionReleasesGroupedCache(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(4, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	if got := s.QueryGroupedPackets(PacketQuery{}).Total; got != 4 {
		t.Fatalf("warm grouped total=%d, want 4", got)
	}
	// A returned snapshot is allowed to finish after eviction. Invalidating the
	// cache must release its root without clearing an active reader's array.
	reader := s.groupedCacheTxs
	s.groupedCacheExp = now.Add(-time.Second)
	s.packets[0].FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	s.RunEviction()
	if s.groupedCacheTxs != nil {
		t.Error("eviction retained the expired grouped packet cache")
	}
	if len(reader) != 4 || reader[0] == nil {
		t.Error("cache invalidation corrupted a borrowed grouped snapshot")
	}
	if got := s.QueryGroupedPackets(PacketQuery{}).Total; got != 3 {
		t.Errorf("grouped total after eviction=%d, want 3", got)
	}
}

func TestGroupedCacheRejectsPreEvictionSnapshot(t *testing.T) {
	now := time.Now().UTC()
	s := makeTestStore(4, now.Add(-30*time.Minute), 0)
	s.retentionHours = 1
	s.QueryGroupedPackets(PacketQuery{})
	oldSnapshot := s.groupedCacheTxs
	oldGeneration := atomic.LoadInt64(&s.evicted)
	s.packets[0].FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
	s.RunEviction()
	// A query that captured its pointers before eviction can finish sorting
	// afterwards. Exercise its real publication step in that exact order.
	s.cacheGroupedPackets(oldSnapshot, "retired", oldGeneration)
	if s.groupedCacheTxs != nil {
		t.Fatal("late grouped-cache publication retained a pre-eviction snapshot")
	}
	s.cacheGroupedPackets(slices.Clone(s.packets), "live", atomic.LoadInt64(&s.evicted))
	if len(s.groupedCacheTxs) != 3 || s.groupedCacheKey != "live" {
		t.Fatal("current grouped snapshot was not published")
	}
}
