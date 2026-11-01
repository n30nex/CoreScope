package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type crossBorderMeshMapperTransport func(*http.Request) (*http.Response, error)

func (fn crossBorderMeshMapperTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func meshMapperTestResponse(status int, body, retry string) *http.Response {
	h := make(http.Header)
	if retry != "" {
		h.Set("Retry-After", retry)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCrossBorderMeshMapperOnlyConfiguredRegionalAPIs(t *testing.T) {
	for _, regions := range [][]string{{"https://other.invalid"}, {"ykf.meshmapper.net"}, {"../BUF"}, {"a"}, {"A", "B", "C", "D", "E", "F", "G", "H", "I"}} {
		if _, err := newCrossBorderMeshMapper(regions, "fixture-key"); err == nil {
			t.Errorf("accepted invalid configured regions %q", regions)
		}
	}
	p, err := newCrossBorderMeshMapper([]string{" ykf ", "YKF", "BUF"}, "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	p.client.Transport = crossBorderMeshMapperTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.RawQuery != "" || r.Header.Get("X-API-Key") != "fixture-key" {
			t.Errorf("unsafe request shape: scheme=%s query=%s", r.URL.Scheme, r.URL.RawQuery)
		}
		if r.URL.Host != "ykf.meshmapper.net" && r.URL.Host != "buf.meshmapper.net" {
			t.Errorf("unexpected host %s", r.URL.Host)
		}
		calls[r.URL.Host+r.URL.Path]++
		switch r.URL.Path {
		case "/get_repeaters.php":
			return meshMapperTestResponse(200, "[]", ""), nil
		case "/get_scopes.php":
			return meshMapperTestResponse(200, fmt.Sprintf(`{"generated_at":"2026-10-01T00:00:00Z","region":%q,"zones":[],"scopes":[]}`, strings.ToUpper(strings.Split(r.URL.Host, ".")[0])), ""), nil
		default:
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
			return nil, nil
		}
	})
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p.refresh(context.Background(), now)
	p.refresh(context.Background(), now.Add(time.Minute))
	if len(calls) != 4 {
		t.Fatalf("got %d endpoints, want two APIs per unique region", len(calls))
	}
	for endpoint, count := range calls {
		if count != 1 {
			t.Errorf("%s fetched %d times before refresh interval", endpoint, count)
		}
	}
	if p.Snapshot().Status != "ready" {
		t.Errorf("successful empty metadata should be ready: %+v", p.Snapshot())
	}
	if err := p.client.CheckRedirect(&http.Request{}, nil); err == nil {
		t.Fatal("redirects must not receive the integration key")
	}
}

func TestCrossBorderMeshMapperDedupAndEvidence(t *testing.T) {
	key := strings.Repeat("AB", 32)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, _ := newCrossBorderMeshMapper([]string{"YKF", "YYZ"}, "fixture-key")
	p.client.Transport = crossBorderMeshMapperTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/get_scopes.php" {
			return meshMapperTestResponse(200, fmt.Sprintf(`{"generated_at":"2026-10-01T00:00:00Z","region":%q,"zones":[],"scopes":[{"name":"zero-count","repeaters":0,"default":0,"monitored":true,"wardriving":false}]}`, strings.ToUpper(strings.Split(r.URL.Host, ".")[0])), ""), nil
		}
		body := fmt.Sprintf(`[{"hex_id":%q,"name":"Fixture repeater","lat":"43.4","lon":-80.4,"last_heard":"1790812000","enabled":1,"iata":"YKF","advert_bytes":"3","is_carpeater":0,"scopes_checked_at":1790811000,"default_scope":"ykf","default_scope_state":"named","default_scope_at":1790811000,"default_scope_src":"reported","scopes":[{"scope":"ykf","observed_at":1790810000,"observed_pkts":9,"proof":"floor","reported_at":1790810100,"uploaded_at":1790810200},{"scope":"*","reported_at":1790810100}]},{"hex_id":"ABAB","name":"Unresolved legacy prefix","enabled":1}]`, key)
		return meshMapperTestResponse(200, body, ""), nil
	})
	p.refresh(context.Background(), now)
	s := p.Snapshot()
	if len(s.Repeaters) != 1 {
		t.Fatalf("want one full-key repeater, got %+v", s.Repeaters)
	}
	r := s.Repeaters[0]
	if r.PublicKey != strings.ToLower(key) || len(r.SourceRegions) != 2 || r.Location == nil || r.Location.Lat != 43.4 {
		t.Errorf("identity, source dedup or numeric conversion lost: %+v", r)
	}
	if len(r.Scopes) != 2 || r.Scopes[1].Name != "ykf" || r.Scopes[1].Proof != "floor" || r.Scopes[1].ObservedPackets != 9 || r.Scopes[1].ObservedAt == nil || r.Scopes[1].ReportedAt == nil || r.Scopes[1].UploadedAt == nil {
		t.Errorf("must retain distinct scope provenance and wildcard: %+v", r.Scopes)
	}
	if r.DefaultScope == nil || *r.DefaultScope != "ykf" || r.DefaultScopeSource != "reported" || r.ScopesCheckedAt == nil {
		t.Errorf("default and queried-state evidence lost: %+v", r)
	}
	if len(s.Scopes) != 2 || s.Scopes[0].Name != "zero-count" || s.Scopes[0].Repeaters != 0 || !s.Scopes[0].Monitored {
		t.Errorf("zero-count monitored names must survive by source region: %+v", s.Scopes)
	}
	// Caller privacy filtering and presentation must not mutate the shared cache.
	s.Repeaters[0].SourceRegions[0] = "BAD"
	s.Repeaters[0].Location.Lat = 0
	*s.Repeaters[0].Scopes[1].ReportedAt = "mutated"
	*s.Repeaters[0].DefaultScope = "mutated"
	s.Repeaters[0].Scopes = nil
	s.Scopes[0].Name = "mutated"
	if next := p.Snapshot(); next.Repeaters[0].SourceRegions[0] == "BAD" || next.Repeaters[0].Location.Lat == 0 || *next.Repeaters[0].DefaultScope == "mutated" || next.Scopes[0].Name == "mutated" || *next.Repeaters[0].Scopes[1].ReportedAt == "mutated" {
		t.Fatal("Snapshot aliases mutable cached data")
	}
}

func TestCrossBorderMeshMapperFailurePreservesOrRevokes(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, _ := newCrossBorderMeshMapper([]string{"YKF"}, "fixture-key")
	phase := "ready"
	calls := 0
	p.client.Transport = crossBorderMeshMapperTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		switch phase {
		case "quota":
			return meshMapperTestResponse(503, `{"error":"key_rate_limited"}`, "7200"), nil
		case "denied":
			return meshMapperTestResponse(403, `{"error":"ip_not_allowed"}`, ""), nil
		}
		if r.URL.Path == "/get_repeaters.php" {
			return meshMapperTestResponse(200, fmt.Sprintf(`[{"hex_id":%q,"name":"Fixture","enabled":1,"scopes":[]}]`, strings.Repeat("AB", 32)), ""), nil
		}
		return meshMapperTestResponse(200, `{"region":"YKF","scopes":[{"name":"ykf","repeaters":1}]}`, ""), nil
	})
	p.refresh(context.Background(), now)
	initial := p.Snapshot()
	if len(initial.Repeaters) != 1 {
		t.Fatal("fixture did not load")
	}
	phase = "quota"
	p.refresh(context.Background(), now.Add(30*time.Minute))
	limited := p.Snapshot()
	if len(limited.Repeaters) != 1 || !limited.Partial || limited.Status != "rate_limited" {
		t.Errorf("quota failure discarded or falsely refreshed prior copy: %+v", limited)
	}
	before := calls
	p.refresh(context.Background(), now.Add(time.Hour))
	if calls != before {
		t.Fatal("retried before Retry-After")
	}
	phase = "denied"
	p.refresh(context.Background(), now.Add(3*time.Hour))
	denied := p.Snapshot()
	if len(denied.Repeaters) != 0 || len(denied.Scopes) != 0 || denied.Status != "access_denied" || denied.Revision <= initial.Revision || denied.Regions[0].ErrorCode != "ip_not_allowed" {
		t.Errorf("authorization change must purge metadata and advance cache revision: %+v", denied)
	}
}

func TestCrossBorderMeshMapperDisabledDoesNoWork(t *testing.T) {
	p, err := newCrossBorderMeshMapper([]string{"YKF"}, "")
	if err != nil {
		t.Fatal(err)
	}
	p.client.Transport = crossBorderMeshMapperTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("missing integration key must not produce anonymous requests")
		return nil, nil
	})
	p.refresh(context.Background(), time.Now())
	if p.Snapshot().Status != "disabled" {
		t.Fatal("missing key should leave metadata explicitly disabled")
	}
}

func TestCrossBorderMeshMapperRedirectAndMalformedBodies(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"redirect", "malformed", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			p, _ := newCrossBorderMeshMapper([]string{"YKF"}, "fixture-key")
			calls := 0
			p.client.Transport = crossBorderMeshMapperTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "ykf.meshmapper.net" {
					t.Fatal("integration key followed an external redirect")
				}
				if scenario == "redirect" {
					response := meshMapperTestResponse(302, "", "")
					response.Header.Set("Location", "https://elsewhere.invalid/private")
					return response, nil
				}
				if scenario == "oversize" {
					return meshMapperTestResponse(200, strings.Repeat(" ", meshMapperMaxBytes+1), ""), nil
				}
				return meshMapperTestResponse(200, `{"broken":`, ""), nil
			})
			p.refresh(context.Background(), now)
			result := p.Snapshot()
			if calls != 2 || result.Status != "unavailable" || len(result.Repeaters) != 0 || len(result.Scopes) != 0 {
				t.Errorf("unsafe response was accepted: calls=%d, snapshot=%+v", calls, result)
			}
		})
	}
}

func TestCrossBorderMeshMapperCappedEvidenceStaysPartialWhenStale(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, _ := newCrossBorderMeshMapper([]string{"YKF"}, "fixture-key")
	phase := "ready"
	parts := make([]string, meshMapperMaxRepeaterScopes+1)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"scope":"scope-%02d","reported_at":1790810100}`, i)
	}
	p.client.Transport = crossBorderMeshMapperTransport(func(r *http.Request) (*http.Response, error) {
		if phase == "failed" && r.URL.Path == "/get_repeaters.php" {
			return meshMapperTestResponse(503, `{"error":"unavailable"}`, ""), nil
		}
		if r.URL.Path == "/get_repeaters.php" {
			return meshMapperTestResponse(200, fmt.Sprintf(`[{"hex_id":%q,"enabled":1,"scopes":[%s]}]`, strings.Repeat("AB", 32), strings.Join(parts, ",")), ""), nil
		}
		return meshMapperTestResponse(200, `{"region":"YKF","scopes":[]}`, ""), nil
	})
	p.refresh(context.Background(), now)
	before := p.Snapshot()
	if !before.Partial || len(before.Repeaters) != 1 || len(before.Repeaters[0].Scopes) != meshMapperMaxRepeaterScopes {
		t.Fatalf("scope evidence was not bounded and marked partial: %+v", before)
	}
	phase = "failed"
	p.refresh(context.Background(), now.Add(30*time.Minute))
	after := p.Snapshot()
	if !after.Partial || len(after.Repeaters) != 1 || *after.Regions[0].RepeatersFetchedAt != *before.Regions[0].RepeatersFetchedAt {
		t.Fatal("failed refresh discarded the cached cap/freshness boundary")
	}
}

func TestCrossBorderMeshMapperRetryDateAndCancellation(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	p, _ := newCrossBorderMeshMapper([]string{"YKF", "BUF"}, "fixture-key")
	calls := 0
	p.client.Transport = crossBorderMeshMapperTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return meshMapperTestResponse(429, `{"error":"rate_limited"}`, now.Add(4*time.Hour).Format(http.TimeFormat)), nil
	})
	p.refresh(context.Background(), now)
	p.refresh(context.Background(), now.Add(3*time.Hour))
	if calls != 1 || p.Snapshot().Status != "rate_limited" {
		t.Fatal("rate-limited request did not stop the refresh or respect HTTP-date Retry-After")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Run(ctx)
	if calls != 1 {
		t.Fatal("cancelled provider made network requests")
	}
}
