package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strings"
)

type CrossBorderScopeResolution struct {
	Checked int  `json:"checked"`
	Matched int  `json:"matched"`
	Limited bool `json:"limited"`
}

func resolveCrossBorderScopes(ctx context.Context, snapshots []*crossBorderSnapshot, names []string) (CrossBorderScopeResolution, error) {
	var stats CrossBorderScopeResolution
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	known := []string{}
	seen := map[string]bool{}
	for _, candidate := range names {
		name := normScope(strings.TrimSpace(candidate))
		if name == "" || len(name) > 128 || isScopeWildcard(name) || seen[name] {
			continue
		}
		if len(known) == 128 {
			stats.Limited = true
			break
		}
		seen[name] = true
		known = append(known, name)
	}
	// A name omitted by this bound could be a colliding candidate. Do not
	// manufacture uniqueness from a caller-supplied catalog we did not finish.
	if stats.Limited || len(known) == 0 {
		return stats, ctx.Err()
	}
	macs := make([]hash.Hash, len(known))
	for i, name := range known {
		macs[i] = newRegionMAC(name)
	}
	// One same-content packet may be re-scoped on another observed path. Include
	// code and payload digest, never just its canonical transmission scope.
	type key struct {
		hash, code  string
		payloadType byte
		payload     [32]byte
	}
	cache := map[key]*CrossBorderScope{}
	for _, snapshot := range snapshots {
		if snapshot == nil {
			continue
		}
		for i := range snapshot.Observations {
			if i&63 == 0 {
				if err := ctx.Err(); err != nil {
					return stats, err
				}
			}
			row := &snapshot.Observations[i]
			wire, valid := crossBorderReadWire(*row)
			if !valid || wire.scope.Kind != "unknown" || row.Hash == "" || (row.RawHexLength != 0 && row.RawHexLength != len(row.RawHex)) {
				continue
			}
			pt, payload, code, ok := scopeHMACInputs(row.RawHex)
			if !ok || code == "0000" || code == "FFFF" {
				continue
			}
			k := key{row.Hash, code, pt, sha256.Sum256(payload)}
			if value, found := cache[k]; found {
				row.ObservedScope = value
				continue
			}
			if stats.Checked >= scopeVerifyMaxWindowPackets {
				stats.Limited = true
				continue
			}
			codeBytes, err := hex.DecodeString(code)
			if err != nil || len(codeBytes) != 2 {
				continue
			}
			wireCode := uint16(codeBytes[0]) | uint16(codeBytes[1])<<8
			stats.Checked++
			match := ""
			ambiguous := false
			for j, name := range known {
				if j&31 == 0 {
					if err := ctx.Err(); err != nil {
						return stats, err
					}
				}
				macs[j].Reset()
				if regionCodeUsingMAC(macs[j], pt, payload) != wireCode {
					continue
				}
				if match != "" {
					ambiguous = true
					break
				}
				match = name
			}
			var value *CrossBorderScope
			if match != "" && !ambiguous {
				value = &CrossBorderScope{Kind: "named", Name: &match, Resolution: "catalog_match"}
				stats.Matched++
			}
			cache[k] = value
			row.ObservedScope = value
		}
	}
	return stats, ctx.Err()
}
