package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"
)

type ArrayWorkflowStatus struct {
	State       string `json:"state"`
	Message     string `json:"message"`
	Reason      string `json:"reason,omitempty"`
	Attempts    int    `json:"attempts"`
	StartedAt   string `json:"started_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

func (s *Server) setArrayWorkflow(state, message, reason string, attempts int, complete bool) {
	now := time.Now().Format(time.RFC3339)
	s.arrayWorkflowMu.Lock()
	defer s.arrayWorkflowMu.Unlock()
	if s.arrayWorkflow.StartedAt == "" || s.arrayWorkflow.State == "succeeded" || s.arrayWorkflow.State == "failed" || s.arrayWorkflow.State == "idle" {
		s.arrayWorkflow.StartedAt = now
	}
	s.arrayWorkflow.State = state
	s.arrayWorkflow.Message = message
	s.arrayWorkflow.Reason = reason
	s.arrayWorkflow.Attempts = attempts
	s.arrayWorkflow.UpdatedAt = now
	if complete {
		s.arrayWorkflow.CompletedAt = now
	} else {
		s.arrayWorkflow.CompletedAt = ""
	}
}

func (s *Server) handleArrayStatus(w http.ResponseWriter, r *http.Request) {
	s.arrayWorkflowMu.RLock()
	workflow := s.arrayWorkflow
	s.arrayWorkflowMu.RUnlock()
	if workflow.State == "" {
		workflow.State = "idle"
		workflow.Message = "No dashboard array-start attempt is active"
	}
	online := s.probeUnraidCached(false)
	arrayState := "Unavailable"
	if online {
		arrayState = s.cachedArrayState()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"online": online, "array_state": arrayState, "workflow": workflow,
	})
}

func (s *Server) cachedArrayState() string {
	s.arrayCacheMu.Lock()
	defer s.arrayCacheMu.Unlock()
	if !s.arrayCachedAt.IsZero() && time.Since(s.arrayCachedAt) < time.Duration(s.cfg.ArrayCacheSeconds)*time.Second {
		return s.arrayCached
	}
	state := "Services not ready"
	if value, err := s.fsState(); err == nil && value != "" {
		state = value
	}
	s.arrayCached = state
	s.arrayCachedAt = time.Now()
	return state
}

func (s *Server) invalidateStatusCaches() {
	s.statusMu.Lock()
	s.statusCachedAt = time.Time{}
	s.statusMu.Unlock()
	s.arrayCacheMu.Lock()
	s.arrayCachedAt = time.Time{}
	s.arrayCacheMu.Unlock()
}

const (
	postWakeOnlineTimeout = 8 * time.Minute
	postWakePollInterval  = 10 * time.Second
	postWakeEmhttpDelay   = 20 * time.Second
	postWakeArrayTimeout  = 8 * time.Minute
	postWakeStartRetry    = 20 * time.Second
	postWakeRefreshDelay  = 40 * time.Second
	powerOffTimeout       = 5 * time.Minute
	powerOffPollInterval  = 5 * time.Second
)

// afterPowerAction makes sleep and shutdown observable in the same workflow
// endpoint used by wake/array start. A successful button response only means
// the request was queued; this workflow confirms that the server actually
// stops answering before reporting success.
func (s *Server) afterPowerAction(action, reason string) {
	if !s.wakeMu.TryLock() {
		log.Printf("%s (%s): another power workflow is already active", action, reason)
		return
	}
	defer s.wakeMu.Unlock()

	workflowReason := action + ":" + reason
	s.setArrayWorkflow("sending_"+action, "Sending the "+action+" command to Unraid", workflowReason, 1, false)
	// SSH commonly remains attached while Unraid finishes stopping services.
	// Run command delivery concurrently so progress is driven by the server's
	// actual availability rather than by how quickly SSH notices the disconnect.
	commandDone := make(chan error, 1)
	commandCtx, cancelCommand := context.WithCancel(context.Background())
	defer cancelCommand()
	go func() {
		if action == "shutdown" {
			commandDone <- s.shutdownUnraidContext(commandCtx)
			return
		}
		commandDone <- s.sleepUnraidContext(commandCtx)
	}()
	commandWait := time.NewTimer(10 * time.Second)
	commandPoll := time.NewTicker(time.Second)
	commandFinished := false
waitForCommand:
	for {
		select {
		case err := <-commandDone:
			commandFinished = true
			if err != nil && s.probeUnraid() {
				s.setArrayWorkflow("failed", powerCommandFailure(action, err), workflowReason, 1, true)
				log.Printf("%s command failed: %v", action, err)
				commandWait.Stop()
				commandPoll.Stop()
				return
			}
			break waitForCommand
		case <-commandPoll.C:
			if !s.probeUnraid() {
				s.setArrayWorkflow("succeeded", "Server is offline; "+action+" completed", workflowReason, 1, true)
				s.refreshAfterPowerAction(action)
				commandWait.Stop()
				commandPoll.Stop()
				return
			}
		case <-commandWait.C:
			break waitForCommand
		}
	}
	commandPoll.Stop()
	commandWait.Stop()
	if !commandFinished {
		log.Printf("%s command is still attached; tracking server availability independently", action)
	}

	s.setArrayWorkflow("waiting_for_power_off", "Command accepted; waiting for the server to go offline", workflowReason, 1, false)
	deadline := time.Now().Add(powerOffTimeout)
	polls := 0
	for time.Now().Before(deadline) {
		if !s.probeUnraid() {
			s.setArrayWorkflow("succeeded", "Server is offline; "+action+" completed", workflowReason, polls+1, true)
			s.refreshAfterPowerAction(action)
			return
		}
		polls++
		s.setArrayWorkflow("waiting_for_power_off", "Unraid is still online; waiting for "+action+" to finish", workflowReason, polls, false)
		time.Sleep(powerOffPollInterval)
	}
	s.setArrayWorkflow("failed", "Server was still online after the 5-minute "+action+" timeout", workflowReason, polls, true)
}

func powerCommandFailure(action string, err error) string {
	detail := strings.TrimSpace(err.Error())
	lower := strings.ToLower(detail)
	if action == "shutdown" && strings.Contains(lower, "refuse poweroff") &&
		(strings.Contains(lower, "array operation") || strings.Contains(lower, "parity") || strings.Contains(lower, "check p")) {
		return "Shutdown blocked for safety: Unraid is running a parity check or another array operation. Let it finish, then try again."
	}
	return "The " + action + " command failed: " + detail
}

func (s *Server) refreshAfterPowerAction(reason string) {
	go func() {
		log.Printf("%s: waiting %s before app refresh", reason, postWakeRefreshDelay)
		time.Sleep(postWakeRefreshDelay)
		if err := s.refreshDiscover(); err != nil {
			log.Printf("%s: refresh: %v", reason, err)
		} else {
			log.Printf("%s: app refresh complete", reason)
		}
	}()
}

func (s *Server) arrayStartCmd() string {
	cmd := strings.TrimSpace(s.cfg.ArrayStartCmd)
	if cmd == "" {
		return "/usr/local/emhttp/plugins/dynamix/scripts/emcmd cmdStart=Start"
	}
	return cmd
}

func (s *Server) fsState() (string, error) {
	code, out, errOut, err := s.sshRun("grep -m1 '^fsState=' /var/local/emhttp/var.ini | cut -d'\"' -f2")
	if err != nil {
		return "", err
	}
	if code != 0 {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return "", &fsStateError{msg: msg, code: code}
	}
	return strings.TrimSpace(out), nil
}

type fsStateError struct {
	msg  string
	code int
}

func (e *fsStateError) Error() string { return e.msg }

func (s *Server) waitForUnraidOnline(reason string) bool {
	deadline := time.Now().Add(postWakeOnlineTimeout)
	polls := 0
	for time.Now().Before(deadline) {
		if s.probeUnraid() {
			s.setArrayWorkflow("waiting_for_services", "Server is online; waiting for Unraid services", reason, polls, false)
			return true
		}
		polls++
		s.setArrayWorkflow("waiting_for_server", "Waiting for the server to answer; Wake-on-LAN will retry", reason, polls, false)
		// A packet sent immediately after SSH drops can arrive while the
		// motherboard is still completing shutdown. Retry every 30 seconds
		// until the host answers so Turn On remains a single-click action.
		if polls%3 == 0 {
			if err := s.sendWOL(); err != nil {
				log.Printf("post-wake: WoL retry failed: %v", err)
			}
		}
		time.Sleep(postWakePollInterval)
	}
	return false
}

// ensureArrayStarted tolerates emhttp and SSH coming up in stages. It keeps
// checking state and retries a rejected start command until the array is
// actually Started or the deadline expires.
func (s *Server) ensureArrayStarted(reason string) (startedByWorkflow bool, ok bool) {
	deadline := time.Now().Add(postWakeArrayTimeout)
	lastStartAttempt := time.Time{}
	for time.Now().Before(deadline) {
		fs, err := s.fsState()
		if err != nil {
			s.setArrayWorkflow("waiting_for_services", "Unraid is online, but array services are not ready yet", reason, 0, false)
			log.Printf("post-wake (%s): fsState not ready: %v", reason, err)
			time.Sleep(postWakePollInterval)
			continue
		}
		if fs == "Started" {
			return startedByWorkflow, true
		}
		if fs == "Starting" {
			s.setArrayWorkflow("array_starting", "Unraid reports that the array is starting", reason, 0, false)
			log.Printf("post-wake (%s): array is Starting", reason)
			time.Sleep(postWakePollInterval)
			continue
		}
		if lastStartAttempt.IsZero() || time.Since(lastStartAttempt) >= postWakeStartRetry {
			startedByWorkflow = true
			lastStartAttempt = time.Now()
			attempts := 1
			s.arrayWorkflowMu.RLock()
			if s.arrayWorkflow.Reason == reason {
				attempts = s.arrayWorkflow.Attempts + 1
			}
			s.arrayWorkflowMu.RUnlock()
			s.setArrayWorkflow("sending_start", "Sending the array start command", reason, attempts, false)
			log.Printf("post-wake (%s): array fsState=%q — issuing start", reason, fs)
			code, out, errOut, runErr := s.sshRun(s.arrayStartCmd())
			if runErr != nil {
				s.setArrayWorkflow("retrying", "Start command could not be delivered; retrying", reason, attempts, false)
				log.Printf("post-wake (%s): array start ssh error (will retry): %v", reason, runErr)
			} else if code != 0 {
				msg := strings.TrimSpace(errOut)
				if msg == "" {
					msg = strings.TrimSpace(out)
				}
				s.setArrayWorkflow("retrying", "Unraid rejected the start command; retrying", reason, attempts, false)
				log.Printf("post-wake (%s): array start rejected (code %d, will retry): %s", reason, code, msg)
			} else {
				s.setArrayWorkflow("command_accepted", "Start command accepted; waiting for the array", reason, attempts, false)
				log.Printf("post-wake (%s): array start accepted", reason)
			}
		}
		time.Sleep(postWakePollInterval)
	}
	return startedByWorkflow, false
}

// afterWake waits for Unraid to boot after WoL, then starts the array if stopped.
// Used by the dashboard Wake button and scheduled wakes.
func (s *Server) afterWake(reason string) {
	go func() {
		if !s.wakeMu.TryLock() {
			log.Printf("post-wake (%s): workflow already active", reason)
			return
		}
		defer s.wakeMu.Unlock()
		s.setArrayWorkflow("waiting_for_server", "Starting array workflow", reason, 0, false)

		if !s.waitForUnraidOnline(reason) {
			s.setArrayWorkflow("failed", "Server did not come online before the 8-minute timeout", reason, 0, true)
			log.Printf("post-wake (%s): unraid never came online", reason)
			return
		}
		time.Sleep(postWakeEmhttpDelay)

		startedByWorkflow, started := s.ensureArrayStarted(reason)
		if !started {
			s.setArrayWorkflow("failed", "Array did not reach Started before the 8-minute timeout", reason, 0, true)
			log.Printf("post-wake (%s): array did not reach Started within %s", reason, postWakeArrayTimeout)
			return
		}
		if !startedByWorkflow {
			s.setArrayWorkflow("succeeded", "Array is started (it was already starting or started)", reason, 0, true)
			log.Printf("post-wake (%s): array already Started", reason)
			if err := s.refreshDiscover(); err != nil {
				log.Printf("post-wake (%s): refresh: %v", reason, err)
			}
			return
		}

		log.Printf("post-wake (%s): array Started", reason)
		s.setArrayWorkflow("refreshing_apps", "Array started; refreshing apps in 40 seconds", reason, 0, false)
		log.Printf("post-wake (%s): waiting %s before app refresh", reason, postWakeRefreshDelay)
		time.Sleep(postWakeRefreshDelay)
		if err := s.refreshDiscover(); err != nil {
			log.Printf("post-wake (%s): refresh: %v", reason, err)
		} else {
			log.Printf("post-wake (%s): app refresh complete", reason)
		}
		s.setArrayWorkflow("succeeded", "Array started and apps refreshed", reason, 0, true)
	}()
}
