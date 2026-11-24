package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type capturedRequest struct {
	Path string
	Text string
}

func newBatchTypeServer(t *testing.T, captured *[]capturedRequest, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/element/active"):
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]string{"element-6066-11e4-a52e-4f735466cecf": "el-1"}})
		case strings.HasSuffix(r.URL.Path, "/element/el-1/value"):
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			mu.Lock()
			*captured = append(*captured, capturedRequest{Path: r.URL.Path, Text: stringValue(payload["text"])})
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestNormalizeBatchTargetsLegacy(t *testing.T) {
	request := batchRequest{
		UDIDs: []string{"a", "b"},
		Body:  json.RawMessage(`{"text":"hi"}`),
	}
	targets, err := normalizeBatchTargets(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].UDID != "a" || targets[1].UDID != "b" {
		t.Fatalf("unexpected targets: %+v", targets)
	}
	for _, target := range targets {
		if !target.HasBody || string(target.Body) != `{"text":"hi"}` {
			t.Fatalf("legacy target should inherit shared body: %+v", target)
		}
	}
}

func TestNormalizeBatchTargetsPerDeviceWithInherit(t *testing.T) {
	request := batchRequest{
		Body: json.RawMessage(`{"text":"default"}`),
		Targets: []batchTarget{
			{UDID: "a", Body: json.RawMessage(`{"text":"custom-a"}`)},
			{UDID: "b"},
		},
	}
	targets, err := normalizeBatchTargets(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(targets[0].Body) != `{"text":"custom-a"}` || !targets[0].HasBody {
		t.Fatalf("custom body not kept: %+v", targets[0])
	}
	if string(targets[1].Body) != `{"text":"default"}` || !targets[1].HasBody {
		t.Fatalf("missing per-device body should inherit shared body: %+v", targets[1])
	}
}

func TestHandleBatchDifferentiatedType(t *testing.T) {
	var captured []capturedRequest
	var mu sync.Mutex
	appium := newBatchTypeServer(t, &captured, &mu)
	defer appium.Close()

	s := &server{
		config:      Config{Names: map[string]string{"device-aaaa1111": "手机 A", "device-bbbb2222": "手机 B"}},
		sessions:    map[string]string{"device-aaaa1111": "session-1", "device-bbbb2222": "session-1"},
		sessionURLs: map[string]string{"device-aaaa1111": appium.URL, "device-bbbb2222": appium.URL},
		wdaSessions: map[string]bool{"device-aaaa1111": false, "device-bbbb2222": false},
		httpClient:  appium.Client(),
	}

	body := `{"operation":"type","targets":[` +
		`{"udid":"device-aaaa1111","body":{"text":"给 A 的内容"}},` +
		`{"udid":"device-bbbb2222","body":{"text":"给 B 的内容"}}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var result batchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Succeeded != 2 || result.Failed != 0 || !result.OK {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if result.Results[0].Name != "手机 A" || result.Results[1].Name != "手机 B" {
		t.Fatalf("results should keep target order with names: %+v", result.Results)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("expected 2 upstream type requests, got %d", len(captured))
	}
	texts := map[string]bool{}
	for _, item := range captured {
		texts[item.Text] = true
	}
	if !texts["给 A 的内容"] || !texts["给 B 的内容"] {
		t.Fatalf("each device should receive its own text: %+v", captured)
	}
}

func TestHandleBatchDifferentiatedIsolatesBadPayload(t *testing.T) {
	var captured []capturedRequest
	var mu sync.Mutex
	appium := newBatchTypeServer(t, &captured, &mu)
	defer appium.Close()

	s := &server{
		sessions:    map[string]string{"device-aaaa1111": "session-1", "device-bbbb2222": "session-1"},
		sessionURLs: map[string]string{"device-aaaa1111": appium.URL, "device-bbbb2222": appium.URL},
		wdaSessions: map[string]bool{"device-aaaa1111": false, "device-bbbb2222": false},
		httpClient:  appium.Client(),
	}

	overlong := strings.Repeat("x", 5000)
	payload, _ := json.Marshal(map[string]any{
		"operation": "type",
		"targets": []map[string]any{
			{"udid": "device-aaaa1111", "body": map[string]string{"text": overlong}},
			{"udid": "device-bbbb2222", "body": map[string]string{"text": "有效内容"}},
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(string(payload)))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", response.Code, response.Body.String())
	}
	var result batchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Succeeded != 1 || result.Failed != 1 || result.OK {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if result.Results[0].OK || result.Results[0].Code != CodeBadRequest {
		t.Fatalf("bad payload device should fail with bad_request: %+v", result.Results[0])
	}
	if !result.Results[1].OK {
		t.Fatalf("valid payload device should still succeed: %+v", result.Results[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 || captured[0].Text != "有效内容" {
		t.Fatalf("only valid device should reach upstream: %+v", captured)
	}
}

func TestHandleBatchRejectsDuplicateTargets(t *testing.T) {
	s := &server{}
	body := `{"operation":"type","targets":[{"udid":"device-aaaa1111","body":{"text":"a"}},{"udid":"device-aaaa1111","body":{"text":"b"}}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", response.Code, response.Body.String())
	}
}

func TestHandleBatchTargetsRejectUnknownOperation(t *testing.T) {
	s := &server{}
	body := `{"operation":"nope","targets":[{"udid":"device-aaaa1111","body":{"text":"a"}}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", response.Code, response.Body.String())
	}
}

func TestValidateAppInput(t *testing.T) {
	if err := validateAppInput(appInput{Action: "open", BundleID: "com.example.App"}); err != nil {
		t.Fatalf("valid open rejected: %v", err)
	}
	if err := validateAppInput(appInput{Action: "uninstall", BundleID: "com.example.App-2"}); err != nil {
		t.Fatalf("valid uninstall rejected: %v", err)
	}
	for _, input := range []appInput{
		{Action: "restart", BundleID: "com.example.App"},
		{Action: "open", BundleID: ""},
		{Action: "open", BundleID: "com example"},
	} {
		if err := validateAppInput(input); err == nil {
			t.Fatalf("invalid app input accepted: %+v", input)
		}
	}
}

func TestActionDeviceAppOpenAndUninstall(t *testing.T) {
	type call struct {
		Path   string
		Script string
		Bundle string
	}
	var calls []call
	var mu sync.Mutex
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		var script, bundle string
		if value, ok := payload["script"].(string); ok {
			script = value
		}
		if args, ok := payload["args"].([]any); ok && len(args) > 0 {
			if m, ok := args[0].(map[string]any); ok {
				bundle = stringValue(m["bundleId"])
			}
		}
		mu.Lock()
		calls = append(calls, call{Path: r.URL.Path, Script: script, Bundle: bundle})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
	}))
	defer appium.Close()

	s := &server{
		sessions:    map[string]string{"device-aaaa1111": "session-1"},
		sessionURLs: map[string]string{"device-aaaa1111": appium.URL},
		wdaSessions: map[string]bool{"device-aaaa1111": false},
		httpClient:  appium.Client(),
	}
	if err := s.appDevice(context.Background(), "device-aaaa1111", "session-1", appInput{Action: "open", BundleID: "com.example.App"}); err != nil {
		t.Fatalf("app open failed: %v", err)
	}
	if err := s.appDevice(context.Background(), "device-aaaa1111", "session-1", appInput{Action: "uninstall", BundleID: "com.example.App"}); err != nil {
		t.Fatalf("app uninstall failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 app calls, got %d", len(calls))
	}
	if calls[0].Script != "mobile: launchApp" || calls[1].Script != "mobile: removeApp" {
		t.Fatalf("unexpected mobile scripts: %+v", calls)
	}
	for _, c := range calls {
		if !strings.HasSuffix(c.Path, "/execute/sync") || c.Bundle != "com.example.App" {
			t.Fatalf("unexpected app call: %+v", c)
		}
	}
}

func TestAppDeviceWDAUninstallUnsupported(t *testing.T) {
	s := &server{wdaSessions: map[string]bool{"device-aaaa1111": true}}
	err := s.appDevice(context.Background(), "device-aaaa1111", "session-1", appInput{Action: "uninstall", BundleID: "com.example.App"})
	if err == nil || errorCode(err) != CodeUnsupported {
		t.Fatalf("WDA uninstall should be unsupported, got %v", err)
	}
}
