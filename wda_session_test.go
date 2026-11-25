package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// WDA only reports a session in /status on some builds. Adopting a running WDA
// must therefore open a session itself instead of failing.
func TestCreateWDASessionReturnsSessionID(t *testing.T) {
	var method, path string
	var body map[string]any
	wda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessionId": "WDA-SESSION-1",
			"value":     map[string]any{"sessionId": "WDA-SESSION-1", "capabilities": map[string]any{}},
		})
	}))
	defer wda.Close()

	s := &server{httpClient: wda.Client()}
	sessionID, err := s.createWDASession(context.Background(), wda.URL)
	if err != nil {
		t.Fatalf("createWDASession returned error: %v", err)
	}
	if sessionID != "WDA-SESSION-1" {
		t.Fatalf("sessionID = %q, want WDA-SESSION-1", sessionID)
	}
	if method != http.MethodPost || path != "/session" {
		t.Fatalf("request = %s %s, want POST /session", method, path)
	}
	capabilities, ok := body["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no capabilities: %v", body)
	}
	if _, ok := capabilities["alwaysMatch"].(map[string]any); !ok {
		t.Fatalf("capabilities.alwaysMatch missing: %v", capabilities)
	}
}

func TestCreateWDASessionRejectsMissingSessionID(t *testing.T) {
	wda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"capabilities": map[string]any{}}})
	}))
	defer wda.Close()

	s := &server{httpClient: wda.Client()}
	if _, err := s.createWDASession(context.Background(), wda.URL); err == nil {
		t.Fatal("createWDASession should fail when WDA omits sessionId")
	}
}

func TestProbeWDASessionToleratesStatusWithoutSessionID(t *testing.T) {
	wda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status":
			// Deliberately no sessionId, which is the common WDA response.
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"ready": true}})
		case "/session/live/window/size":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"width": 390, "height": 844}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wda.Close()

	s := &server{
		sessions:    map[string]string{"dev": "live"},
		sessionURLs: map[string]string{"dev": wda.URL},
		wdaSessions: map[string]bool{"dev": true},
		httpClient:  wda.Client(),
	}
	if err := s.probeSession(context.Background(), "dev", "live"); err != nil {
		t.Fatalf("probeSession = %v, want nil (a missing sessionId must not mean lost)", err)
	}
}

func TestProbeWDASessionDetectsDifferentSessionID(t *testing.T) {
	wda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessionId": "other",
				"value":     map[string]any{"ready": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wda.Close()

	s := &server{
		sessions:    map[string]string{"dev": "live"},
		sessionURLs: map[string]string{"dev": wda.URL},
		wdaSessions: map[string]bool{"dev": true},
		httpClient:  wda.Client(),
	}
	if err := s.probeSession(context.Background(), "dev", "live"); err != errSessionLost {
		t.Fatalf("probeSession = %v, want errSessionLost", err)
	}
}

func TestProbeWDASessionDetectsInvalidSession(t *testing.T) {
	wda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"ready": true}})
		case "/session/live/window/size":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": map[string]string{"error": "invalid session id", "message": "invalid session id"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wda.Close()

	s := &server{
		sessions:    map[string]string{"dev": "live"},
		sessionURLs: map[string]string{"dev": wda.URL},
		wdaSessions: map[string]bool{"dev": true},
		httpClient:  wda.Client(),
	}
	if err := s.probeSession(context.Background(), "dev", "live"); err != errSessionLost {
		t.Fatalf("probeSession = %v, want errSessionLost", err)
	}
}
