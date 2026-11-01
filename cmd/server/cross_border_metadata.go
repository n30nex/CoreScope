package main

import (
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

func (svc *crossBorderService) dashboardMetadata(local, peer crossBorderSnapshot, f CrossBorderFilters) (CrossBorderMeshMapperSnapshot, []CrossBorderScopeCatalogEntry) {
	metadata := svc.meshMapper.Snapshot()
	if svc.meshMapperError != "" {
		metadata.Status = "unavailable"
		metadata.Partial = true
	}
	// Only identities already eligible for the selected native report can be
	// enriched. A hidden identity in either complete collision index wins over
	// a public alias in the other feed or in MeshMapper.
	allowed := map[string]bool{}
	for _, snapshot := range []crossBorderSnapshot{local, peer} {
		for _, node := range snapshot.Nodes {
			if crossBorderPublicKey(node.PublicKey) && !node.Hidden && !svc.serverCfg.IsBlacklisted(node.PublicKey) && !svc.serverCfg.IsNameHidden(node.Name) && (node.Role == "repeater" || node.Role == "room") {
				allowed[crossBorderID(node.PublicKey)] = true
			}
		}
	}
	for _, snapshot := range []crossBorderSnapshot{local, peer} {
		for key, hidden := range snapshot.DashboardHiddenIDs {
			if hidden {
				delete(allowed, crossBorderID(key))
			}
		}
		for _, node := range snapshot.DashboardNodes {
			if node.Hidden || svc.serverCfg.IsBlacklisted(node.PublicKey) {
				delete(allowed, crossBorderID(node.PublicKey))
			}
		}
		for _, node := range snapshot.Nodes {
			if node.Hidden || svc.serverCfg.IsNameHidden(node.Name) {
				delete(allowed, crossBorderID(node.PublicKey))
			}
		}
	}
	public := make([]CrossBorderMeshMapperRepeater, 0, len(metadata.Repeaters))
	for _, repeater := range metadata.Repeaters {
		if allowed[crossBorderID(repeater.PublicKey)] && repeater.Enabled != 0 && !svc.serverCfg.IsNameHidden(repeater.Name) {
			public = append(public, repeater)
		}
	}
	metadata.Repeaters = public
	// Catalogue entries describe names, not traffic totals. Public MeshMapper
	// aggregate counts keep their original regional populations and are never
	// substituted for this report's packet denominator.
	extra := []CrossBorderScopeCatalogEntry{}
	add := func(name, label, status string) {
		extra = append(extra, CrossBorderScopeCatalogEntry{Name: name, Label: name, Sources: []CrossBorderScopeCatalogSource{{Label: label, URL: "#/cross-border", Status: status}}})
	}
	if selected, named := crossBorderSelectedScope(f.Scope); named {
		add(selected, "Selected scope", "selection")
	}
	native := map[string]string{}
	for _, snapshot := range []crossBorderSnapshot{local, peer} {
		for _, node := range snapshot.Nodes {
			if !allowed[crossBorderID(node.PublicKey)] || node.ConfiguredScope == nil || (node.DeclaredTruncated != nil && *node.DeclaredTruncated) {
				continue
			}
			names, _ := splitDeclaredRegions(*node.ConfiguredScope)
			for _, name := range names {
				if len(native) < 129 || native[name] != "" {
					native[name] = "reported"
				}
			}
		}
		for _, row := range snapshot.Observations {
			wire, valid := crossBorderReadWire(row)
			if valid && wire.scope.Name != nil && (len(native) < 129 || native[*wire.scope.Name] != "") {
				native[*wire.scope.Name] = "observed"
			}
		}
	}
	names := make([]string, 0, len(native))
	for name := range native {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		label := "CoreScope repeater report"
		if native[name] == "observed" {
			label = "CoreScope packet evidence"
		}
		add(name, label, native[name])
	}
	regions := append(append([]string{}, f.LocalIATA...), f.PeerIATA...)
	return metadata, crossBorderScopeCatalog(regions, metadata, extra...)
}

func applyCrossBorderMetadata(response *CrossBorderResponse, metadata CrossBorderMeshMapperSnapshot, catalog []CrossBorderScopeCatalogEntry) {
	// Resolver matches are only available after the dashboard was built. Add
	// that provenance using this same metadata snapshot, with no second API read.
	byName := map[string]int{}
	for i, entry := range catalog {
		byName[entry.Name] = i
	}
	addMatch := func(name string) {
		name, valid := meshMapperScopeName(name)
		if !valid || name == "*" {
			return
		}
		i, exists := byName[name]
		if !exists {
			if len(catalog) >= 129 {
				return
			}
			i = len(catalog)
			byName[name] = i
			catalog = append(catalog, CrossBorderScopeCatalogEntry{Name: name, Label: name, Sources: []CrossBorderScopeCatalogSource{}, MeshMapperRegions: []string{}})
		}
		for _, source := range catalog[i].Sources {
			if source.Status == "observed" || source.Status == "matched" {
				return
			}
		}
		catalog[i].Sources = append(catalog[i].Sources, CrossBorderScopeCatalogSource{Label: "CoreScope packet match", URL: "#/cross-border", Status: "matched"})
	}
	if response.Dashboard != nil {
		for _, source := range []CrossBorderDashboardSource{response.Dashboard.Local, response.Dashboard.Peer} {
			for _, scope := range source.Scopes {
				if scope.Records > 0 && strings.HasPrefix(scope.Key, "named:") {
					addMatch(strings.TrimPrefix(scope.Key, "named:"))
				}
			}
		}
	}
	response.MeshMapper = &metadata
	response.ScopeCatalogTruncated = len(catalog) > 128
	if len(catalog) > 128 {
		// Published references and the selected zero-count name must not vanish
		// merely because discovery filled the display budget.
		remove := len(catalog) - 1
		for i := len(catalog) - 1; i >= 0; i-- {
			keep := false
			for _, source := range catalog[i].Sources {
				if source.Status == "published" || source.Status == "proposal" || source.Status == "reference" || source.Status == "selection" {
					keep = true
					break
				}
			}
			if !keep {
				remove = i
				break
			}
		}
		catalog = append(append([]CrossBorderScopeCatalogEntry{}, catalog[:remove]...), catalog[remove+1:]...)
		if len(catalog) > 128 {
			catalog = catalog[:128]
		}
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	response.ScopeCatalog = catalog
	options := []CrossBorderScopeOption{{Value: "all", Label: "All scopes"}, {Value: "unscoped", Label: "Unscoped traffic"}, {Value: "unknown", Label: "Scoped · name unknown"}}
	seen := map[string]bool{"all": true, "unscoped": true, "unknown": true}
	addOption := func(name string) {
		value := crossBorderScopeOptionValue(name)
		if value != "" && !seen[value] && len(options) < 132 {
			seen[value] = true
			options = append(options, CrossBorderScopeOption{Value: value, Label: name})
		}
	}
	if selected, named := crossBorderSelectedScope(response.Filters.Scope); named {
		addOption(selected)
	}
	for _, entry := range catalog {
		addOption(entry.Name)
	}
	for _, option := range response.Options.Scopes {
		if name, named := crossBorderSelectedScope(option.Value); named {
			addOption(name)
		}
	}
	response.Options.Scopes = options
}

func crossBorderSelectedScope(filter string) (string, bool) {
	if strings.HasPrefix(filter, "named:") {
		return strings.TrimPrefix(filter, "named:"), true
	}
	return filter, filter != "" && filter != "all" && filter != "unscoped" && filter != "unknown"
}

func crossBorderScopeOptionValue(name string) string {
	if name == "all" || name == "unscoped" || name == "unknown" || strings.HasPrefix(name, "named:") {
		return "named:" + name
	}
	return name
}

func crossBorderMeshMapperKey() (string, error) {
	key := strings.TrimSpace(os.Getenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY"))
	file := strings.TrimSpace(os.Getenv("CORESCOPE_CROSS_BORDER_MESHMAPPER_API_KEY_FILE"))
	if key != "" && file != "" {
		return "", errors.New("ambiguous MeshMapper credential sources")
	}
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return "", errors.New("MeshMapper credential file unavailable")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 4097))
		if err != nil || len(data) > 4096 {
			return "", errors.New("invalid MeshMapper credential file")
		}
		key = strings.TrimSpace(string(data))
	}
	if len(key) > 4096 {
		return "", errors.New("invalid MeshMapper credential")
	}
	for _, c := range key {
		if c <= 32 || c > 126 {
			return "", errors.New("invalid MeshMapper credential")
		}
	}
	return key, nil
}
