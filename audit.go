package main

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const powerAuditFilename = "power-audit.jsonl"

type PowerAuditEvent struct {
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
	Phase     string `json:"phase"`
	Result    string `json:"result"`
	Detail    string `json:"detail,omitempty"`
	Source    string `json:"source,omitempty"`
	SourceIP  string `json:"source_ip,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
	User      string `json:"user,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
}

func workflowResult(state string) string {
	switch state {
	case "succeeded":
		return "success"
	case "failed":
		return "failed"
	default:
		return "progress"
	}
}

func (s *Server) auditPath() string {
	return filepath.Join(s.cfg.DataDir, powerAuditFilename)
}

func (s *Server) appendAudit(event PowerAuditEvent) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().In(s.loc).Format(time.RFC3339)
	}
	b, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	f, err := os.OpenFile(s.auditPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

func requestSourceIP(r *http.Request) string {
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return strings.TrimSpace(strings.Split(value, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) auditRequest(r *http.Request, action, result, detail string) {
	user := strings.TrimSpace(r.Header.Get("Tailscale-User-Login"))
	if user == "" {
		user = strings.TrimSpace(r.Header.Get("Remote-User"))
	}
	s.appendAudit(PowerAuditEvent{
		Action: action, Phase: "request", Result: result, Detail: detail,
		Source: "http", SourceIP: requestSourceIP(r),
		UserAgent: r.UserAgent(), User: user,
	})
}

func (s *Server) handlePowerAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 80
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 {
		if value > 500 {
			value = 500
		}
		limit = value
	}

	s.auditMu.Lock()
	f, err := os.Open(s.auditPath())
	if err != nil {
		s.auditMu.Unlock()
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"events": []PowerAuditEvent{}})
			return
		}
		http.Error(w, "audit log unavailable", http.StatusInternalServerError)
		return
	}
	events := make([]PowerAuditEvent, 0, limit)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event PowerAuditEvent
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			events = append(events, event)
			if len(events) > limit {
				events = events[1:]
			}
		}
	}
	_ = f.Close()
	s.auditMu.Unlock()
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
