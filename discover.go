package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var skipNameRE = regexp.MustCompile(`(?i)(^docker-dashboard$|_db$|_redis$|_cron$|_postgres$|postgres$|redis$|mariadb|chromadb|collabora)`)

var projectNames = map[string]string{
	"unraid-mcp-hub":   "Unraid MCP",
	"docker-dashboard": "Homelab HQ",
	"timemachine":      "Time Machine",
}

type discoveredApp struct {
	Name    string   `json:"name"`
	RawName string   `json:"raw_name"`
	Status  string   `json:"status"`
	Image   string   `json:"image"`
	URL     string   `json:"url"`
	URLs    []string `json:"urls"`
}

func (s *Server) sshRun(remoteCmd string) (int, string, string, error) {
	target := s.cfg.SSHTarget
	if target == "" {
		target = "root@" + s.cfg.UnraidIP
	}
	opts := s.cfg.SSHOpts
	if opts == "" {
		opts = "-o BatchMode=yes -o ConnectTimeout=8"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := sshArgs(opts, target, remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return 1, "", string(out), fmt.Errorf("ssh command timed out: %w", ctx.Err())
	}
	combined := string(out)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), combined, combined, nil
		}
		return 1, "", combined, err
	}
	return 0, combined, "", nil
}

func sshArgs(opts, target, remoteCmd string) []string {
	args := append([]string(nil), strings.Fields(opts)...)
	return append(args, target, remoteCmd)
}

func (s *Server) refreshDiscover() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev, _ := s.loadAppsJSON()
	prevApps := prevAppsList(prev)

	if !s.probeUnraid() {
		apps := s.applyDisplayNames(s.mergeWithStatic(nil))
		return s.writeAppsJSON(apps, false, "", false, "")
	}

	discovered, err := s.discover()
	if err != nil {
		fallback := prevApps
		if len(fallback) == 0 {
			fallback = s.mergeWithStatic(nil)
		}
		stale := len(prevApps) > 0
		return s.writeAppsJSON(fallback, false, err.Error(), stale, prevUpdated(prev))
	}

	apps := s.applyDisplayNames(s.mergeWithStatic(discovered))
	return s.writeAppsJSON(apps, true, "", false, "")
}

func prevAppsList(prev map[string]any) []discoveredApp {
	raw, _ := prev["apps"].([]any)
	out := make([]discoveredApp, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		app := mapToApp(m)
		if app.RawName != "" {
			out = append(out, app)
		}
	}
	return out
}

func prevUpdated(prev map[string]any) string {
	if v, ok := prev["updated"].(string); ok {
		return v
	}
	return ""
}

func mapToApp(m map[string]any) discoveredApp {
	app := discoveredApp{Status: "running"}
	if v, ok := m["name"].(string); ok {
		app.Name = v
	}
	if v, ok := m["raw_name"].(string); ok {
		app.RawName = v
	}
	if v, ok := m["image"].(string); ok {
		app.Image = v
	}
	if v, ok := m["url"].(string); ok {
		app.URL = v
	}
	if urls, ok := m["urls"].([]any); ok {
		for _, u := range urls {
			if s, ok := u.(string); ok {
				app.URLs = append(app.URLs, s)
			}
		}
	}
	if app.URL == "" && len(app.URLs) > 0 {
		app.URL = app.URLs[0]
	}
	return app
}

func (s *Server) discover() ([]discoveredApp, error) {
	code, out, errOut, err := s.sshRun("docker ps -q")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		if msg == "" {
			msg = "docker ps failed"
		}
		return nil, fmt.Errorf("%s", msg)
	}

	ids := make([]string, 0)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids = append(ids, line)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	inspectCmd := "docker inspect " + strings.Join(ids, " ")
	code, out, errOut, err = s.sshRun(inspectCmd)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return nil, fmt.Errorf("%s", msg)
	}

	var containers []map[string]any
	if err := json.Unmarshal([]byte(out), &containers); err != nil {
		return nil, err
	}

	items := make([]discoveredApp, 0)
	for _, container := range containers {
		state, _ := container["State"].(map[string]any)
		if status, _ := state["Status"].(string); status != "running" {
			continue
		}
		name := strings.TrimPrefix(fmt.Sprint(container["Name"]), "/")
		if skipNameRE.MatchString(name) {
			continue
		}
		urls := urlsForContainer(container, s.cfg.UnraidIP)
		if s.cfg.AppHostname != "" {
			magicURLs := urlsForContainer(container, s.cfg.AppHostname)
			urls = append(magicURLs, urls...)
			seen := map[string]bool{}
			unique := urls[:0]
			for _, u := range urls {
				if !seen[u] {
					seen[u] = true
					unique = append(unique, u)
				}
			}
			urls = unique
		}
		if strings.Contains(strings.ToLower(name), "unraid-mcp") {
			for i := range urls {
				urls[i] = strings.TrimRight(urls[i], "/") + "/health"
			}
		}
		if len(urls) == 0 {
			continue
		}
		config, _ := container["Config"].(map[string]any)
		imageTag, _ := config["Image"].(string)
		image := imageTag
		if parts := strings.Split(imageTag, "/"); len(parts) > 0 {
			image = parts[len(parts)-1]
		}
		items = append(items, discoveredApp{
			Name:    friendlyName(container),
			RawName: name,
			Status:  "running",
			Image:   image,
			URL:     urls[0],
			URLs:    urls,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		a := strings.ToLower(items[i].Name + items[i].RawName)
		b := strings.ToLower(items[j].Name + items[j].RawName)
		return a < b
	})
	return items, nil
}

func friendlyName(container map[string]any) string {
	config, _ := container["Config"].(map[string]any)
	labels, _ := config["Labels"].(map[string]any)
	if project, _ := labels["com.docker.compose.project"].(string); project != "" {
		if friendly, ok := projectNames[project]; ok {
			return friendly
		}
		return titleWords(strings.NewReplacer("-", " ", "_", " ").Replace(project))
	}
	name := strings.TrimPrefix(fmt.Sprint(container["Name"]), "/")
	known := map[string]string{"Plex-Media-Server": "Plex"}
	if v, ok := known[name]; ok {
		return v
	}
	return titleWords(strings.NewReplacer("-", " ", "_", " ").Replace(name))
}

func titleWords(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, " ")
}

func autoNameFromRaw(raw string) string {
	known := map[string]string{
		"Plex-Media-Server":           "Plex",
		"unraid-mcp-hub-unraid-mcp-1": "Unraid MCP",
		"docker-dashboard":            "Homelab HQ",
		"timemachine":                 "Time Machine",
	}
	if name, ok := known[raw]; ok {
		return name
	}
	return titleWords(strings.NewReplacer("-", " ", "_", " ").Replace(raw))
}

func urlsForContainer(container map[string]any, hostIP string) []string {
	if urls := urlsFromPorts(container, hostIP); len(urls) > 0 {
		return urls
	}
	config, _ := container["Config"].(map[string]any)
	labels, _ := config["Labels"].(map[string]any)
	if webui := resolveUnraidWebUI(labels["net.unraid.docker.webui"], hostIP, ""); webui != "" {
		return []string{webui}
	}
	name := strings.ToLower(strings.TrimPrefix(fmt.Sprint(container["Name"]), "/"))
	if name == "plex-media-server" {
		return []string{fmt.Sprintf("http://%s:32400/web", hostIP)}
	}
	return urlsFromNetwork(container, config)
}

func urlsFromPorts(container map[string]any, hostIP string) []string {
	network, _ := container["NetworkSettings"].(map[string]any)
	ports, _ := network["Ports"].(map[string]any)
	urls := make([]string, 0)
	for _, bindingsAny := range ports {
		bindings, _ := bindingsAny.([]any)
		for _, bindingAny := range bindings {
			binding, _ := bindingAny.(map[string]any)
			hostPort, _ := binding["HostPort"].(string)
			if hostPort == "" {
				continue
			}
			hostBindIP, _ := binding["HostIp"].(string)
			if hostBindIP == "" || hostBindIP == "0.0.0.0" || hostBindIP == "::" {
				urls = append(urls, fmt.Sprintf("http://%s:%s", hostIP, hostPort))
			} else if strings.HasPrefix(hostBindIP, "192.168.") || strings.HasPrefix(hostBindIP, "100.") || strings.HasPrefix(hostBindIP, "10.") {
				urls = append(urls, fmt.Sprintf("http://%s:%s", hostBindIP, hostPort))
			}
		}
	}
	sort.Strings(urls)
	seen := map[string]bool{}
	unique := make([]string, 0, len(urls))
	for _, u := range urls {
		if !seen[u] {
			seen[u] = true
			unique = append(unique, u)
		}
	}
	return unique
}

func resolveUnraidWebUI(label any, hostIP, hostPort string) string {
	s, _ := label.(string)
	if s == "" {
		return ""
	}
	url := strings.ReplaceAll(s, "[IP]", hostIP)
	if hostPort != "" {
		re := regexp.MustCompile(`\[PORT:\d+\]`)
		url = re.ReplaceAllString(url, hostPort)
	} else {
		re := regexp.MustCompile(`\[PORT:(\d+)\]`)
		url = re.ReplaceAllString(url, "$1")
	}
	if strings.Contains(url, "[PORT:") || strings.Contains(url, "[IP]") {
		return ""
	}
	if !strings.HasPrefix(url, "http") {
		url = "http://" + strings.TrimLeft(url, "/")
	}
	return url
}

func urlsFromNetwork(container map[string]any, config map[string]any) []string {
	network, _ := container["NetworkSettings"].(map[string]any)
	networks, _ := network["Networks"].(map[string]any)
	name := strings.TrimPrefix(fmt.Sprint(container["Name"]), "/")
	envMap := map[string]string{}
	for _, line := range toStringSlice(config["Env"]) {
		if k, v, ok := strings.Cut(line, "="); ok {
			envMap[k] = v
		}
	}
	urls := make([]string, 0)
	for _, net := range networks {
		netMap, _ := net.(map[string]any)
		ip, _ := netMap["IPAddress"].(string)
		if ip == "" {
			continue
		}
		if name == "timemachine" {
			share := envMap["SHARE_NAME"]
			if share == "" {
				share = "TimeMachine"
			}
			urls = append(urls, fmt.Sprintf("smb://%s/%s", ip, share))
		} else if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "100.") || strings.HasPrefix(ip, "10.") {
			urls = append(urls, "http://"+ip)
		}
	}
	return urls
}

func toStringSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) loadStaticApps() []discoveredApp {
	path := filepath.Join(s.cfg.DataDir, "static-apps.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data []map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	out := make([]discoveredApp, 0, len(data))
	for _, item := range data {
		app := mapToApp(item)
		if app.RawName != "" {
			out = append(out, app)
		}
	}
	return out
}

func (s *Server) mergeWithStatic(discovered []discoveredApp) []discoveredApp {
	static := s.loadStaticApps()
	byName := map[string]discoveredApp{}
	for _, app := range discovered {
		byName[app.RawName] = app
	}
	for _, app := range static {
		byName[app.RawName] = app
	}
	out := make([]discoveredApp, 0, len(byName))
	for _, app := range byName {
		out = append(out, app)
	}
	sort.Slice(out, func(i, j int) bool {
		a := strings.ToLower(out[i].Name + out[i].RawName)
		b := strings.ToLower(out[j].Name + out[j].RawName)
		return a < b
	})
	return out
}

func (s *Server) loadDisplayNames() map[string]string {
	path := filepath.Join(s.cfg.DataDir, "display-names.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out[k] = s
		}
	}
	return out
}

func (s *Server) applyDisplayNames(apps []discoveredApp) []discoveredApp {
	names := s.loadDisplayNames()
	if len(names) == 0 {
		return apps
	}
	for i := range apps {
		if custom, ok := names[apps[i].RawName]; ok {
			apps[i].Name = custom
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		a := strings.ToLower(apps[i].Name + apps[i].RawName)
		b := strings.ToLower(apps[j].Name + apps[j].RawName)
		return a < b
	})
	return apps
}

func (s *Server) loadAppsJSON() (map[string]any, error) {
	path := filepath.Join(s.cfg.DataDir, "apps.json")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Server) writeAppsJSON(apps []discoveredApp, online bool, errMsg string, stale bool, updated string) error {
	if updated == "" {
		updated = time.Now().In(s.loc).Format(time.RFC3339Nano)
	}
	payload := map[string]any{
		"updated": updated,
		"online":  online,
		"apps":    apps,
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	if stale {
		payload["stale"] = true
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.cfg.DataDir, "apps.json")
	return writeFileAtomic(path, b, 0o644)
}

func (s *Server) sleepUnraid() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.sleepUnraidContext(ctx)
}

func (s *Server) sleepUnraidContext(ctx context.Context) error {
	cmd := s.cfg.SleepCmd
	if cmd == "" {
		target := s.cfg.SSHTarget
		if target == "" {
			target = "root@" + s.cfg.UnraidIP
		}
		opts := s.cfg.SSHOpts
		if opts == "" {
			opts = "-o BatchMode=yes -o ConnectTimeout=8"
		}
		cmd = fmt.Sprintf("ssh %s %s /usr/local/emhttp/plugins/dynamix.s3.sleep/scripts/s3_sleep -S", opts, target)
	}
	c := commandContext(ctx, cmd)
	out, err := c.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func (s *Server) shutdownUnraid() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return s.shutdownUnraidContext(ctx)
}

func (s *Server) shutdownUnraidContext(ctx context.Context) error {
	if strings.TrimSpace(s.cfg.ShutdownCmd) == "" {
		return s.sleepUnraidContext(ctx)
	}
	c := commandContext(ctx, s.cfg.ShutdownCmd)
	out, err := c.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// Execute simple SSH configuration directly so context cancellation also
// terminates the SSH client. Other legacy command strings retain shell syntax.
func commandContext(ctx context.Context, command string) *exec.Cmd {
	fields := strings.Fields(command)
	if len(fields) > 0 && fields[0] == "ssh" && !strings.ContainsAny(command, "'\"\\|&;<>()$`") {
		return exec.CommandContext(ctx, fields[0], fields[1:]...)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}
