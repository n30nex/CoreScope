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
	for _, palette := range []struct {
		name string
		got  map[string]string
		want map[string]string
	}{
		{"light", got.Theme, map[string]string{
			"background": "#f4f4f5", "surface1": "#ffffff", "surface2": "#f4f4f5",
			"accent": "#2563eb", "text": "#18181b", "textMuted": "#52525b", "border": "#d4d4d8",
		}},
		{"dark", got.ThemeDark, map[string]string{
			"background": "#09090b", "surface1": "#101012", "surface2": "#141418",
			"accent": "#3b82f6", "text": "#fafafa", "textMuted": "#a1a1aa", "border": "#27272a",
		}},
	} {
		for key, want := range palette.want {
			if palette.got[key] != want {
				t.Errorf("%s %s = %q, want %q", palette.name, key, palette.got[key], want)
			}
		}
	}
	if !strings.HasPrefix(got.Theme["font"], "Inter, system-ui") || got.ThemeDark["font"] != "" {
		t.Fatal("use a shared local font default so dark mode still inherits operator font overrides")
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
	if got.ThemeDark["surface1"] != "#101012" || got.Theme["text"] != "#18181b" {
		t.Fatal("partial overrides must retain the new default palette for untouched fields")
	}
}
