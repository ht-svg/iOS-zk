package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decodeErrorResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v; body %q", err, response.Body.String())
	}
	return body
}

func TestAppErrorCodeIsPreservedThroughWrapping(t *testing.T) {
	cause := errors.New("upstream down")
	wrapped := fmt.Errorf("外层: %w", upstreamError(cause))

	appErr := appErrorFrom(wrapped)
	if appErr == nil {
		t.Fatal("appErrorFrom(wrapped) = nil")
	}
	if appErr.Code != CodeUpstream {
		t.Errorf("code = %q, want %q", appErr.Code, CodeUpstream)
	}
	if appErr.Status != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", appErr.Status, http.StatusBadGateway)
	}
	if got := errorCode(wrapped); got != CodeUpstream {
		t.Errorf("errorCode = %q, want %q", got, CodeUpstream)
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("wrapped appError lost its cause")
	}
}

func TestCodeForStatus(t *testing.T) {
	tests := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, CodeBadRequest},
		{http.StatusConflict, CodeSessionRequired},
		{http.StatusNotImplemented, CodeUnsupported},
		{http.StatusServiceUnavailable, CodeService},
		{http.StatusInternalServerError, CodeInternal},
		{http.StatusBadGateway, CodeUpstream},
		{http.StatusTeapot, CodeInternal},
	}
	for _, test := range tests {
		if got := codeForStatus(test.status); got != test.code {
			t.Errorf("codeForStatus(%d) = %q, want %q", test.status, got, test.code)
		}
	}
}

func TestWriteErrorIncludesStableCode(t *testing.T) {
	response := httptest.NewRecorder()
	writeError(response, http.StatusBadRequest, errors.New("参数错误"))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	body := decodeErrorResponse(t, response)
	if body["code"] != CodeBadRequest {
		t.Errorf("code = %q, want %q", body["code"], CodeBadRequest)
	}
	if body["error"] != "参数错误" {
		t.Errorf("error = %q, want %q", body["error"], "参数错误")
	}
}

func TestWriteAPIErrorHonorsAppErrorStatus(t *testing.T) {
	s := &server{}
	response := httptest.NewRecorder()
	s.writeAPIError(response, http.StatusBadGateway, fmt.Errorf("包装: %w", unsupportedError("当前模式不支持")))

	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
	body := decodeErrorResponse(t, response)
	if body["code"] != CodeUnsupported {
		t.Errorf("code = %q, want %q", body["code"], CodeUnsupported)
	}
}

func TestWriteAPIErrorMapsSessionLost(t *testing.T) {
	s := &server{}
	response := httptest.NewRecorder()
	s.writeAPIError(response, http.StatusBadGateway, fmt.Errorf("包装: %w", errSessionLost))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	body := decodeErrorResponse(t, response)
	if body["code"] != CodeSessionLost {
		t.Errorf("code = %q, want %q", body["code"], CodeSessionLost)
	}
}

func TestSessionIDUsesSessionRequiredCode(t *testing.T) {
	s := &server{sessions: map[string]string{}}
	_, err := s.sessionID("device-udid")
	if err == nil {
		t.Fatal("sessionID() error = nil, want session_required")
	}
	if got := errorCode(err); got != CodeSessionRequired {
		t.Errorf("errorCode = %q, want %q", got, CodeSessionRequired)
	}
	appErr := appErrorFrom(err)
	if appErr == nil || appErr.Status != http.StatusConflict {
		t.Fatalf("appError = %+v, want HTTP 409", appErr)
	}
}

func TestAppiumRequestMarksUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": map[string]string{"error": "boom", "message": "boom"},
		})
	}))
	defer upstream.Close()

	s := &server{httpClient: upstream.Client()}
	_, err := s.appiumRequest(context.Background(), upstream.URL, http.MethodGet, "/boom", nil)
	if err == nil {
		t.Fatal("appiumRequest() error = nil, want upstream error")
	}
	if got := errorCode(err); got != CodeUpstream {
		t.Errorf("errorCode = %q, want %q", got, CodeUpstream)
	}
}
