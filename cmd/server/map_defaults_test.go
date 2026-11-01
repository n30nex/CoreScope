package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCanadaverseMapDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name   string
		center []float64
		zoom   int
		want   MapConfigResponse
	}{
		{name: "Canada-wide fallback", want: MapConfigResponse{Center: []float64{56, -96}, Zoom: 3}},
		{name: "operator override", center: []float64{45, -75}, zoom: 11, want: MapConfigResponse{Center: []float64{45, -75}, Zoom: 11}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.MapDefaults.Center = tc.center
			cfg.MapDefaults.Zoom = tc.zoom
			srv := &Server{cfg: cfg}
			response := httptest.NewRecorder()
			srv.handleConfigMap(response, httptest.NewRequest(http.MethodGet, "/api/config/map", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			var got MapConfigResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("map defaults = %+v, want %+v", got, tc.want)
			}
		})
	}
}
