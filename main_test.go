package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	if cfg.SiteName != "Homelab HQ" || cfg.Listen != ":8888" || cfg.StaticDir != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestStatusCacheCollapsesConcurrentChecks(t *testing.T) {
	var calls atomic.Int32
	s := &Server{
		cfg: Config{StatusCacheSeconds: 30},
		probeOverride: func() bool {
			calls.Add(1)
			time.Sleep(10 * time.Millisecond)
			return true
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.probeUnraidCached(false) }()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe called %d times, want 1", got)
	}
}

func TestScheduleStateIsCachedAndWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	s := &Server{cfg: Config{DataDir: dir, Timezone: "UTC"}, lastFire: map[string]string{}}
	if err := s.initSchedules(); err != nil {
		t.Fatal(err)
	}
	sf := SchedulesFile{Schedules: []Schedule{{ID: "one", Label: "test", Days: []int{1}}}}
	if err := s.saveSchedules(sf); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.schedulesPath()); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadSchedules()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Schedules) != 1 || loaded.Schedules[0].ID != "one" {
		t.Fatalf("unexpected cached schedules: %+v", loaded)
	}
}

func TestNewIDAndSSHArgs(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || id[14] != '4' {
		t.Fatalf("unexpected UUID: %q", id)
	}
	args := sshArgs("-i /config/key -o BatchMode=yes", "root@server", "docker ps -q")
	want := []string{"-i", "/config/key", "-o", "BatchMode=yes", "root@server", "docker ps -q"}
	if len(args) != len(want) {
		t.Fatalf("args=%q", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args=%q", args)
		}
	}
	cmd := commandContext(context.Background(), "ssh -o BatchMode=yes root@example.test true")
	if filepath.Base(cmd.Path) != "ssh" {
		t.Fatalf("simple SSH command used shell: %q", cmd.Path)
	}
	shellCmd := commandContext(context.Background(), "ssh root@example.test 'echo one two'")
	if shellCmd.Path != "/bin/sh" {
		t.Fatalf("quoted command did not preserve shell parsing: %q", shellCmd.Path)
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

func TestPowerCommandFailureExplainsParitySafetyBlock(t *testing.T) {
	got := powerCommandFailure("shutdown", fmt.Errorf("REFUSE poweroff: array operation in progress (check P)"))
	want := "Shutdown blocked for safety: Unraid is running a parity check or another array operation. Let it finish, then try again."
	if got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}
