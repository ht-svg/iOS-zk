package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type contextKey string

const requestIDKey contextKey = "request_id"

var requestSequence uint64

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

// Flush is required by the MJPEG handlers. Without forwarding it, wrapping
// the ResponseWriter would silently break streaming.
func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *server) withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := fmt.Sprintf("%x-%08x", time.Now().UnixNano(), atomic.AddUint64(&requestSequence, 1))
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		recorder := &statusRecorder{ResponseWriter: w}
		started := time.Now()

		defer func() {
			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}
			log.Printf("event=request_completed request_id=%s method=%s path=%q status=%d duration_ms=%d",
				requestID, r.Method, r.URL.Path, status, time.Since(started).Milliseconds())
		}()

		next.ServeHTTP(recorder, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return "-"
}

func logDeviceEvent(ctx context.Context, level, udid, event string, err error) {
	fields := []string{
		"level=" + level,
		"request_id=" + requestIDFromContext(ctx),
		"udid=" + udid,
		"event=" + event,
	}
	if err != nil {
		fields = append(fields, "error="+fmt.Sprintf("%q", err.Error()))
	}
	log.Print(strings.Join(fields, " "))
}
