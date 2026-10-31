package main

import (
	"sort"
	"strings"
)

type CrossBorderScopeCatalogSource struct {
	Label  string `json:"label"`
	URL    string `json:"url"`
	Status string `json:"status"`
}

type CrossBorderScopeCatalogEntry struct {
	Name              string                          `json:"name"`
	Label             string                          `json:"label"`
	Sources           []CrossBorderScopeCatalogSource `json:"sources"`
	MeshMapperRegions []string                        `json:"meshMapperRegions"`
}

// Keep one overflow entry internally. A resolver receiving more than128 names
// can abstain instead of treating a truncated candidate set as collision-free.
func crossBorderScopeCatalog(regions []string, snapshot CrossBorderMeshMapperSnapshot, extra ...CrossBorderScopeCatalogEntry) []CrossBorderScopeCatalogEntry {
	const limit = 129
	byName := map[string]*CrossBorderScopeCatalogEntry{}
	add := func(name string, source CrossBorderScopeCatalogSource, region string) {
		name, valid := meshMapperScopeName(name)
		if !valid || name == "*" {
			return
		}
		entry := byName[name]
		if entry == nil {
			if len(byName) == limit {
				return
			}
			entry = &CrossBorderScopeCatalogEntry{Name: name, Label: name, Sources: []CrossBorderScopeCatalogSource{}, MeshMapperRegions: []string{}}
			byName[name] = entry
		}
		if source.URL != "" {
			found := false
			for _, known := range entry.Sources {
				if known == source {
					found = true
					break
				}
			}
			if !found {
				entry.Sources = append(entry.Sources, source)
			}
		}
		if region != "" {
			found := false
			for _, known := range entry.MeshMapperRegions {
				if known == region {
					found = true
					break
				}
			}
			if !found && len(entry.MeshMapperRegions) < meshMapperMaxRegions {
				entry.MeshMapperRegions = append(entry.MeshMapperRegions, region)
			}
		}
	}
	// References describe public naming guidance, not deployment or adoption.
	for _, guide := range []struct {
		names  []string
		source CrossBorderScopeCatalogSource
	}{
		{[]string{"na", "can", "on", "onny", "yyz"}, CrossBorderScopeCatalogSource{"WNY GTA settings", "https://wnymeshcore.org/region-settings/gta", "published"}},
		{[]string{"na", "us", "east", "northeast", "us-ny", "wny", "onny"}, CrossBorderScopeCatalogSource{"WNY region settings", "https://wnymeshcore.org/region-settings", "published"}},
		{[]string{"na", "can", "on", "qc", "onqc", "yyz", "ykf", "ygk", "yow", "yul", "yqb", "ytf"}, CrossBorderScopeCatalogSource{"ON/QC proposal", "https://meshcore.ca/proposals/onqc-scopes/#phase-1-repeater-setup", "proposal"}},
		{[]string{"ca"}, CrossBorderScopeCatalogSource{"MeshMapper YKF review · 2026-10-31", "https://ykf.meshmapper.net/", "reference"}},
	} {
		for _, name := range guide.names {
			add(name, guide.source, "")
		}
	}
	for _, region := range regions {
		name := strings.ToLower(region)
		if _, exists := byName[name]; exists {
			add(name, CrossBorderScopeCatalogSource{"Canadian region reference", "https://canadaverse.org/meshcore-canada/config/map/", "reference"}, "")
		}
	}
	for _, entry := range extra {
		if len(entry.Sources) == 0 {
			add(entry.Name, CrossBorderScopeCatalogSource{}, "")
		}
		for _, source := range entry.Sources {
			add(entry.Name, source, "")
		}
	}
	discovered := CrossBorderScopeCatalogSource{"MeshMapper scope evidence", "https://wiki.meshmapper.net/scopes-api/", "discovered"}
	for _, scope := range snapshot.Scopes {
		add(scope.Name, discovered, scope.Region)
	}
	for _, repeater := range snapshot.Repeaters {
		for _, scope := range repeater.Scopes {
			for _, region := range scope.SourceRegions {
				add(scope.Name, discovered, region)
			}
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]CrossBorderScopeCatalogEntry, 0, len(names))
	for _, name := range names {
		sort.Strings(byName[name].MeshMapperRegions)
		result = append(result, *byName[name])
	}
	return result
}
