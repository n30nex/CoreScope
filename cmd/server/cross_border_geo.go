package main

import (
	_ "embed"
	"encoding/json"
	"math"
	"sync"
)

// The source, pinned revisions and licences are recorded beside these assets.
// Country polygons classify reported coordinates; the IBC line measures reach.
// Neither geometry describes a MeshMapper scope or measured RF coverage.
//
//go:embed cross_border_data/countries.geojson
var crossBorderCountriesJSON []byte

//go:embed cross_border_data/border.geojson
var crossBorderBoundaryJSON []byte

type crossBorderGeoPoint [2]float64
type crossBorderGeoBox struct{ west, south, east, north float64 }
type crossBorderGeoPolygon struct {
	country string
	rings   [][]crossBorderGeoPoint
	box     crossBorderGeoBox
}
type crossBorderGeoSegment struct {
	a, b crossBorderGeoPoint
	box  crossBorderGeoBox
}
type crossBorderGeography struct {
	countries []crossBorderGeoPolygon
	border    []crossBorderGeoSegment
	valid     bool
}
type crossBorderGeoCollection struct {
	Type     string `json:"type"`
	Features []struct {
		Properties struct {
			Country string `json:"country"`
		} `json:"properties"`
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

var crossBorderGeographyOnce sync.Once
var crossBorderGeographyData crossBorderGeography

func crossBorderGeo() *crossBorderGeography {
	crossBorderGeographyOnce.Do(func() {
		crossBorderGeographyData = parseCrossBorderGeography(crossBorderCountriesJSON, crossBorderBoundaryJSON)
	})
	return &crossBorderGeographyData
}

func parseCrossBorderGeography(countries, border []byte) crossBorderGeography {
	var result crossBorderGeography
	var c, b crossBorderGeoCollection
	if len(countries) > 20<<20 || len(border) > 5<<20 || json.Unmarshal(countries, &c) != nil || json.Unmarshal(border, &b) != nil || c.Type != "FeatureCollection" || b.Type != "FeatureCollection" {
		return result
	}
	for _, feature := range c.Features {
		country := feature.Properties.Country
		switch country {
		case "CAN":
			country = "CA"
		case "USA":
			country = "US"
		}
		if country != "CA" && country != "US" {
			continue
		}
		var polygons [][][]crossBorderGeoPoint
		switch feature.Geometry.Type {
		case "MultiPolygon":
			if json.Unmarshal(feature.Geometry.Coordinates, &polygons) != nil {
				return crossBorderGeography{}
			}
		case "Polygon":
			var rings [][]crossBorderGeoPoint
			if json.Unmarshal(feature.Geometry.Coordinates, &rings) != nil {
				return crossBorderGeography{}
			}
			polygons = append(polygons, rings)
		default:
			return crossBorderGeography{}
		}
		for _, rings := range polygons {
			if len(rings) == 0 || len(rings[0]) < 4 {
				return crossBorderGeography{}
			}
			box := crossBorderGeoBox{west: 180, south: 90, east: -180, north: -90}
			for _, ring := range rings {
				for _, p := range ring {
					if !crossBorderGeoValid(p[1], p[0]) {
						return crossBorderGeography{}
					}
				}
			}
			for _, p := range rings[0] {
				box.west = math.Min(box.west, p[0])
				box.east = math.Max(box.east, p[0])
				box.south = math.Min(box.south, p[1])
				box.north = math.Max(box.north, p[1])
			}
			result.countries = append(result.countries, crossBorderGeoPolygon{country, rings, box})
		}
	}
	for _, feature := range b.Features {
		var lines [][]crossBorderGeoPoint
		switch feature.Geometry.Type {
		case "MultiLineString":
			if json.Unmarshal(feature.Geometry.Coordinates, &lines) != nil {
				return crossBorderGeography{}
			}
		case "LineString":
			var line []crossBorderGeoPoint
			if json.Unmarshal(feature.Geometry.Coordinates, &line) != nil {
				return crossBorderGeography{}
			}
			lines = append(lines, line)
		default:
			return crossBorderGeography{}
		}
		for _, line := range lines {
			for i, p := range line {
				if !crossBorderGeoValid(p[1], p[0]) {
					return crossBorderGeography{}
				}
				if i == 0 {
					continue
				}
				a := line[i-1]
				result.border = append(result.border, crossBorderGeoSegment{a, p, crossBorderGeoBox{math.Min(a[0], p[0]), math.Min(a[1], p[1]), math.Max(a[0], p[0]), math.Max(a[1], p[1])}})
			}
		}
	}
	result.valid = len(result.countries) > 0 && len(result.border) > 0
	return result
}

func crossBorderGeoValid(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && !(lat == 0 && lon == 0)
}
func (b crossBorderGeoBox) contains(lat, lon float64) bool {
	return lon >= b.west && lon <= b.east && lat >= b.south && lat <= b.north
}
func crossBorderGeoRing(p crossBorderGeoPoint, ring []crossBorderGeoPoint) bool {
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		a, b := ring[i], ring[j]
		if (a[1] > p[1]) != (b[1] > p[1]) && p[0] < (b[0]-a[0])*(p[1]-a[1])/(b[1]-a[1])+a[0] {
			inside = !inside
		}
	}
	return inside
}
func (g *crossBorderGeography) country(location *CrossBorderLocation) string {
	if location == nil || !g.valid || !crossBorderGeoValid(location.Lat, location.Lon) {
		return ""
	}
	point := crossBorderGeoPoint{location.Lon, location.Lat}
	country := ""
	for _, p := range g.countries {
		if !p.box.contains(location.Lat, location.Lon) || !crossBorderGeoRing(point, p.rings[0]) {
			continue
		}
		inHole := false
		for _, hole := range p.rings[1:] {
			if crossBorderGeoRing(point, hole) {
				inHole = true
				break
			}
		}
		if inHole {
			continue
		}
		if country != "" && country != p.country {
			return ""
		}
		country = p.country
	}
	// Simplified country polygons and self-reported positions are not a survey.
	// Abstain within 250 m of the official border instead of choosing a side.
	if country != "" && g.distance(location) < 0.25 {
		return ""
	}
	return country
}

func crossBorderCountry(location *CrossBorderLocation) string {
	return crossBorderGeo().country(location)
}

// Shortest planar segment distance on a local equirectangular projection.
// The output is an approximate straight-line distance in kilometres, not
// cumulative route length. Coordinates near the border are excluded above.
func (g *crossBorderGeography) distance(p *CrossBorderLocation) float64 {
	if p == nil || !g.valid || !crossBorderGeoValid(p.Lat, p.Lon) {
		return math.Inf(1)
	}
	const latitudeKm = 111.1950802335329
	longitudeKm := latitudeKm * math.Cos(p.Lat*math.Pi/180)
	best := math.Inf(1)
	for _, s := range g.border {
		dx := math.Max(s.box.west-p.Lon, math.Max(0, p.Lon-s.box.east)) * longitudeKm
		dy := math.Max(s.box.south-p.Lat, math.Max(0, p.Lat-s.box.north)) * latitudeKm
		if dx*dx+dy*dy >= best {
			continue
		}
		ax, ay := (s.a[0]-p.Lon)*longitudeKm, (s.a[1]-p.Lat)*latitudeKm
		bx, by := (s.b[0]-p.Lon)*longitudeKm, (s.b[1]-p.Lat)*latitudeKm
		vx, vy := bx-ax, by-ay
		den := vx*vx + vy*vy
		t := 0.0
		if den > 0 {
			t = math.Max(0, math.Min(1, -(ax*vx+ay*vy)/den))
		}
		x, y := ax+t*vx, ay+t*vy
		best = math.Min(best, x*x+y*y)
	}
	return math.Sqrt(best)
}
func crossBorderDistanceToBorderKM(location *CrossBorderLocation) *float64 {
	g := crossBorderGeo()
	if location == nil || !g.valid || !crossBorderGeoValid(location.Lat, location.Lon) {
		return nil
	}
	d := g.distance(location)
	if math.IsInf(d, 0) || math.IsNaN(d) {
		return nil
	}
	return &d
}
