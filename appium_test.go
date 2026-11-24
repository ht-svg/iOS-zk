package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadUnreadCountForWeChat(t *testing.T) {
	const elementKey = "element-6066-11e4-a52e-4f735466cecf"
	appium := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/element":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			id := "tab"
			if body["value"] == `//XCUIElementTypeApplication[@index="0"]` {
				id = "application"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"value": map[string]string{elementKey: id}})
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/element/application/attribute/name":
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "WeChat"})
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/element/tab/attribute/name":
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "7 unread"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer appium.Close()

	s := &server{
		config:     Config{AppiumURLs: map[string]string{"test-device-udid": appium.URL}},
		httpClient: appium.Client(),
	}
	count, app, err := s.readUnreadCount(context.Background(), "test-device-udid", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if app != "WeChat" || count != 7 {
		t.Fatalf("got app=%q unread=%d, want WeChat/7", app, count)
	}
}
