package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The theme handler needs no database: branding must not affect data access.
func TestCanadaverseThemeDefaultsAndOverrides(t *testing.T) {
	t.Chdir(t.TempDir())
	srv := &Server{cfg: &Config{}}
	type themeResult struct {
		Branding  map[string]string `json:"branding"`
		Theme     map[string]string `json:"theme"`
		ThemeDark map[string]string `json:"themeDark"`
		Home      struct {
			HeroTitle string `json:"heroTitle"`
			Steps     []struct {
				Description string `json:"description"`
			} `json:"steps"`
		} `json:"home"`
	}
	read := func() themeResult {
		t.Helper()
		w := httptest.NewRecorder()
		srv.handleConfigTheme(w, httptest.NewRequest("GET", "/api/config/theme", nil))
		var got themeResult
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := read()
	if got.Branding["siteName"] != "Canadaverse CoreScope" || got.Home.HeroTitle != "Canadaverse CoreScope" {
		t.Fatalf("unexpected default identity: branding=%v homeTitle=%v", got.Branding, got.Home.HeroTitle)
	}
	if got.Theme["background"] != "#f3f8f5" || got.ThemeDark["background"] != "#020706" || got.ThemeDark["accent"] != "#18b7ff" {
		t.Fatalf("unexpected light/dark palette: light=%v dark=%v", got.Theme["background"], got.ThemeDark)
	}
	if len(got.Home.Steps) < 2 || !strings.Contains(got.Home.Steps[1].Description, "local mesh community") || strings.Contains(got.Home.Steps[1].Description, "910.525") {
		t.Fatal("onboarding must defer to the operator's local radio preset")
	}
	// Operator config still overrides defaults; theme.json still wins over config.
	srv.cfg.Branding = map[string]interface{}{"siteName": "Operator mesh", "logoUrl": "/operator.svg"}
	srv.cfg.Theme = map[string]interface{}{"accent": "#112233"}
	srv.cfg.ThemeDark = map[string]interface{}{"background": "#123456"}
	srv.cfg.Home = map[string]interface{}{"heroTitle": "Operator dashboard"}
	got = read()
	if got.Branding["siteName"] != "Operator mesh" || got.Branding["logoUrl"] != "/operator.svg" || got.Theme["accent"] != "#112233" || got.ThemeDark["background"] != "#123456" || got.Home.HeroTitle != "Operator dashboard" {
		t.Fatal("operator settings must override brand defaults")
	}
	if err := os.WriteFile("theme.json", []byte(`{"branding":{"siteName":"Theme mesh"},"theme":{"accent":"#223344"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got.Branding["siteName"] != "Theme mesh" || got.Theme["accent"] != "#223344" || got.ThemeDark["background"] != "#123456" {
		t.Fatal("theme.json must keep its existing precedence")
	}
}
