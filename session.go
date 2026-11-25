package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type windowSizeEntry struct {
	width   int
	height  int
	expires time.Time
}

func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	udid, err := requestUDID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.mu.RLock()
	existingSession := s.sessions[udid]
	wasWDA := s.wdaSessions[udid]
	s.mu.RUnlock()
	if existingSession != "" {
		probeCtx, cancel := context.WithTimeout(r.Context(), runtimeCfg.ProbeTimeout)
		probeErr := s.probeSession(probeCtx, udid, existingSession)
		cancel()
		if probeErr == nil {
			writeJSON(w, http.StatusOK, map[string]string{"sessionId": existingSession, "sessionType": sessionType(wasWDA)})
			return
		}
		logDeviceEvent(r.Context(), "warn", udid, "session_probe_failed_recreating", probeErr)
		s.invalidateSession(udid)
		if wasWDA {
			if sessionID, wdaURL, wdaErr := s.adoptRunningWDA(r.Context(), udid); wdaErr == nil {
				s.setSession(udid, sessionID, wdaURL, true)
				writeJSON(w, http.StatusOK, map[string]string{"sessionId": sessionID, "sessionType": "WDA"})
				return
			} else {
				logDeviceEvent(r.Context(), "warn", udid, "wda_session_recreate_failed", wdaErr)
			}
		}
	}

	baseURL := s.appiumURL(udid)
	body := map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": map[string]any{
				"platformName":              "iOS",
				"appium:automationName":     "XCUITest",
				"appium:udid":               udid,
				"appium:deviceName":         "iPhone",
				"appium:noReset":            true,
				"appium:autoAcceptAlerts":   true,
				"appium:newCommandTimeout":  2000,
				"appium:waitForIdleTimeout": 100,
				"appium:showXcodeLog":       false,
				"appium:useNewWDA":          true,
			},
			"firstMatch": []any{map[string]any{}},
		},
	}
	result, err := s.appiumRequest(r.Context(), baseURL, http.MethodPost, "/session", body)
	if err != nil {
		sessionID, wdaURL, wdaErr := s.adoptRunningWDA(r.Context(), udid)
		if wdaErr != nil {
			s.writeAPIError(w, http.StatusBadGateway, fmt.Errorf("Appium 不可用（%v）；WDA 直连也失败：%w", err, wdaErr))
			return
		}
		s.setSession(udid, sessionID, wdaURL, true)
		writeJSON(w, http.StatusOK, map[string]string{"sessionId": sessionID, "sessionType": "WDA"})
		return
	}
	sessionID := sessionIDFrom(result)
	if sessionID == "" {
		writeError(w, http.StatusBadGateway, errors.New("Appium 未返回 sessionId，请检查 Appium/XCUITest 日志"))
		return
	}
	s.setSession(udid, sessionID, baseURL, false)
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": sessionID, "sessionType": "Appium"})
}

func (s *server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	udid, err := requestUDID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID, err := s.sessionID(udid)
	if err != nil {
		s.writeAPIError(w, http.StatusConflict, err)
		return
	}
	baseURL := s.sessionURL(udid)
	wda := s.isWDASession(udid)
	s.invalidateSession(udid)

	var warning string
	if !wda {
		if _, requestErr := s.appiumRequest(r.Context(), baseURL, http.MethodDelete, sessionPath(sessionID), nil); requestErr != nil {
			warning = "本地会话已清理，但 Appium 端删除失败：" + requestErr.Error()
			logDeviceEvent(r.Context(), "warn", udid, "appium_session_delete_failed", requestErr)
		}
	}
	response := map[string]any{"ok": true}
	if warning != "" {
		response["warning"] = warning
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *server) sessionID(udid string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.sessions[udid]
	if id == "" {
		return "", errSessionRequired
	}
	return id, nil
}

func (s *server) setSession(udid, sessionID, baseURL string, wda bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]string)
	}
	if s.sessionURLs == nil {
		s.sessionURLs = make(map[string]string)
	}
	if s.wdaSessions == nil {
		s.wdaSessions = make(map[string]bool)
	}
	s.sessions[udid] = sessionID
	s.sessionURLs[udid] = baseURL
	s.wdaSessions[udid] = wda
	delete(s.windowSizes, udid)
}

func (s *server) invalidateSession(udid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, udid)
	delete(s.sessionURLs, udid)
	delete(s.wdaSessions, udid)
	delete(s.windowSizes, udid)
}

func (s *server) clearWindowSize(udid string) {
	s.mu.Lock()
	delete(s.windowSizes, udid)
	s.mu.Unlock()
}

// probeSession verifies that a cached session is still alive before reusing it.
// A stale session is worse than no session: every subsequent request fails with 502.
func (s *server) probeSession(ctx context.Context, udid, sessionID string) error {
	if s.isWDASession(udid) {
		result, err := s.appiumRequest(ctx, s.sessionURL(udid), http.MethodGet, "/status", nil)
		if err != nil {
			return err
		}
		value, _ := result["value"].(map[string]any)
		ready, _ := value["ready"].(bool)
		if !ready {
			return errSessionLost
		}
		// Only a non-empty, different session id proves the cached session is
		// gone. Most WDA builds omit sessionId from /status entirely, and
		// treating that as "lost" would rebuild the session on every request.
		if current := sessionIDFrom(result); current != "" && current != sessionID {
			return errSessionLost
		}
		_, err = s.appiumRequest(ctx, s.sessionURL(udid), http.MethodGet, sessionPath(sessionID)+"/window/size", nil)
		if err != nil && isSessionInvalid(err) {
			return errSessionLost
		}
		return err
	}
	_, err := s.appiumRequest(ctx, s.sessionURL(udid), http.MethodGet, sessionPath(sessionID), nil)
	return err
}

// sessionRequest wraps appiumRequest so any invalid-session response clears the
// local cache immediately instead of leaving the UI stuck on a dead session.
func (s *server) sessionRequest(ctx context.Context, udid, sessionID, method, endpoint string, body any) (map[string]any, error) {
	result, err := s.appiumRequest(ctx, s.sessionURL(udid), method, endpoint, body)
	if err != nil && isSessionInvalid(err) {
		s.invalidateSession(udid)
		return nil, errSessionLost
	}
	return result, err
}

func isSessionInvalid(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"invalid session id",
		"session is either terminated",
		"a session is either terminated",
		"no such session",
		"session not found",
		"session does not exist",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// reconcileDevices drops sessions and relays for devices that are no longer
// present, and invalidates relays whose usbmux DeviceID changed after a replug.
func (s *server) reconcileDevices(found []muxDevice) {
	present := make(map[string]bool, len(found))
	deviceIDs := make(map[string]uint32, len(found))
	for _, device := range found {
		present[device.UDID] = true
		deviceIDs[device.UDID] = device.DeviceID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.deviceIDs == nil {
		s.deviceIDs = make(map[string]uint32)
	}
	for udid, oldID := range s.deviceIDs {
		newID, stillPresent := deviceIDs[udid]
		if !stillPresent || newID != oldID {
			s.closeRelayLocked(udid, &s.wdaRelay)
			s.closeRelayLocked(udid, &s.wdaStreamRelay)
			delete(s.sessions, udid)
			delete(s.sessionURLs, udid)
			delete(s.wdaSessions, udid)
			delete(s.windowSizes, udid)
		}
	}
	for udid := range s.sessions {
		if !present[udid] {
			delete(s.sessions, udid)
			delete(s.sessionURLs, udid)
			delete(s.wdaSessions, udid)
			delete(s.windowSizes, udid)
		}
	}
	for udid := range s.wdaRelay.endpoints {
		if _, ok := deviceIDs[udid]; !ok {
			s.closeRelayLocked(udid, &s.wdaRelay)
		}
	}
	for udid := range s.wdaStreamRelay.endpoints {
		if _, ok := deviceIDs[udid]; !ok {
			s.closeRelayLocked(udid, &s.wdaStreamRelay)
		}
	}
	for udid, id := range deviceIDs {
		s.deviceIDs[udid] = id
	}
}

func (s *server) appiumURL(udid string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if endpoint := s.config.AppiumURLs[udid]; endpoint != "" {
		return endpoint
	}
	return runtimeCfg.DefaultAppiumURL
}

func (s *server) sessionURL(udid string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if endpoint := s.sessionURLs[udid]; endpoint != "" {
		return endpoint
	}
	if endpoint := s.config.AppiumURLs[udid]; endpoint != "" {
		return endpoint
	}
	return runtimeCfg.DefaultAppiumURL
}

func (s *server) isWDASession(udid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wdaSessions[udid]
}

func sessionType(isWDA bool) string {
	if isWDA {
		return "WDA"
	}
	return "Appium"
}
