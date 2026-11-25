package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseUSBMuxDevices(t *testing.T) {
	input := `<?xml version="1.0"?>
<plist version="1.0">
<dict>
  <key>DeviceList</key>
  <array>
    <dict>
      <key>Properties</key>
      <dict>
        <key>DeviceID</key>
        <integer>42</integer>
        <key>SerialNumber</key>
        <string>00008030-001A11261489802E</string>
        <key>ConnectionType</key>
        <string>USB</string>
      </dict>
    </dict>
    <dict>
      <key>Properties</key>
      <dict>
        <key>UDID</key>
        <string>device-two-udid</string>
        <key>ConnectionType</key>
        <string>Network</string>
      </dict>
    </dict>
  </array>
</dict>
</plist>`
	devices, err := parseUSBMuxDevices([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}
	if devices[0].UDID != "00008030-001A11261489802E" || devices[0].ConnectionType != "usb" {
		t.Fatalf("unexpected first device: %+v", devices[0])
	}
	if devices[0].DeviceID != 42 {
		t.Fatalf("got device id %d, want 42", devices[0].DeviceID)
	}
}

func TestIsSessionInvalid(t *testing.T) {
	for _, message := range []string{
		"Appium 返回错误: invalid session id",
		"A session is either terminated or not started",
		"no such session",
	} {
		if !isSessionInvalid(errors.New(message)) {
			t.Errorf("%q should be treated as an invalid session", message)
		}
	}
	for _, message := range []string{"connection refused", "i/o timeout", "no such element"} {
		if isSessionInvalid(errors.New(message)) {
			t.Errorf("%q should not be treated as an invalid session", message)
		}
	}
}

func TestHandleCreateSessionRecreatesStaleSession(t *testing.T) {
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/old-session":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": map[string]string{
					"error":   "invalid session id",
					"message": "A session is either terminated or not started",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": map[string]string{"sessionId": "new-session"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer appium.Close()

	s := &server{
		config:      Config{AppiumURLs: map[string]string{"device-udid": appium.URL}},
		sessions:    map[string]string{"device-udid": "old-session"},
		sessionURLs: map[string]string{"device-udid": appium.URL},
		wdaSessions: map[string]bool{"device-udid": false},
		httpClient:  appium.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/devices/device-udid/session", strings.NewReader("{}"))
	request.SetPathValue("udid", "device-udid")
	response := httptest.NewRecorder()
	s.handleCreateSession(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handleCreateSession status = %d, response %s", response.Code, response.Body.String())
	}
	if got := s.sessions["device-udid"]; got != "new-session" {
		t.Fatalf("session = %q, want new-session", got)
	}
}

func TestReconcileDevicesDropsMissingSessions(t *testing.T) {
	s := &server{
		sessions:    map[string]string{"keep": "session-1", "gone": "session-2"},
		sessionURLs: map[string]string{"keep": "http://127.0.0.1:4723", "gone": "http://127.0.0.1:4723"},
		wdaSessions: map[string]bool{"keep": false, "gone": false},
		windowSizes: map[string]windowSizeEntry{"gone": {width: 390, height: 844}},
	}
	s.reconcileDevices([]muxDevice{{UDID: "keep", DeviceID: 1}})
	if got := s.sessions["keep"]; got != "session-1" {
		t.Fatalf("keep session = %q, want session-1", got)
	}
	if _, ok := s.sessions["gone"]; ok {
		t.Fatal("missing device session was not removed")
	}
}

func TestSessionRequestInvalidatesDeadSession(t *testing.T) {
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"value": map[string]string{"error": "invalid session id", "message": "invalid session id"},
		})
	}))
	defer appium.Close()

	s := &server{
		sessions:    map[string]string{"device-udid": "dead-session"},
		sessionURLs: map[string]string{"device-udid": appium.URL},
		wdaSessions: map[string]bool{"device-udid": false},
		httpClient:  appium.Client(),
	}
	_, err := s.sessionRequest(context.Background(), "device-udid", "dead-session", http.MethodGet, "/session/dead-session/screenshot", nil)
	if !errors.Is(err, errSessionLost) {
		t.Fatalf("error = %v, want errSessionLost", err)
	}
	if _, ok := s.sessions["device-udid"]; ok {
		t.Fatal("dead session was not removed")
	}
}

func TestPreviewScreenshot(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 800))
	for x := 0; x < 400; x++ {
		for y := 0; y < 800; y++ {
			src.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, src); err != nil {
		t.Fatal(err)
	}
	encoded, err := previewScreenshot(base64.StdEncoding.EncodeToString(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatal(err)
	}
	if bounds := img.Bounds(); bounds.Dx() != 180 || bounds.Dy() != 360 {
		t.Fatalf("preview bounds = %v, want 180x360", bounds)
	}
}

func TestHandleBatchReportsPartialFailures(t *testing.T) {
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/good/window/size":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]int{"width": 390, "height": 844}})
		case r.Method == http.MethodPost && r.URL.Path == "/session/good/actions":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
		case r.Method == http.MethodGet && r.URL.Path == "/session/bad/window/size":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": map[string]string{"error": "invalid session id", "message": "invalid session id"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer appium.Close()

	s := &server{
		config:      Config{Names: map[string]string{"good-device": "手机 A", "bad-device": "手机 B"}},
		sessions:    map[string]string{"good-device": "good", "bad-device": "bad"},
		sessionURLs: map[string]string{"good-device": appium.URL, "bad-device": appium.URL},
		wdaSessions: map[string]bool{"good-device": false, "bad-device": false},
		httpClient:  appium.Client(),
	}
	authorizeDevices(s, "good-device", "bad-device")
	body := `{"udids":["good-device","bad-device"],"operation":"tap","body":{"x":10,"y":20,"imageWidth":100,"imageHeight":200}}`
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handleBatch status = %d, response %s", response.Code, response.Body.String())
	}
	var result batchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Succeeded != 1 || result.Failed != 1 || result.OK {
		t.Fatalf("unexpected batch summary: %+v", result)
	}
	if !result.Results[0].OK || result.Results[0].Name != "手机 A" {
		t.Fatalf("unexpected good result: %+v", result.Results[0])
	}
	if result.Results[1].OK || result.Results[1].Code != "session_lost" || result.Results[1].Name != "手机 B" {
		t.Fatalf("unexpected bad result: %+v", result.Results[1])
	}
	if _, ok := s.sessions["bad-device"]; ok {
		t.Fatal("failed device session was not invalidated")
	}
}

func TestHandleBatchRejectsUnknownOperation(t *testing.T) {
	s := &server{}
	body := `{"udids":["good-device"],"operation":"unknown","body":{}}`
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("handleBatch status = %d, want 400; response %s", response.Code, response.Body.String())
	}
}

func TestHandleBatchReportsMissingSession(t *testing.T) {
	s := &server{sessions: map[string]string{}}
	authorizeDevices(s, "good-device")
	body := `{"udids":["good-device"],"operation":"tap","body":{"x":1,"y":2,"imageWidth":10,"imageHeight":20}}`
	request := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleBatch(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handleBatch status = %d, response %s", response.Code, response.Body.String())
	}
	var result batchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 || result.Results[0].Code != "session_required" {
		t.Fatalf("unexpected batch result: %+v", result)
	}
}

func TestReconcileDevicesInvalidatesSessionWhenDeviceIDChanges(t *testing.T) {
	s := &server{
		sessions:    map[string]string{"device-udid": "session-1"},
		sessionURLs: map[string]string{"device-udid": "http://127.0.0.1:4723"},
		wdaSessions: map[string]bool{"device-udid": false},
		deviceIDs:   map[string]uint32{"device-udid": 1},
	}
	s.reconcileDevices([]muxDevice{{UDID: "device-udid", DeviceID: 2}})
	if _, ok := s.sessions["device-udid"]; ok {
		t.Fatal("session should be invalidated after usbmuxd DeviceID changes")
	}
	if got := s.deviceIDs["device-udid"]; got != 2 {
		t.Fatalf("device ID = %d, want 2", got)
	}
}

func TestResizeToWidth(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x := 0; x < 4; x++ {
		for y := 0; y < 2; y++ {
			src.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	dst := resizeToWidth(src, 2)
	if bounds := dst.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 1 {
		t.Fatalf("resized bounds = %v, want 2x1", bounds)
	}
}

func TestSessionURLUsesWDAEndpoint(t *testing.T) {
	s := &server{
		config:      Config{AppiumURLs: map[string]string{"device-udid": "http://127.0.0.1:4723"}},
		sessionURLs: map[string]string{"device-udid": "http://127.0.0.1:49152"},
	}
	if got := s.sessionURL("device-udid"); got != "http://127.0.0.1:49152" {
		t.Fatalf("sessionURL got %q, want WDA endpoint", got)
	}
}

func TestParseUSBMuxError(t *testing.T) {
	input := `<plist><dict><key>Number</key><integer>3</integer></dict></plist>`
	_, err := parseUSBMuxDevices([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "错误码 3") {
		t.Fatalf("expected usbmux error code, got %v", err)
	}
}

func TestMessageTargetAndUnreadNumber(t *testing.T) {
	tests := []struct {
		app, xpath, attr, mode string
	}{
		{"WhatsApp", `//XCUIElementTypeButton[@index="3"]`, "value", "whatsapp"},
		{"微信", `//XCUIElementTypeNavigationBar[@index="0"]`, "name", "digits"},
		{"抖音", `//XCUIElementTypeButton[@index="7"]`, "name", "tiktok"},
		{"未知应用", "", "", ""},
	}
	for _, test := range tests {
		xpath, attr, mode := messageTarget(test.app)
		if xpath != test.xpath || attr != test.attr || mode != test.mode {
			t.Errorf("%s target = (%q, %q, %q)", test.app, xpath, attr, mode)
		}
	}
	if got := firstNumber("3 条新消息"); got != 3 {
		t.Fatalf("firstNumber got %d, want 3", got)
	}
	if got := firstNumber("没有消息"); got != 0 {
		t.Fatalf("firstNumber got %d, want 0", got)
	}
}

func TestValidateLocalURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:4723", "http://localhost:4723"} {
		if err := validateLocalURL(raw); err != nil {
			t.Errorf("%s rejected: %v", raw, err)
		}
	}
	if err := validateLocalURL("http://example.com:4723"); err == nil {
		t.Fatal("remote Appium URL should be rejected")
	}
}

func TestScaleTouchPointFromScreenshotPixels(t *testing.T) {
	x, y := scaleTouchPoint(585, 1266, 1170, 2532, 390, 844)
	if x != 195 || y != 422 {
		t.Fatalf("scaled point = (%d, %d), want (195, 422)", x, y)
	}
}

func TestValidateImagePoint(t *testing.T) {
	if err := validateImagePoint(9, 19, 10, 20); err != nil {
		t.Fatalf("valid image point rejected: %v", err)
	}
	if err := validateImagePoint(10, 19, 10, 20); err == nil {
		t.Fatal("point outside screenshot should be rejected")
	}
}

func TestWriteErrorLogsChineseMessage(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previousOutput)

	response := httptest.NewRecorder()
	writeError(response, http.StatusBadGateway, errors.New("Appium 返回错误: Unhandled endpoint: /wda/example"))
	if !strings.Contains(output.String(), "event=http_error") ||
		!strings.Contains(output.String(), "设备上的 WDA 不支持此操作接口") {
		t.Fatalf("expected a structured Chinese console error, got %q", output.String())
	}
}

func TestCreateSessionDisablesPerDeviceXcodeLog(t *testing.T) {
	var capabilities map[string]any
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		caps := body["capabilities"].(map[string]any)
		capabilities = caps["alwaysMatch"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"sessionId": "session-1"}})
	}))
	defer appium.Close()

	s := &server{
		config:      Config{AppiumURLs: map[string]string{"device-udid": appium.URL}},
		sessions:    make(map[string]string),
		sessionURLs: make(map[string]string),
		wdaSessions: make(map[string]bool),
		httpClient:  appium.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/devices/device-udid/session", strings.NewReader("{}"))
	request.SetPathValue("udid", "device-udid")
	response := httptest.NewRecorder()
	s.handleCreateSession(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handleCreateSession status = %d, response %s", response.Code, response.Body.String())
	}
	if capabilities["appium:showXcodeLog"] != false {
		t.Fatalf("showXcodeLog = %v, want false", capabilities["appium:showXcodeLog"])
	}
}

func TestHandleTapScalesScreenshotCoordinates(t *testing.T) {
	var action map[string]any
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/window/size":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]int{"width": 390, "height": 844}})
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/actions":
			if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
		default:
			http.NotFound(w, r)
		}
	}))
	defer appium.Close()

	s := &server{
		sessions:    map[string]string{"device-udid": "session-1"},
		sessionURLs: map[string]string{"device-udid": appium.URL},
		httpClient:  appium.Client(),
	}
	authorizeDevices(s, "device-udid")
	request := httptest.NewRequest(http.MethodPost, "/api/devices/device-udid/tap",
		strings.NewReader(`{"x":585,"y":1266,"imageWidth":1170,"imageHeight":2532}`))
	request.SetPathValue("udid", "device-udid")
	response := httptest.NewRecorder()
	s.handleTap(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handleTap status = %d, response %s", response.Code, response.Body.String())
	}

	actions := action["actions"].([]any)
	pointer := actions[0].(map[string]any)
	pointerActions := pointer["actions"].([]any)
	move := pointerActions[0].(map[string]any)
	if move["x"] != float64(195) || move["y"] != float64(422) {
		t.Fatalf("tap point = (%v, %v), want (195, 422)", move["x"], move["y"])
	}
}

func TestHandleSwipeUsesW3CActions(t *testing.T) {
	var action map[string]any
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/window/size":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]int{"width": 390, "height": 844}})
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/actions":
			if err := json.NewDecoder(r.Body).Decode(&action); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
		default:
			http.NotFound(w, r)
		}
	}))
	defer appium.Close()

	s := &server{
		sessions:    map[string]string{"device-udid": "session-1"},
		sessionURLs: map[string]string{"device-udid": appium.URL},
		wdaSessions: map[string]bool{"device-udid": true},
		httpClient:  appium.Client(),
	}
	authorizeDevices(s, "device-udid")
	request := httptest.NewRequest(http.MethodPost, "/api/devices/device-udid/swipe",
		strings.NewReader(`{"x1":585,"y1":1266,"x2":585,"y2":422,"durationMs":500,"imageWidth":1170,"imageHeight":2532}`))
	request.SetPathValue("udid", "device-udid")
	response := httptest.NewRecorder()
	s.handleSwipe(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handleSwipe status = %d, response %s", response.Code, response.Body.String())
	}
	actions := action["actions"].([]any)
	pointer := actions[0].(map[string]any)
	pointerActions := pointer["actions"].([]any)
	moveToStart := pointerActions[0].(map[string]any)
	moveToEnd := pointerActions[2].(map[string]any)
	if moveToStart["x"] != float64(195) || moveToStart["y"] != float64(422) ||
		moveToEnd["x"] != float64(195) || moveToEnd["y"] != float64(141) ||
		moveToEnd["duration"] != float64(500) {
		t.Fatalf("unexpected W3C WDA drag payload: %#v", action)
	}
}

func TestStreamRouteUsesMultipartMJPEG(t *testing.T) {
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/session/session-1/screenshot":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": ""})
		default:
			http.NotFound(w, r)
		}
	}))
	defer appium.Close()

	s := &server{
		sessions:    map[string]string{"device-udid": "session-1"},
		sessionURLs: map[string]string{"device-udid": appium.URL},
		httpClient:  appium.Client(),
	}
	request := httptest.NewRequest(http.MethodGet, "/api/devices/device-udid/stream", nil)
	request.SetPathValue("udid", "device-udid")
	request = request.WithContext(request.Context())
	response := httptest.NewRecorder()
	s.handleStream(response, request)
	if got := response.Header().Get("Content-Type"); got != "" &&
		got != "multipart/x-mixed-replace; boundary=go-phone-frame" {
		t.Fatalf("unexpected stream content type: %q", got)
	}
}

func TestActionDeviceSendsEnterKey(t *testing.T) {
	var payload map[string]any
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/session-1/actions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"value": nil})
	}))
	defer appium.Close()

	if err := validateActionInput(actionInput{Action: "enter"}); err != nil {
		t.Fatalf("validateActionInput(enter) = %v", err)
	}

	s := &server{
		sessions:    map[string]string{"device-udid": "session-1"},
		sessionURLs: map[string]string{"device-udid": appium.URL},
		wdaSessions: map[string]bool{"device-udid": true},
		httpClient:  appium.Client(),
	}
	if err := s.actionDevice(context.Background(), "device-udid", "session-1", actionInput{Action: "enter"}); err != nil {
		t.Fatalf("actionDevice(enter) = %v", err)
	}

	actions, ok := payload["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("unexpected actions payload: %#v", payload)
	}
	keyboard, ok := actions[0].(map[string]any)
	if !ok || keyboard["type"] != "key" || keyboard["id"] != "keyboard" {
		t.Fatalf("unexpected keyboard action: %#v", actions[0])
	}
	keyActions, ok := keyboard["actions"].([]any)
	if !ok || len(keyActions) != 2 {
		t.Fatalf("unexpected key actions: %#v", keyboard["actions"])
	}
	keyDown, _ := keyActions[0].(map[string]any)
	keyUp, _ := keyActions[1].(map[string]any)
	if keyDown["type"] != "keyDown" || keyDown["value"] != "\uE007" ||
		keyUp["type"] != "keyUp" || keyUp["value"] != "\uE007" {
		t.Fatalf("unexpected Enter key actions: %#v", keyActions)
	}
}
