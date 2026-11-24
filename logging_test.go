package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestLoggingEmitsStructuredLog(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	s := &server{}
	handler := s.withRequestLogging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/devices", nil))

	logged := output.String()
	for _, fragment := range []string{"event=request_completed", "request_id=", "method=GET", "status=418", "duration_ms="} {
		if !strings.Contains(logged, fragment) {
			t.Fatalf("log missing %q: %s", fragment, logged)
		}
	}
}

func TestLogDeviceEventIncludesContextAndError(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	ctx := context.WithValue(context.Background(), requestIDKey, "req-42")
	logDeviceEvent(ctx, "warn", "device-udid", "stream_failed", errors.New("boom"))

	logged := output.String()
	for _, fragment := range []string{"level=warn", "request_id=req-42", "udid=device-udid", "event=stream_failed", "error="} {
		if !strings.Contains(logged, fragment) {
			t.Fatalf("log missing %q: %s", fragment, logged)
		}
	}
}
