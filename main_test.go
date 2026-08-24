package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"unraid_ip":"192.0.2.10"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SiteName != "Homelab HQ" || cfg.Listen != ":8888" || cfg.StaticDir != "www" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestPublicConfigExcludesPrivateFields(t *testing.T) {
	s := &Server{cfg: Config{
		SiteName:    "My Lab",
		UnraidURL:   "http://unraid.example.test/Main",
		AppHostname: "unraid.example.test",
		SSHTarget:   "root@private-host",
		ShellyMAC:   "02:00:00:00:00:01",
	}}
	r := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	s.handlePublicConfig(w, r)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"ssh_target", "shelly_mac", "unraid_ip", "unraid_mac"} {
		if _, ok := body[private]; ok {
			t.Fatalf("public config exposed %s", private)
		}
	}
}
