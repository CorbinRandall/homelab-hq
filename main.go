package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "time/tzdata"

	"github.com/google/uuid"
)

type Config struct {
	SiteName            string       `json:"site_name"`
	UnraidURL           string       `json:"unraid_url"`
	HeaderLinks         []HeaderLink `json:"header_links"`
	Listen              string       `json:"listen"`
	DataDir             string       `json:"data_dir"`
	StaticDir           string       `json:"static_dir"`
	Timezone            string       `json:"timezone"`
	UnraidIP            string       `json:"unraid_ip"`
	UnraidMAC           string       `json:"unraid_mac"`
	UnraidBroadcast     string       `json:"unraid_broadcast"`
	UnraidWOLPort       int          `json:"unraid_wol_port"`
	WOLCmd              string       `json:"wol_cmd"`
	UnraidProbeTimeout  int          `json:"unraid_probe_timeout_ms"`
	ShellyHost          string       `json:"shelly_host"`
	ShellyMAC           string       `json:"shelly_mac"`
	PrimaryHub          string       `json:"primary_hub"`
	ProxySleepToPrimary bool         `json:"proxy_sleep_to_primary"`
	SSHTarget           string       `json:"ssh_target"`
	SSHOpts             string       `json:"ssh_opts"`
	SleepCmd            string       `json:"sleep_cmd"`
	ShutdownCmd         string       `json:"shutdown_cmd"`
	ArrayStartCmd       string       `json:"array_start_cmd"`
	AppHostname         string       `json:"app_hostname"`
}

type HeaderLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Schedule struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Action    string `json:"action"`
	Enabled   bool   `json:"enabled"`
	Hour      int    `json:"hour"`
	Minute    int    `json:"minute"`
	Days      []int  `json:"days"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type SchedulesFile struct {
	Schedules []Schedule `json:"schedules"`
	Timezone  string     `json:"timezone"`
	DayLabels []string   `json:"day_labels"`
}

type Server struct {
	cfg             Config
	mu              sync.Mutex
	loc             *time.Location
	client          *http.Client
	lastFire        map[string]string // schedule id -> YYYY-MM-DD
	wakeMu          sync.Mutex        // serialize post-wake/array-start workflows
	arrayWorkflowMu sync.RWMutex
	arrayWorkflow   ArrayWorkflowStatus
	shellyMu        sync.Mutex
}

func main() {
	cfgPath := flag.String("config", "config.json", "path to config.json")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("timezone: %v", err)
	}

	s := &Server{
		cfg: cfg,
		loc: loc,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		lastFire: map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/unraid/status", s.handleUnraidStatus)
	mux.HandleFunc("/unraid/array-status", s.handleArrayStatus)
	mux.HandleFunc("/unraid/wake", s.handleUnraidWake)
	mux.HandleFunc("/unraid/sleep", s.handleUnraidSleep)
	mux.HandleFunc("/unraid/start", s.handleUnraidStart)
	mux.HandleFunc("/unraid/shutdown", s.handleUnraidShutdown)
	mux.HandleFunc("/plug/status", s.handlePlugStatus)
	mux.HandleFunc("/plug/on", s.handlePlugOn)
	mux.HandleFunc("/plug/off", s.handlePlugOff)
	mux.HandleFunc("/plug/cycle", s.handlePlugCycle)
	mux.HandleFunc("/api/wake-schedules", s.handleWakeSchedules)
	mux.HandleFunc("/api/wake-schedules/", s.handleWakeScheduleID)
	mux.HandleFunc("/api/refresh", s.handleRefresh)
	mux.HandleFunc("/api/config", s.handlePublicConfig)
	mux.HandleFunc("/hide", s.handleHide)
	mux.HandleFunc("/unhide", s.handleUnhide)
	mux.HandleFunc("/rename", s.handleRename)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/data/", http.StripPrefix("/data/", http.FileServer(http.Dir(cfg.DataDir))))
	mux.Handle("/", http.FileServer(http.Dir(cfg.StaticDir)))

	go s.scheduleLoop()

	log.Printf("homelab-hq listening on %s (unraid=%s shelly=%s primary=%t)", cfg.Listen, cfg.UnraidIP, cfg.ShellyHost, s.isPrimary())
	log.Fatal(http.ListenAndServe(cfg.Listen, mux))
}

func loadConfig(path string) (Config, error) {
	cfg := Config{
		SiteName:            "Homelab HQ",
		Listen:              ":8888",
		DataDir:             "data",
		StaticDir:           "www",
		Timezone:            "UTC",
		UnraidBroadcast:     "255.255.255.255",
		UnraidWOLPort:       9,
		UnraidProbeTimeout:  2500,
		PrimaryHub:          "",
		ProxySleepToPrimary: false,
		SSHOpts:             "-o BatchMode=yes -o ConnectTimeout=8",
		SleepCmd:            "",
		ArrayStartCmd:       "/usr/local/emhttp/plugins/dynamix/scripts/emcmd cmdStart=Start",
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.UnraidWOLPort == 0 {
		cfg.UnraidWOLPort = 9
	}
	if cfg.UnraidProbeTimeout == 0 {
		cfg.UnraidProbeTimeout = 2500
	}
	if strings.TrimSpace(cfg.SiteName) == "" {
		cfg.SiteName = "Homelab HQ"
	}
	return cfg, nil
}

func (s *Server) handlePublicConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"site_name":        s.cfg.SiteName,
		"unraid_url":       s.cfg.UnraidURL,
		"header_links":     s.cfg.HeaderLinks,
		"unraid_hostname":  s.cfg.AppHostname,
		"poll_interval_ms": 15000,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleUnraidStatus(w http.ResponseWriter, r *http.Request) {
	online := s.probeUnraid()
	writeJSON(w, http.StatusOK, map[string]any{"online": online})
}

func (s *Server) probeUnraid() bool {
	addr := net.JoinHostPort(s.cfg.UnraidIP, "80")
	conn, err := net.DialTimeout("tcp", addr, time.Duration(s.cfg.UnraidProbeTimeout)*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (s *Server) handleUnraidWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.sendWOL(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.afterWake("button")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) sendWOL() error {
	if cmd := strings.TrimSpace(s.cfg.WOLCmd); cmd != "" {
		out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
		if err != nil {
			return fmt.Errorf("wol_cmd failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	mac := strings.TrimSpace(s.cfg.UnraidMAC)
	if mac == "" {
		return fmt.Errorf("unraid_mac not set in config.json — add the Unraid NIC MAC")
	}
	hw, err := net.ParseMAC(normalizeMAC(mac))
	if err != nil {
		return fmt.Errorf("bad unraid_mac: %w", err)
	}
	var payload bytes.Buffer
	payload.Write([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	for i := 0; i < 16; i++ {
		payload.Write(hw)
	}
	bcast := s.cfg.UnraidBroadcast
	if bcast == "" {
		bcast = "255.255.255.255"
	}
	addr := &net.UDPAddr{IP: net.ParseIP(bcast), Port: s.cfg.UnraidWOLPort}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(payload.Bytes())
	return err
}

func normalizeMAC(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	if !strings.Contains(mac, ":") && len(mac) == 12 {
		parts := make([]string, 0, 6)
		for i := 0; i < 12; i += 2 {
			parts = append(parts, mac[i:i+2])
		}
		mac = strings.Join(parts, ":")
	}
	return mac
}

func (s *Server) isPrimary() bool {
	return strings.TrimSpace(s.cfg.PrimaryHub) == ""
}

func (s *Server) handleUnraidStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	go s.afterWake("start-array")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) handleUnraidShutdown(w http.ResponseWriter, r *http.Request) {
	s.handleUnraidPower(w, r, "shutdown")
}
func (s *Server) handleUnraidSleep(w http.ResponseWriter, r *http.Request) {
	s.handleUnraidPower(w, r, "sleep")
}
func (s *Server) handleUnraidPower(w http.ResponseWriter, r *http.Request, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.isPrimary() && s.cfg.ProxySleepToPrimary && s.cfg.PrimaryHub != "" {
		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(s.cfg.PrimaryHub, "/")+"/unraid/"+action, nil)
		if err == nil {
			resp, err := s.client.Do(req)
			if err == nil {
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				if len(body) == 0 {
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "via": "primary"})
				} else {
					_, _ = w.Write(body)
				}
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "sleep unavailable (primary hub unreachable); wake still works locally",
		})
		return
	}
	go s.afterPowerAction(action, "button")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action})
}

func (s *Server) shellyURL(path string) string {
	s.shellyMu.Lock()
	host := s.cfg.ShellyHost
	s.shellyMu.Unlock()
	return "http://" + host + path
}

func (s *Server) shellyHost() string {
	s.shellyMu.Lock()
	defer s.shellyMu.Unlock()
	return s.cfg.ShellyHost
}

func (s *Server) refreshShellyHost() string {
	mac := strings.ToLower(strings.TrimSpace(s.cfg.ShellyMAC))
	if mac == "" {
		return s.shellyHost()
	}

	var subnet net.IP
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, network, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() {
				ones, bits := network.Mask.Size()
				if bits == 32 && ones == 24 {
					subnet = ip.To4()
					break
				}
			}
		}
		if subnet != nil {
			break
		}
	}
	if subnet != nil {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 48)
		for i := 1; i < 255; i++ {
			ip := fmt.Sprintf("%d.%d.%d.%d", subnet[0], subnet[1], subnet[2], i)
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				conn, _ := net.DialTimeout("tcp", net.JoinHostPort(ip, "80"), 250*time.Millisecond)
				if conn != nil {
					_ = conn.Close()
				}
				<-sem
			}()
		}
		wg.Wait()
	}

	arp, _ := os.ReadFile("/proc/net/arp")
	for _, line := range strings.Split(string(arp), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && strings.ToLower(fields[3]) == mac {
			s.shellyMu.Lock()
			s.cfg.ShellyHost = fields[0]
			s.shellyMu.Unlock()
			return fields[0]
		}
	}
	return s.shellyHost()
}

func (s *Server) handlePlugStatus(w http.ResponseWriter, r *http.Request) {
	if s.shellyHost() == "" {
		s.refreshShellyHost()
	}
	if s.shellyHost() == "" {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	}
	info, err := s.shellyGet("/rpc/Shelly.GetDeviceInfo")
	status, err2 := s.shellyGet("/rpc/Switch.GetStatus?id=0")
	if err != nil || err2 != nil {
		s.refreshShellyHost()
		info, err = s.shellyGet("/rpc/Shelly.GetDeviceInfo")
		status, err2 = s.shellyGet("/rpc/Switch.GetStatus?id=0")
	}
	if err != nil || err2 != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": true,
			"error":      "unreachable",
			"host":       s.shellyHost(),
		})
		return
	}
	on, _ := status["output"].(bool)
	out := map[string]any{
		"configured": true,
		"on":         on,
		"host":       s.shellyHost(),
	}
	if m, ok := info["model"].(string); ok {
		out["model"] = m
	}
	if n, ok := info["id"].(string); ok {
		out["name"] = n
	}
	if ap, ok := status["apower"].(float64); ok {
		out["apower"] = ap
	}
	if v, ok := status["voltage"].(float64); ok {
		out["voltage"] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) shellyGet(path string) (map[string]any, error) {
	resp, err := s.client.Get(s.shellyURL(path))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) shellySet(on bool) error {
	u := fmt.Sprintf("/rpc/Switch.Set?id=0&on=%t", on)
	_, err := s.shellyGet(u)
	if err != nil {
		s.refreshShellyHost()
		_, err = s.shellyGet(u)
	}
	return err
}

func (s *Server) handlePlugOn(w http.ResponseWriter, r *http.Request) {
	s.plugAction(w, r, func() error { return s.shellySet(true) })
}

func (s *Server) handlePlugOff(w http.ResponseWriter, r *http.Request) {
	s.plugAction(w, r, func() error { return s.shellySet(false) })
}

func (s *Server) handlePlugCycle(w http.ResponseWriter, r *http.Request) {
	s.plugAction(w, r, func() error {
		if err := s.shellySet(false); err != nil {
			return err
		}
		time.Sleep(3 * time.Second)
		return s.shellySet(true)
	})
}

func (s *Server) plugAction(w http.ResponseWriter, r *http.Request, fn func() error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := fn(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) schedulesPath() string {
	return filepath.Join(s.cfg.DataDir, "schedules.json")
}

func (s *Server) loadSchedules() (SchedulesFile, error) {
	sf := SchedulesFile{
		Schedules: []Schedule{},
		Timezone:  s.cfg.Timezone,
		DayLabels: []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	}
	b, err := os.ReadFile(s.schedulesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return sf, nil
		}
		return sf, err
	}
	if err := json.Unmarshal(b, &sf); err != nil {
		return sf, err
	}
	if sf.Timezone == "" {
		sf.Timezone = s.cfg.Timezone
	}
	if len(sf.DayLabels) == 0 {
		sf.DayLabels = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	}
	return sf, nil
}

func (s *Server) saveSchedules(sf SchedulesFile) error {
	sf.Timezone = s.cfg.Timezone
	sf.DayLabels = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.schedulesPath(), b, 0o644)
}

func (s *Server) handleWakeSchedules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sf, err := s.loadSchedules()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, sf)
	case http.MethodPost:
		var payload Schedule
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
			return
		}
		now := time.Now().In(s.loc).Format(time.RFC3339Nano)
		payload.ID = uuid.NewString()
		payload.CreatedAt = now
		payload.UpdatedAt = now
		if payload.Days == nil {
			payload.Days = []int{}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		sf, err := s.loadSchedules()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		sf.Schedules = append(sf.Schedules, payload)
		if err := s.saveSchedules(sf); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "schedule": payload})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleWakeScheduleID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/wake-schedules/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sf, err := s.loadSchedules()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	idx := -1
	for i := range sf.Schedules {
		if sf.Schedules[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
			return
		}
		sc := &sf.Schedules[idx]
		if v, ok := patch["label"].(string); ok {
			sc.Label = v
		}
		if v, ok := patch["action"].(string); ok {
			sc.Action = v
		}
		if v, ok := patch["enabled"].(bool); ok {
			sc.Enabled = v
		}
		if v, ok := patch["hour"].(float64); ok {
			sc.Hour = int(v)
		}
		if v, ok := patch["minute"].(float64); ok {
			sc.Minute = int(v)
		}
		if v, ok := patch["days"].([]any); ok {
			days := make([]int, 0, len(v))
			for _, d := range v {
				if n, ok := d.(float64); ok {
					days = append(days, int(n))
				}
			}
			sc.Days = days
		}
		sc.UpdatedAt = time.Now().In(s.loc).Format(time.RFC3339Nano)
		if err := s.saveSchedules(sf); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "schedule": *sc})
	case http.MethodDelete:
		sf.Schedules = append(sf.Schedules[:idx], sf.Schedules[idx+1:]...)
		if err := s.saveSchedules(sf); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isPrimary() {
		if err := s.refreshDiscover(); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"action": "refresh", "ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"action": "refresh", "ok": true})
		return
	}
	if err := s.syncFromPrimary(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"action": "refresh", "ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": "refresh", "ok": true})
}

func (s *Server) syncFromPrimary() error {
	if s.cfg.PrimaryHub == "" {
		return fmt.Errorf("primary_hub not configured")
	}
	base := strings.TrimRight(s.cfg.PrimaryHub, "/")
	// Ask primary to refresh, then pull caches.
	req, _ := http.NewRequest(http.MethodPost, base+"/api/refresh", nil)
	resp, err := s.client.Do(req)
	if err == nil {
		resp.Body.Close()
		time.Sleep(2 * time.Second)
	}
	for _, name := range []string{"apps.json", "hidden.json"} {
		r, err := s.client.Get(base + "/data/" + name)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			return err
		}
		if r.StatusCode >= 400 {
			return fmt.Errorf("primary %s: %s", name, r.Status)
		}
		if err := os.WriteFile(filepath.Join(s.cfg.DataDir, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleHide(w http.ResponseWriter, r *http.Request) {
	s.mutateHidden(w, r, true)
}

func (s *Server) handleUnhide(w http.ResponseWriter, r *http.Request) {
	s.mutateHidden(w, r, false)
}

func (s *Server) mutateHidden(w http.ResponseWriter, r *http.Request, hide bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	container := r.FormValue("container")
	if container == "" {
		b, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(b))
		container = vals.Get("container")
	}
	if container == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "container required"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hidden, err := s.loadHidden()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if hide {
		name := container
		urlStr := ""
		if apps, err := s.loadApps(); err == nil {
			for _, a := range apps {
				if a.RawName == container {
					name = a.Name
					if len(a.URLs) > 0 {
						urlStr = a.URLs[0]
					}
					break
				}
			}
		}
		hidden[container] = map[string]any{"name": name, "url": urlStr}
	} else {
		delete(hidden, container)
	}
	if err := s.saveHidden(hidden); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type appEntry struct {
	RawName string   `json:"raw_name"`
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	URLs    []string `json:"urls"`
}

func (s *Server) loadApps() ([]appEntry, error) {
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "apps.json"))
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Apps []appEntry `json:"apps"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	return wrap.Apps, nil
}

func (s *Server) loadHidden() (map[string]any, error) {
	path := filepath.Join(s.cfg.DataDir, "hidden.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func (s *Server) saveHidden(m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.cfg.DataDir, "hidden.json"), b, 0o644)
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Container string `json:"container"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	if body.Container == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "container required"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.cfg.DataDir, "apps.json")
	b, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var wrap map[string]any
	if err := json.Unmarshal(b, &wrap); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	apps, _ := wrap["apps"].([]any)
	names := s.loadDisplayNames()
	if body.Name == "" {
		delete(names, body.Container)
	} else {
		names[body.Container] = body.Name
	}
	nameBytes, err := json.MarshalIndent(names, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := os.WriteFile(filepath.Join(s.cfg.DataDir, "display-names.json"), nameBytes, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	for i, raw := range apps {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["raw_name"] == body.Container {
			if body.Name == "" {
				m["name"] = autoNameFromRaw(body.Container)
			} else {
				m["name"] = body.Name
			}
			apps[i] = m
			break
		}
	}
	wrap["apps"] = apps
	out, err := json.MarshalIndent(wrap, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// also update hidden entry name if present
	hidden, _ := s.loadHidden()
	if h, ok := hidden[body.Container].(map[string]any); ok {
		if body.Name == "" {
			h["name"] = autoNameFromRaw(body.Container)
		} else {
			h["name"] = body.Name
		}
		hidden[body.Container] = h
		_ = s.saveHidden(hidden)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) scheduleLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		s.tickSchedules()
	}
}

func (s *Server) tickSchedules() {
	now := time.Now().In(s.loc)
	day := int(now.Weekday()) // Sun=0
	keyDay := now.Format("2006-01-02")

	s.mu.Lock()
	sf, err := s.loadSchedules()
	if err != nil {
		s.mu.Unlock()
		return
	}
	toFire := []Schedule{}
	for _, sc := range sf.Schedules {
		if !sc.Enabled {
			continue
		}
		if sc.Hour != now.Hour() || sc.Minute != now.Minute() {
			continue
		}
		match := false
		for _, d := range sc.Days {
			if d == day {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if s.lastFire[sc.ID] == keyDay {
			continue
		}
		toFire = append(toFire, sc)
	}
	s.mu.Unlock()

	if len(toFire) == 0 {
		return
	}
	for _, sc := range toFire {
		action := sc.Action
		if action == "" {
			action = "wake"
		}
		if action == "shutdown" || action == "sleep" {
			if !s.probeUnraid() {
				continue
			}
			var err error
			if action == "shutdown" {
				err = s.shutdownUnraid()
			} else {
				err = s.sleepUnraid()
			}
			if err != nil {
				log.Printf("schedule %s shutdown failed: %v", sc.ID, err)
				continue
			}
			log.Printf("schedule %s fired %s", sc.ID, action)
			s.refreshAfterPowerAction("schedule " + sc.ID + " " + action)
		} else {
			if s.probeUnraid() {
				continue
			}
			if err := s.sendWOL(); err != nil {
				log.Printf("schedule %s wake failed: %v", sc.ID, err)
				continue
			}
			log.Printf("schedule %s fired WOL", sc.ID)
			s.afterWake("schedule:" + sc.Label)
		}
		s.mu.Lock()
		s.lastFire[sc.ID] = keyDay
		s.mu.Unlock()
	}
}
