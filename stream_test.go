package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyMJPEGStreamPassesThroughFrames(t *testing.T) {
	const boundary = "go-test-boundary"
	upstreamBody := "--" + boundary + "\r\nContent-Type: image/jpeg\r\nContent-Length: 5\r\n\r\nframe\r\n--" + boundary + "--\r\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity", got)
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/stream", nil)
	server := &server{}
	server.proxyMJPEGStream(recorder, request, "device-udid", upstream.URL)

	gotType := recorder.Header().Get("Content-Type")
	if !strings.Contains(gotType, "multipart/x-mixed-replace") {
		t.Fatalf("Content-Type = %q, want MJPEG stream", gotType)
	}
	if !strings.Contains(recorder.Body.String(), "frame") {
		t.Fatalf("stream body = %q, want embedded frame", recorder.Body.String())
	}
	if recorder.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", recorder.Header().Get("X-Accel-Buffering"))
	}
}
