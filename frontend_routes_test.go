package main

import (
	"regexp"
	"testing"
)

// The UI builds device URLs as /api/devices/{udid}/{operation}, so every
// operation it sends must match a registered route segment. A hardcoded
// mapping previously sent tap and swipe to /type, which the strict JSON
// decoder rejected with `unknown field "x"`.
func TestFrontendOperationsMatchDeviceRoutes(t *testing.T) {
	routes := map[string]bool{
		"tap":       true,
		"swipe":     true,
		"type":      true,
		"actions":   true,
		"app":       true,
		"clipboard": true,
	}

	source, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read embedded web/index.html: %v", err)
	}
	matches := regexp.MustCompile(`singleOrBatch\("([a-zA-Z]+)"`).FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("no singleOrBatch calls found; the regex or the UI changed")
	}
	for _, match := range matches {
		operation := match[1]
		if !routes[operation] {
			t.Fatalf("singleOrBatch(%q) has no matching /api/devices/{udid}/%s route", operation, operation)
		}
	}
}

// Guards the specific regression: the UI must not re-introduce a hand-written
// operation-to-endpoint mapping.
func TestFrontendDoesNotRemapDeviceEndpoints(t *testing.T) {
	source, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read embedded web/index.html: %v", err)
	}
	if regexp.MustCompile(`const\s+endpoint\s*=`).Match(source) {
		t.Fatal("web/index.html reintroduced an endpoint mapping; build the path from the operation directly")
	}
}
