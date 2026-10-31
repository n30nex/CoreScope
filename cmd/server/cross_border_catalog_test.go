package main

import "testing"

func TestCrossBorderScopeCatalogPreservesDocumentedAndDiscoveredNames(t *testing.T) {
	snapshot := CrossBorderMeshMapperSnapshot{Scopes: []CrossBorderMeshMapperScope{
		{Name: "ca", Region: "YKF", Repeaters: 1},
		{Name: "zero-count", Region: "YKF", Monitored: true},
		{Name: "Case", Region: "BUF", Repeaters: 1},
		{Name: "case", Region: "BUF", Repeaters: 1},
		{Name: "*", Region: "BUF", Repeaters: 1},
	}}
	entries := crossBorderScopeCatalog([]string{"YKF", "YYZ", "BUF"}, snapshot)
	byName := map[string]CrossBorderScopeCatalogEntry{}
	for _, entry := range entries {
		if _, duplicate := byName[entry.Name]; duplicate {
			t.Errorf("duplicate scope %q", entry.Name)
		}
		byName[entry.Name] = entry
	}
	for _, name := range []string{"ykf", "yyz", "na", "can", "on", "qc", "onqc", "onny", "us", "east", "northeast", "us-ny", "wny", "ca", "zero-count", "Case", "case"} {
		if _, exists := byName[name]; !exists {
			t.Errorf("missing scope %q", name)
		}
	}
	if _, exists := byName["*"]; exists {
		t.Error("wildcard belongs to unscoped evidence, not named catalog")
	}
	if entry := byName["onqc"]; len(entry.Sources) != 1 || entry.Sources[0].Status != "proposal" {
		t.Errorf("ON/QC guide must remain a proposal: %+v", entry)
	}
	if entry := byName["ca"]; len(entry.MeshMapperRegions) != 1 || entry.MeshMapperRegions[0] != "YKF" {
		t.Errorf("ca must remain distinct from documented can: %+v", entry)
	}
	if len(byName["ykf"].Sources) == 0 {
		t.Error("IATA reference needs source provenance")
	}
}

func TestCrossBorderScopeCatalogReviewedCaReferenceSurvivesOutage(t *testing.T) {
	catalog := crossBorderScopeCatalog([]string{"YKF"}, CrossBorderMeshMapperSnapshot{Status: "access_denied"})
	var ca, can *CrossBorderScopeCatalogEntry
	for i := range catalog {
		if catalog[i].Name == "ca" {
			ca = &catalog[i]
		}
		if catalog[i].Name == "can" {
			can = &catalog[i]
		}
	}
	if ca == nil || can == nil {
		t.Fatal("reviewed ca reference and documented can must both remain selectable")
	}
	if len(ca.Sources) != 1 || ca.Sources[0].Status != "reference" || ca.Sources[0].URL != "https://ykf.meshmapper.net/" {
		t.Fatalf("reviewed name falsely presented as a live API observation: %+v", ca)
	}
}
