package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrossBorderMetadataKeySources(t *testing.T) {
	t.Setenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY", "")
	t.Setenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY_FILE", "")
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("fixture-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY_FILE", file)
	if key, err := crossBorderMeshMapperKey(); err != nil || key != "fixture-key" {
		t.Fatal("file credential was not read")
	}
	t.Setenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY", "another-fixture")
	if _, err := crossBorderMeshMapperKey(); err == nil {
		t.Fatal("ambiguous credential sources accepted")
	}
	t.Setenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY_FILE", "")
	if key, err := crossBorderMeshMapperKey(); err != nil || key != "another-fixture" {
		t.Fatal("environment credential was not read")
	}
	t.Setenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY", "header\r\ninjection")
	if _, err := crossBorderMeshMapperKey(); err == nil {
		t.Fatal("invalid header credential accepted")
	}
}

func TestCrossBorderMetadataPrivacyAndPersistentScopes(t *testing.T) {
	key := func(s string) string { return strings.Repeat(s, 64) }
	provider, _ := newCrossBorderMeshMapper([]string{"YKF"}, "")
	provider.snapshot.Store(&CrossBorderMeshMapperSnapshot{Status: "ready", Revision: 1, Regions: []CrossBorderMeshMapperRegion{}, Scopes: []CrossBorderMeshMapperScope{}, Repeaters: []CrossBorderMeshMapperRepeater{
		{PublicKey: key("a"), Name: "Visible", Enabled: 1, SourceRegions: []string{"YKF"}, Scopes: []CrossBorderMeshMapperScopeEvidence{{Name: "external", SourceRegions: []string{"YKF"}}}},
		{PublicKey: key("b"), Name: "Hidden in the other feed", Enabled: 1},
		{PublicKey: key("c"), Name: "External only", Enabled: 1},
		{PublicKey: key("d"), Name: "Disabled", Enabled: 0},
		{PublicKey: key("e"), Name: "Private external alias", Enabled: 1},
		{PublicKey: key("f"), Name: "Observer-only hidden identity", Enabled: 1},
	}})
	cfg := &Config{}
	cfg.SetHiddenNamePrefixes([]string{"Private"})
	svc := &crossBorderService{serverCfg: cfg, meshMapper: provider}
	local := crossBorderSnapshot{Available: true, Nodes: []crossBorderNode{
		{PublicKey: key("a"), Name: "Visible", Role: "repeater", ConfiguredScope: crossBorderString("native,Case,*")},
		{PublicKey: key("b"), Name: "Visible local alias", Role: "repeater"},
		{PublicKey: key("d"), Name: "Disabled", Role: "repeater"},
		{PublicKey: key("e"), Name: "Other alias", Role: "repeater"},
		{PublicKey: key("f"), Name: "Visible node alias", Role: "repeater"},
	}, DashboardNodes: []crossBorderNodeIdentity{{PublicKey: key("a")}, {PublicKey: key("b")}, {PublicKey: key("d")}, {PublicKey: key("e")}}}
	peer := crossBorderSnapshot{DashboardNodes: []crossBorderNodeIdentity{{PublicKey: key("b"), Hidden: true}}, DashboardHiddenIDs: map[string]bool{key("f"): true}}
	f := CrossBorderFilters{Scope: "named:all", LocalIATA: []string{"YKF"}, PeerIATA: []string{"BUF"}}
	metadata, catalog := svc.dashboardMetadata(local, peer, f)
	if len(metadata.Repeaters) != 1 || metadata.Repeaters[0].PublicKey != key("a") {
		t.Fatalf("external metadata bypassed native visibility: %+v", metadata.Repeaters)
	}
	byName := map[string]bool{}
	for _, entry := range catalog {
		byName[entry.Name] = true
	}
	for _, name := range []string{"native", "Case", "external", "ca", "can", "all", "onny", "onqc", "ykf"} {
		if !byName[name] {
			t.Errorf("missing stable/selected/native scope %q", name)
		}
	}
	response := CrossBorderResponse{Filters: f, Options: CrossBorderOptions{Scopes: []CrossBorderScopeOption{{Value: "all"}, {Value: "unscoped"}, {Value: "unknown"}, {Value: "seen-on-packet"}}}}
	applyCrossBorderMetadata(&response, metadata, catalog)
	options := map[string]bool{}
	for _, option := range response.Options.Scopes {
		options[option.Value] = true
	}
	for _, value := range []string{"all", "unscoped", "unknown", "named:all", "seen-on-packet", "onny", "ykf"} {
		if !options[value] {
			t.Errorf("missing scope selection %q", value)
		}
	}
	if response.MeshMapper == nil || len(response.ScopeCatalog) == 0 {
		t.Fatal("metadata/catalog not attached")
	}
	if len(provider.Snapshot().Repeaters) != 6 {
		t.Fatal("privacy filtering mutated provider cache")
	}
}

func TestCrossBorderMetadataRevisionInvalidatesCacheAndInflight(t *testing.T) {
	provider, _ := newCrossBorderMeshMapper(nil, "")
	provider.snapshot.Store(&CrossBorderMeshMapperSnapshot{Revision: 1})
	svc := &crossBorderService{cfg: crossBorderTestConfig(), meshMapper: provider}
	calls := 0
	svc.load = func(context.Context, CrossBorderFilters) (CrossBorderResponse, error) {
		calls++
		return CrossBorderResponse{Status: "ready"}, nil
	}
	if _, err := svc.response(context.Background(), crossBorderTestFilters()); err != nil {
		t.Fatal(err)
	}
	provider.snapshot.Store(&CrossBorderMeshMapperSnapshot{Revision: 2, Status: "access_denied"})
	if _, err := svc.response(context.Background(), crossBorderTestFilters()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("external access change reused old cached report")
	}
	started, release := make(chan struct{}), make(chan struct{})
	svc.load = func(context.Context, CrossBorderFilters) (CrossBorderResponse, error) {
		close(started)
		<-release
		return CrossBorderResponse{Status: "old-private-metadata"}, nil
	}
	provider.snapshot.Store(&CrossBorderMeshMapperSnapshot{Revision: 3})
	done := make(chan error, 1)
	go func() { _, err := svc.response(context.Background(), crossBorderTestFilters()); done <- err }()
	<-started
	provider.snapshot.Store(&CrossBorderMeshMapperSnapshot{Revision: 4, Status: "access_denied"})
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pre-revocation in-flight report was returned")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight report did not finish")
	}
}

func TestCrossBorderMetadataCloseCancelsProvider(t *testing.T) {
	cancelled := false
	svc := &crossBorderService{meshMapperCancel: func() { cancelled = true }}
	(&Server{crossBorder: svc}).closeCrossBorder()
	if !cancelled {
		t.Fatal("closing server left metadata polling alive")
	}
}

func TestCrossBorderMetadataCatalogOverflowIsVisibleToResolver(t *testing.T) {
	provider, _ := newCrossBorderMeshMapper(nil, "")
	snapshot := CrossBorderMeshMapperSnapshot{Status: "ready", Regions: []CrossBorderMeshMapperRegion{}, Repeaters: []CrossBorderMeshMapperRepeater{}, Scopes: []CrossBorderMeshMapperScope{}}
	for i := 0; i < 200; i++ {
		snapshot.Scopes = append(snapshot.Scopes, CrossBorderMeshMapperScope{Name: fmt.Sprintf("extra-%03d", i), Region: "YKF"})
	}
	provider.snapshot.Store(&snapshot)
	svc := &crossBorderService{meshMapper: provider}
	f := CrossBorderFilters{Scope: "selected-zero"}
	metadata, catalog := svc.dashboardMetadata(crossBorderSnapshot{}, crossBorderSnapshot{}, f)
	if len(catalog) != 129 {
		t.Fatalf("resolver needs overflow evidence, got %d candidates", len(catalog))
	}
	response := CrossBorderResponse{Filters: f}
	applyCrossBorderMetadata(&response, metadata, catalog)
	if !response.ScopeCatalogTruncated || len(response.ScopeCatalog) > 128 {
		t.Fatal("public catalogue did not expose its bound")
	}
	found := false
	for _, entry := range response.ScopeCatalog {
		if entry.Name == "selected-zero" {
			found = true
		}
	}
	if !found {
		t.Fatal("selected zero-count scope was evicted by discovered names")
	}
}
