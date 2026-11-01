package main

import (
	"math"
	"testing"
)

func TestCrossBorderGeographyUsesPolygonsRatherThanLatitude(t *testing.T) {
	for _, test := range []struct {
		name, country string
		lat, lon      float64
	}{
		{"Waterloo", "CA", 43.46, -80.52}, {"Toronto", "CA", 43.65, -79.38}, {"Hamilton", "CA", 43.25, -79.87},
		{"St Catharines", "CA", 43.16, -79.24}, {"Buffalo", "US", 42.88, -78.88}, {"Rochester", "US", 43.15, -77.61}, {"Syracuse", "US", 43.05, -76.15},
		{"Windsor south of Detroit", "CA", 42.30, -83.03}, {"Detroit north of Windsor", "US", 42.35, -83.05},
		{"Vancouver", "CA", 49.28, -123.12}, {"Bellingham", "US", 48.75, -122.48}, {"Fredericton", "CA", 45.96, -66.64}, {"Bangor", "US", 44.80, -68.77},
		{"Mexico", "", 25.69, -100.32}, {"Atlantic", "", 35, -40}, {"No GPS", "", 0, 0}, {"Bad latitude", "", 100, -80},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := crossBorderCountry(&CrossBorderLocation{Lat: test.lat, Lon: test.lon}); got != test.country {
				t.Fatalf("country=%q, want %q", got, test.country)
			}
		})
	}
	if crossBorderCountry(nil) != "" || crossBorderCountry(&CrossBorderLocation{Lat: math.NaN(), Lon: -80}) != "" {
		t.Fatal("invalid location assigned a country")
	}
}

func TestCrossBorderGeographyDistanceAndBorderAbstention(t *testing.T) {
	g := crossBorderGeo()
	if !g.valid || len(g.border) < 100 || len(g.countries) < 2 {
		t.Fatal("embedded geographic evidence did not load")
	}
	p := g.border[0].a
	boundary := &CrossBorderLocation{Lat: p[1], Lon: p[0]}
	if crossBorderCountry(boundary) != "" {
		t.Fatal("point on border assigned to a country")
	}
	if d := crossBorderDistanceToBorderKM(boundary); d == nil || *d > 0.001 {
		t.Fatalf("point on boundary distance=%v", d)
	}
	d := crossBorderDistanceToBorderKM(&CrossBorderLocation{Lat: 43.65, Lon: -79.38})
	if d == nil || *d < 20 || *d > 150 {
		t.Fatalf("Toronto border-distance outside broad geographic bounds: %v", d)
	}
	if crossBorderDistanceToBorderKM(nil) != nil {
		t.Fatal("missing position became measured distance")
	}
}

func TestCrossBorderGeographyHolesOverlapAndMalformedInput(t *testing.T) {
	countries := []byte(`{"type":"FeatureCollection","features":[{"properties":{"country":"CA"},"geometry":{"type":"Polygon","coordinates":[[[10,10],[20,10],[20,20],[10,20],[10,10]],[[12,12],[14,12],[14,14],[12,14],[12,12]]]}},{"properties":{"country":"US"},"geometry":{"type":"Polygon","coordinates":[[[18,18],[25,18],[25,25],[18,25],[18,18]]]}}]}`)
	border := []byte(`{"type":"FeatureCollection","features":[{"geometry":{"type":"LineString","coordinates":[[10,30],[25,30]]}}]}`)
	g := parseCrossBorderGeography(countries, border)
	if !g.valid || g.country(&CrossBorderLocation{Lat: 11, Lon: 11}) != "CA" || g.country(&CrossBorderLocation{Lat: 13, Lon: 13}) != "" || g.country(&CrossBorderLocation{Lat: 19, Lon: 19}) != "" {
		t.Fatal("hole or overlapping countries assigned incorrectly")
	}
	bad := parseCrossBorderGeography([]byte(`{`), border)
	if bad.valid || bad.country(&CrossBorderLocation{Lat: 11, Lon: 11}) != "" {
		t.Fatal("malformed geometry failed open")
	}
}
